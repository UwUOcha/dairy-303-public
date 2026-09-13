package botcore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Sender — то, что умеет доставить сообщение на платформу.
type Sender interface {
	// Send отправляет сообщение пользователю платформы.
	Send(ctx context.Context, extID, text string, kb *Keyboard) error
	// Platform возвращает метку платформы ('tg', 'vk').
	Platform() string
	// Retryable сообщает, стоит ли повторить отправку и через сколько.
	Retryable(err error) (time.Duration, bool)
	// Undeliverable сообщает, что адресат недостижим навсегда: заблокировал
	// бота или удалил аккаунт.
	Undeliverable(err error) bool
}

// Notifier рассылает то, что бот шлёт по своей инициативе: расписание утром и
// вечером, разовую подсказку про пустые дни и новости о правках.
//
// Узкое место здесь — не база и не CPU, а лимиты платформы: телеграм пускает
// около тридцати сообщений в секунду. Поэтому отправка идёт через ограничитель
// и уважает retry_after, а не пытается выплюнуть залп разом.
type Notifier struct {
	core    *Bot
	api     *api.Client
	sender  Sender
	log     *slog.Logger
	limit   *rate.Limiter
	changes map[string]changeReply
}

// NewNotifier собирает рассыльщика. rps — сколько сообщений в секунду слать.
func NewNotifier(core *Bot, client *api.Client, sender Sender, rps float64, log *slog.Logger) *Notifier {
	if rps <= 0 {
		rps = 25
	}
	return &Notifier{
		core: core, api: client, sender: sender, log: log,
		limit: rate.NewLimiter(rate.Limit(rps), 1),
	}
}

// Run тикает раз в минуту: рассылает расписание тем, у кого настало их время,
// и разгребает очередь исходящих.
func (n *Notifier) Run(ctx context.Context) {
	// Сразу подбираем недавние задания после рестарта, затем выравниваем
	// тики на границу минуты. Уже доставленное отсекается по Done.
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			timer.Reset(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
			if err := n.tick(ctx); err != nil && ctx.Err() == nil {
				n.log.Error("рассылка расписания", "ошибка", err)
			}
			if err := n.drain(ctx); err != nil && ctx.Err() == nil {
				n.log.Error("очередь исходящих", "ошибка", err)
			}
		}
	}
}

func (n *Notifier) tick(ctx context.Context) error {
	now := time.Now().UTC()
	utcMinute := now.Hour()*60 + now.Minute()

	targets, err := n.api.UsersToNotify(ctx, utcMinute)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}

	// Расписание дня зависит только от тройки (группа, подгруппа, какая
	// рассылка), а не от человека. Без этого кэша залп на всю группу
	// превращался в отдельный запрос к raspd на каждого адресата — при пяти
	// тысячах подписчиков на одну минуту это пять тысяч походов за одним и тем
	// же ответом.
	day := newDayCache(n.core)

	sent := 0
	for _, t := range targets {
		if t.Platform != n.sender.Platform() {
			continue
		}
		// «Сегодня» у каждого своё — сверяем в его часовом поясе. Без этой
		// проверки перезапуск botd в ту же минуту разбудил бы человека дважды.
		if t.Done(TodayIn(t.TZOffset)) {
			continue
		}
		if err := n.notifyOne(ctx, day, t); err != nil && ctx.Err() == nil {
			n.log.Warn("не доставлено", "пользователь", t.ExtID, "вид", t.Kind, "ошибка", err)
			continue
		}
		sent++
	}
	if sent > 0 {
		n.log.Info("рассылка расписания", "отправлено", sent, "минута_utc", utcMinute,
			"запросов_расписания", day.misses)
	}
	return nil
}

