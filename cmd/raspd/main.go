// Команда raspd — фоновый демон расписания.
//
// Держит локальную копию расписания вуза в SQLite и отдаёт её ботам по
// unix-сокету. Получает нормализованные данные через HTTP-адаптер.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/config"
	"github.com/UwUOcha/dairy-303-public/internal/logbuf"
	"github.com/UwUOcha/dairy-303-public/internal/providerclient"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"github.com/UwUOcha/dairy-303-public/internal/syncer"
)

// startedAt — момент запуска процесса, от него панель считает аптайм демона.
var startedAt = time.Now()

func main() {
	if err := profile.Init(); err != nil {
		slog.Error("профиль установки", "ошибка", err)
		os.Exit(1)
	}
	health := flag.Bool("health", false, "проверить живость демона через сокет и выйти")
	background := flag.Bool("background-sync", true, "фоновые обходы; отключать только для ограниченной проверки на копии базы")
	botKey := flag.String("bot-key", "", "личный API-ключ владельца из профиля: create, rotate, revoke, status")
	flag.Parse()
	if *botKey != "" {
		if *health || flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "-bot-key используется отдельно")
			os.Exit(1)
		}
		if err := manageBotKey(*botKey); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Жалобы демона идут и в journald, и в кольцевой буфер: последний отдаётся
	// админ-панели, чтобы «что тут ругалось за последний час» не требовало ssh.
	ring := logbuf.NewRing(logbuf.Capacity)
	handler := logbuf.NewHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}), ring, slog.LevelWarn)
	log := slog.New(handler)
	slog.SetDefault(log)

	if *health {
		if err := checkHealth(); err != nil {
			log.Error("демон не отвечает", "ошибка", err)
			os.Exit(1)
		}
		return
	}

	if err := run(log, ring, *background); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("raspd остановлен с ошибкой", "ошибка", err)
		os.Exit(1)
	}
}

func manageBotKey(op string) error {
	if op != "create" && op != "rotate" && op != "revoke" && op != "status" {
		return errors.New("используйте -bot-key create|rotate|revoke|status")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := api.NewClient(config.Socket()).BotKey(ctx, op)
	if err != nil {
		return err
	}
	if out.Token != "" {
		// stdout contains only the secret, so it can be redirected to a private file.
		fmt.Println(out.Token)
	} else if out.Active {
		fmt.Printf("Ключ активен · Telegram %s · создан %s\n", store.BotKeyOwner(), time.Unix(out.CreatedAt, 0).UTC().Format(time.RFC3339))
	} else {
		fmt.Println("Активного ключа нет")
	}
	return nil
}

// checkHealth стучится в сокет и проверяет, что демон обслуживает запросы.
//
// Живёт в самом бинаре, потому что в образе нет ни шелла, ни curl: HEALTHCHECK
// в докере умеет запустить только то, что уже лежит внутри.
//
// Про sync_error проба намеренно молчит: недоступный сервер вуза не делает
// сервис мёртвым — локальная копия ровно для того и держится. Иначе ночь без
// связи с вузом заканчивалась бы перезапуском живого демона.
func checkHealth() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := api.NewClient(config.Socket()).Health(ctx)
	if err != nil {
		return err
	}
	if !h.OK {
		return errors.New(h.Error)
	}
	return nil
}

func logLevel() slog.Level {
	if os.Getenv("DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func run(log *slog.Logger, ring *logbuf.Ring, background bool) error {
	cfg, err := config.LoadRasp()
	if err != nil {
		return err
	}

	// Сигналы перехватываем до открытия ресурсов, чтобы Ctrl+C на долгом
	// первичном синке не оставлял за собой ни сокета, ни недописанной базы.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	log.Info("база открыта", "путь", cfg.DBPath)

	client, err := providerclient.New(cfg.ProviderURL, cfg.ProviderToken, db, cfg.ImportLegacyIDs, profile.Current().Timezone)
	if err != nil {
		return err
	}
	loc := profile.Current().Location()

	sync := syncer.New(client, db, syncer.Options{
		StaffInterval:  cfg.StaffInterval,
		FullSyncAt:     cfg.FullSyncAt,
		HotInterval:    cfg.HotInterval,
		GroupsInterval: cfg.GroupsInterval,
		MonthsAhead:    cfg.MonthsAhead,
		StaleAfter:     cfg.StaleAfter,
		MonthTimeout:   cfg.MonthTimeout,
		Location:       loc,
	}, log)

	// Расписание месяца изменилось — ставим подписчикам группы сообщение в
	// очередь. Дальше его заберёт botd: доставка не дело этого демона, а
	// событие рождается только здесь, при сравнении хэшей в SaveMonth.
	//
	// Правки расписания в вузе идут постоянно, и узнать о них заранее для
	// студента ценнее, чем лишний раз посмотреть само расписание, — ради этого
	// локальная копия и держится.
	sync.OnChange(func(ctx context.Context, groupID int64, year, month int) {
		n, err := db.EnqueueChange(ctx, groupID, year, month)
		if err != nil {
			log.Error("не удалось поставить в очередь новость об изменении",
				"группа", groupID, "год", year, "месяц", month, "ошибка", err)
			return
		}
		if n > 0 {
			log.Info("новость об изменении поставлена в очередь",
				"группа", groupID, "год", year, "месяц", month, "адресатов", n)
		}
	})

	ln, err := api.Listen(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer ln.Close()

	apiSrv := api.NewServer(db, sync, loc, cfg.WarnAfter, log)
	apiSrv.Diagnostics(ring, startedAt, cfg.MonthsAhead)

	srv := &http.Server{
		Handler:      apiSrv.Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	errc := make(chan error, 2)
	go func() {
		log.Info("API слушает", "сокет", cfg.SocketPath)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	if background {
		go func() {
			if err := sync.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errc <- err
			}
		}()
	} else {
		log.Warn("фоновые обходы отключены; доступны только запросы API")
	}

	select {
	case <-ctx.Done():
		log.Info("получен сигнал, останавливаюсь")
	case err := <-errc:
		stop()
		shutdown(srv, log)
		return err
	}
	shutdown(srv, log)
	return nil
}

func shutdown(srv *http.Server, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("остановка HTTP-сервера", "ошибка", err)
	}
}
