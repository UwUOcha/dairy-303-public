package botcore

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/time/rate"

	"github.com/UwUOcha/dairy-303-public/internal/api"
)

// Проверка того, что адресаты ещё в диалоге.
//
// Зачем вообще. Блокировка сама по себе никуда не приходит: площадка сообщает
// о ней только в ответ на попытку написать. Пока бот молчит — человек в базе
// числится живым, и вся статистика читателей медленно превращается в
// статистику когда-то заходивших. Проверка закрывает эту дыру, спрашивая
// площадку напрямую.
//
// Почему не считать блокировки одной рассылкой. Потому что рассылку человек
// мог выключить сам, и тогда бот к нему больше не постучится никогда. Такие
// как раз и составляют самый неучтённый угол базы.

// Reacher — площадка, умеющая проверить адресата, не написав ему.
//
// Отдельно от Sender, потому что это не отправка: доставлять сюда нечего, и
// путать «не смог написать» с «специально сходил спросить» не стоит.
type Reacher interface {
	Sender
	// Reachable отвечает, можно ли ещё писать этому адресату. Ошибка значит
	// «спросить не удалось» — это не ответ, и записывать его нельзя.
	Reachable(ctx context.Context, extID string) (bool, error)
}

// Prober обходит базу и отмечает, кто выбросил бота из диалога.
type Prober struct {
	api    *api.Client
	sender Reacher
	log    *slog.Logger
	limit  *rate.Limiter
}

const (
	// probeTick — как часто брать следующую порцию.
	//
	// Час, а не сутки: обход размазан по неделе (см. store.ProbeInterval), и
	// чем мельче шаг, тем ровнее ложится нагрузка на площадку. Заодно порция
	// после перезапуска botd начинается почти сразу, а не завтра.
	probeTick = time.Hour
	// probeBatch — сколько адресатов проверять за раз.
	//
	// Двести в час — это около тридцати трёх тысяч в неделю: с запасом
	// перекрывает базу любого размера, который этот бот увидит, и при этом
	// одна порция занимает у площадки чуть больше минуты.
	probeBatch = 200
	// probeRPS — с какой скоростью спрашивать.
	//
	// Втрое меньше, чем разрешено рассылке: проверка никуда не торопится, а
	// лимит у площадки общий, и отнимать его у живых сообщений ради
	// показателя в панели — плохой размен.
	probeRPS = 3
	// probeGiveUp — после скольких подряд неудачных попыток бросить порцию.
	//
	// Проверки не отвечают ошибкой по одной: если площадка отказывает, она
	// отказывает всем подряд. Продолжать в такую минуту значит впустую жечь
	// лимит и залить лог одинаковыми жалобами.
	probeGiveUp = 10
)

// NewProber собирает обходчик.
func NewProber(client *api.Client, sender Reacher, log *slog.Logger) *Prober {
	return &Prober{
		api: client, sender: sender, log: log,
		limit: rate.NewLimiter(probeRPS, 1),
	}
}

// Run обходит базу порциями, пока жив контекст.
//
// Первая порция — сразу после старта: она же и есть догоняющая, если botd
// стоял. Никакого расписания «в ночь на воскресенье» здесь нет намеренно —
// очередь хранится в самих записях, и любой момент запуска одинаково хорош.
func (p *Prober) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			timer.Reset(probeTick)
			if err := p.sweep(ctx); err != nil && ctx.Err() == nil {
				p.log.Warn("проверка адресатов прервана",
					"платформа", p.sender.Platform(), "ошибка", err)
			}
		}
	}
}

// sweep проверяет одну порцию.
func (p *Prober) sweep(ctx context.Context) error {
	platform := p.sender.Platform()
	targets, err := p.api.UsersToProbe(ctx, platform, probeBatch)
	if err != nil || len(targets) == 0 {
		return err
	}

	var gone, back, failures int
	for _, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := p.limit.Wait(ctx); err != nil {
			return err
		}

		reachable, err := p.reach(ctx, t.ExtID)
		if err != nil {
			// Молчание площадки — не ответ: отметку не ставим, и человек
			// останется первым в очереди на следующую порцию.
			failures++
			if failures >= probeGiveUp {
				return err
			}
			continue
		}
		failures = 0

		if err := p.api.MarkProbed(ctx, platform, t.ExtID, reachable); err != nil {
			return err
		}
		switch {
		case !reachable && !t.Blocked:
			gone++
		case reachable && t.Blocked:
			back++
		}
	}

	// Тишина в спокойный час — тоже результат: писать в лог «проверено 200,
	// изменений нет» двадцать четыре раза в сутки незачем.
	if gone > 0 || back > 0 {
		p.log.Info("проверка адресатов", "платформа", platform, "проверено", len(targets),
			"заблокировали", gone, "вернулись", back)
	}
	return nil
}

// reach спрашивает площадку об одном адресате, уважая её просьбу подождать.
func (p *Prober) reach(ctx context.Context, extID string) (bool, error) {
	reachable, err := p.sender.Reachable(ctx, extID)
	if err == nil {
		return reachable, nil
	}
	wait, ok := p.sender.Retryable(err)
	if !ok {
		return false, err
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(wait):
	}
	return p.sender.Reachable(ctx, extID)
}