func (n *Notifier) notifyOne(ctx context.Context, day *dayCache, t store.NotifyTarget) error {
	d, err := day.digest(ctx, t.User, t.Kind)
	if err != nil {
		return err
	}
	today := TodayIn(t.TZOffset)

	// Пустой день, а сообщать о них человек не просил. Промолчать нельзя ровно
	// один раз: первое молчание бота читается как поломка, поэтому оно
	// объясняется вслух — и больше никогда.
	if d.Empty && !t.EmptyDays {
		if !t.EmptyHinted {
			text, kb := n.core.EmptyHint(d.When)
			if err := n.deliver(ctx, t.Platform, t.ExtID, text, kb); err != nil {
				return err
			}
			if err := n.api.MarkEmptyHinted(ctx, t.Platform, t.ExtID); err != nil {
				return err
			}
		}
		// Отметка нужна и здесь: иначе следующий тик снова возьмёт человека в
		// работу и будет брать до конца суток.
		return n.api.MarkNotified(ctx, t.Platform, t.ExtID, t.Kind, today)
	}

	if err := n.deliver(ctx, t.Platform, t.ExtID, d.Text, d.KB); err != nil {
		return err
	}
	return n.api.MarkNotified(ctx, t.Platform, t.ExtID, t.Kind, today)
}

// deliver отправляет одно сообщение с уважением к лимитам платформы.
//
// Недостижимого адресата не ретраим, а выключаем ему уведомления: он
// заблокировал бота, и следующие сообщения тоже не дойдут.
func (n *Notifier) deliver(ctx context.Context, platform, extID, text string, kb *Keyboard) error {
	if err := n.limit.Wait(ctx); err != nil {
		return err
	}

	metrics := n.core.Metrics()
	err := n.sender.Send(ctx, extID, text, kb)
	if wait, ok := n.sender.Retryable(err); ok {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		err = n.sender.Send(ctx, extID, text, kb)
	}
	if err != nil && n.sender.Undeliverable(err) {
		n.log.Info("адресат недостижим, выключаю уведомления", "пользователь", extID)
		metrics.Blocked(platform)
		return n.api.DisableNotify(ctx, platform, extID)
	}
	if err != nil {
		metrics.Failed(platform)
		return err
	}
	metrics.Sent(platform)
	return nil
}

// ── очередь исходящих ───────────────────────────────────────────────────────

// outboxRetry — через сколько повторить неудачную отправку. Растёт с числом
// попыток, но новость о правке расписания живёт недолго, поэтому потолок
// низкий (дальше store выбросит сообщение сам).
func outboxRetry(attempts int) time.Duration {
	d := time.Duration(1<<uint(min(attempts, 4))) * time.Minute
	return min(d, 30*time.Minute)
}

// drain разгребает очередь сообщений, которые бот шлёт не в ответ на действие
// человека: расписание его группы поправили.
//
// Очередь живёт в базе, а не в памяти: событие рождается в raspd, а доставляет
// его botd, и перезапуск любого из двух не должен терять сообщение.
func (n *Notifier) drain(ctx context.Context) error {
	n.changes = map[string]changeReply{}
	items, err := n.api.TakeOutbox(ctx, n.sender.Platform(), 200)
	if err != nil || len(items) == 0 {
		return err
	}

	delivered := 0
	for _, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		text, kb, err := n.render(ctx, it)
		if err != nil {
			n.log.Warn("не удалось собрать сообщение очереди",
				"id", it.ID, "вид", it.Kind, "ошибка", err)
			// Чтение пользователя тоже может временно не сработать. Только
			// повреждённое тело и неизвестный вид сообщения невосстановимы.
			if !errors.Is(err, errInvalidPayload) && !errors.Is(err, errUnknownKind) {
				if ferr := n.api.OutboxFail(ctx, it.ID, outboxRetry(it.Attempts)); ferr != nil {
					return ferr
				}
				continue
			}
			if derr := n.api.OutboxDone(ctx, it.ID, it.Payload); derr != nil {
				return derr
			}
			continue
		}

		if text == "" {
			if err := n.api.OutboxDone(ctx, it.ID, it.Payload); err != nil {
				return err
			}
			continue
		}
		if err := n.deliver(ctx, it.Platform, it.ExtID, text, kb); err != nil {
			n.log.Warn("сообщение очереди не доставлено", "id", it.ID, "ошибка", err)
			if ferr := n.api.OutboxFail(ctx, it.ID, outboxRetry(it.Attempts)); ferr != nil {
				return ferr
			}
			continue
		}
		if err := n.api.OutboxDone(ctx, it.ID, it.Payload); err != nil {
			return err
		}
		delivered++
	}
	if delivered > 0 {
		n.log.Info("очередь исходящих", "доставлено", delivered, "платформа", n.sender.Platform())
	}
	return nil
}

