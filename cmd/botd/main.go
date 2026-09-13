// Команда botd — демон ботов.
//
// Ходит за данными в raspd по unix-сокету и ничего не знает ни про адаптер, ни
// про SQLite. Наружу смотрит только длинным опросом: ни одного входящего
// порта на машине.
package main

import (
	"context"
	"errors"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/botcore"
	"github.com/UwUOcha/dairy-303-public/internal/config"
	"github.com/UwUOcha/dairy-303-public/internal/logbuf"
	"github.com/UwUOcha/dairy-303-public/internal/tg"
	"github.com/UwUOcha/dairy-303-public/internal/vk"
)

// startedAt — момент запуска процесса. Аптайм бота панель считает от него, а
// не от первого отчёта: между стартом и первым heartbeat проходит полминуты.
var startedAt = time.Now()

func main() {
	if err := profile.Init(); err != nil {
		slog.Error("профиль установки", "ошибка", err)
		os.Exit(1)
	}
	level := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		level = slog.LevelDebug
	}
	// Жалобы бота попадают и в journald, и в кольцевой буфер: у botd нет
	// входящего порта, поэтому в панель они уезжают отчётом вместе со
	// счётчиками.
	ring := logbuf.NewRing(logbuf.Capacity)
	handler := logbuf.NewHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}), ring, slog.LevelWarn)
	log := slog.New(handler)
	slog.SetDefault(log)

	if err := run(log, ring); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("botd остановлен с ошибкой", "ошибка", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, ring *logbuf.Ring) error {
	cfg, err := config.LoadBot()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := api.NewClient(cfg.SocketPath)
	if err := waitForRasp(ctx, client, log); err != nil {
		return err
	}

	core := botcore.New(client, cfg.TZOffset, log, botcore.WithAuthor(botcore.Author{
		TG:     cfg.AdminTG,
		VK:     cfg.AdminVK,
		LinkTG: cfg.LinkTG,
		LinkVK: cfg.LinkVK,
	}))

	adapters, err := platforms(cfg, core, log)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reportLoop(ctx, client, core.Metrics(), ring, log)
	}()

	for _, p := range adapters {
		wg.Add(3)
		go func() {
			defer wg.Done()
			log.Info("бот запущен", "платформа", p.Platform())
			p.Run(ctx)
		}()
		go func() {
			defer wg.Done()
			botcore.NewNotifier(core, client, p, p.rps, log).Run(ctx)
		}()
		// Обход «кто ещё в диалоге» живёт рядом с рассыльщиком и в том же
		// процессе: спросить площадку о человеке может только тот, у кого есть
		// её ключ, а у raspd его нет и не должно быть.
		go func() {
			defer wg.Done()
			botcore.NewProber(client, p, log).Run(ctx)
		}()
	}

	wg.Wait()
	return ctx.Err()
}

// platform — адаптер одной площадки: и приём событий, и отправка рассылки.
//
// Reacher, а не просто Sender: кроме отправки от площадки нужен ещё один
// навык — ответить, жив ли диалог с человеком, которому бот давно не писал.
type platform struct {
	botcore.Reacher
	// Run слушает входящие события до отмены контекста.
	Run func(context.Context)
	// rps — сколько сообщений в секунду площадка готова принимать. Это
	// единственное реальное узкое место всей системы: база отдаёт неделю
	// занятий за доли миллисекунды, а вот залп на пять тысяч подписчиков
	// упирается ровно сюда.
	rps float64
}

// platforms собирает адаптеры, для которых заданы токены.
//
// Адаптеры тонкие, сценарии и кэши общие — разделять их по процессам незачем,
// пока один длинный опрос не начал мешать другому.
func platforms(cfg config.Bot, core *botcore.Bot, log *slog.Logger) ([]platform, error) {
	var out []platform

	if cfg.TelegramToken != "" {
		a, err := tg.New(cfg.TelegramToken, core, log)
		if err != nil {
			return nil, err
		}
		// Телеграм пускает около 30 сообщений в секунду; берём с запасом.
		out = append(out, platform{Reacher: a, Run: a.Run, rps: 25})
	}

	if cfg.VKToken != "" {
		a, err := vk.New(cfg.VKToken, cfg.VKRequireSub, core, log)
		if err != nil {
			return nil, err
		}
		// Ключ сообщества пускает 20 запросов в секунду на все методы разом,
		// так что оставляем запас на служебные вызовы адаптера.
		out = append(out, platform{Reacher: a, Run: a.Run, rps: 15})
	}

	return out, nil
}

// waitForRasp дожидается готовности raspd.
//
// systemd поднимает юниты параллельно, и бот вполне может стартовать раньше,
// чем демон расписания успеет создать сокет. Падать из-за этого и уходить в
// цикл перезапусков незачем — достаточно подождать.
func waitForRasp(ctx context.Context, client *api.Client, log *slog.Logger) error {
	const attempts = 30
	for i := 1; ; i++ {
		health, err := client.Health(ctx)
		if err == nil {
			log.Info("raspd на связи", "групп", health.Groups, "занятий", health.Lessons)
			return nil
		}
		if i >= attempts {
			return err
		}
		log.Warn("жду raspd", "попытка", i, "ошибка", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// reportInterval — как часто бот отчитывается перед демоном расписания.
//
// Полминуты выбраны из того, что панель обновляется раз в десять секунд и
// должна замечать смерть бота на глаз, а не через минуты. Отчёт — это один
// POST в юникс-сокет с килобайтом JSON, так что чаще незачем, а реже уже
// заметно.
const reportInterval = 30 * time.Second

// reportLoop шлёт демону расписания счётчики бота и его последние жалобы.
//
// Направление именно такое, потому что у botd нет ни одного входящего порта, и
// заводить его ради статистики значило бы разменять главное свойство демона —
// «наружу смотрит только длинным опросом» — на удобство админки.
//
// Ошибка отчёта не мешает боту работать: это диагностика, а не сценарий.
// Логируется она на уровне debug, иначе перезапуск raspd залил бы буфер
// собственными жалобами о том, что некому жаловаться.
func reportLoop(ctx context.Context, client *api.Client, metrics *botcore.Metrics, ring *logbuf.Ring, log *slog.Logger) {
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()

	report := func() {
		rep := api.BotReport{
			Runtime:   api.SampleRuntime(startedAt),
			Platforms: metrics.Snapshot(),
			Log:       ring.Entries(),
		}
		if err := client.ReportBot(ctx, rep); err != nil && ctx.Err() == nil {
			log.Debug("не удалось отчитаться перед raspd", "ошибка", err)
		}
	}

	// Первый отчёт сразу: иначе панель полминуты после деплоя показывала бы
	// бота мёртвым.
	report()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report()
		}
	}
}