// render собирает текст и кнопки по записи очереди.
func (n *Notifier) render(ctx context.Context, it store.OutboxItem) (string, *Keyboard, error) {
	switch it.Kind {
	case store.OutboxChange:
		var p store.ChangePayload
		if err := json.Unmarshal([]byte(it.Payload), &p); err != nil {
			return "", nil, errors.Join(errInvalidPayload, err)
		}
		u, err := n.api.User(ctx, it.Platform, it.ExtID)
		if err != nil {
			return "", nil, err
		}
		if u.User.GroupID != p.GroupID || !u.User.Notify || !u.User.Changes {
			return "", nil, nil
		}
		if !p.Personal {
			text, kb := n.core.ChangeMessage(u.User, p)
			return text, kb, nil
		}
		key := strconv.FormatInt(u.User.SubgroupID, 10) + ":" + it.Payload
		if cached, ok := n.changes[key]; ok {
			return cached.text, cached.kb, nil
		}
		resp, err := n.api.ChangeSummary(ctx, api.ChangeSummaryRequest{GroupID: p.GroupID, SubgroupID: u.User.SubgroupID, Before: p.Before})
		if err != nil {
			return "", nil, err
		}
		for _, d := range resp.Days {
			if d.Missing {
				return "", nil, errors.New("неполные данные для сводки правок")
			}
		}
		text, kb := PersonalChangeMessage(resp)
		if n.changes != nil {
			n.changes[key] = changeReply{text, kb}
		}
		return text, kb, nil

	case store.OutboxFeedback:
		var p store.FeedbackPayload
		if err := json.Unmarshal([]byte(it.Payload), &p); err != nil {
			return "", nil, errors.Join(errInvalidPayload, err)
		}
		text, kb := n.core.FeedbackMessage(p)
		return text, kb, nil

	case store.OutboxAnswer:
		var p store.AnswerPayload
		if err := json.Unmarshal([]byte(it.Payload), &p); err != nil {
			return "", nil, errors.Join(errInvalidPayload, err)
		}
		text, kb := n.core.AnswerMessage(p)
		return text, kb, nil
	}
	return "", nil, errUnknownKind
}

// errUnknownKind — в очереди лежит вид сообщения, которого эта версия бота не
// знает. Бывает при откате бинаря на версию назад.
var errUnknownKind = errors.New("botcore: неизвестный вид сообщения очереди")

var errInvalidPayload = errors.New("botcore: повреждённое тело сообщения очереди")

// dayCache схлопывает одинаковые сообщения рассылки.
//
// Текст зависит только от тройки (группа, подгруппа, вид рассылки): день
// считается по часовому поясу вуза, один и тот же для всех адресатов. Значит
// вся группа — это один запрос к raspd, а не сотня.
//
// Решение «слать или промолчать» в кэш не попадает: оно зависит от настроек
// человека, а не от дня. Кэшируется только сам день.
type dayCache struct {
	core  *Bot
	items map[dayKey]Digest
	// misses — сколько раз всё-таки пришлось сходить за расписанием.
	misses int
}

type dayKey struct {
	group, subgroup int64
	kind            store.NotifyKind
}

func newDayCache(core *Bot) *dayCache {
	return &dayCache{core: core, items: map[dayKey]Digest{}}
}

func (c *dayCache) digest(ctx context.Context, u store.User, kind store.NotifyKind) (Digest, error) {
	key := dayKey{u.GroupID, u.SubgroupID, kind}
	if d, ok := c.items[key]; ok {
		return d, nil
	}
	c.misses++
	d, err := c.core.Digest(ctx, u, kind)
	// Сетевую неудачу не запоминаем: следующему адресату той же группы стоит
	// попробовать заново.
	if err != nil {
		return d, err
	}
	c.items[key] = d
	return d, nil
}

type changeReply struct {
	text string
	kb   *Keyboard
}
