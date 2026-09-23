package botcore

import (
	"context"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Author — кто стоит за ботом: адресат обратной связи и ссылки на бота.
//
// Автор один, а площадок две, поэтому обращения собираются в одном месте:
// написанное во ВКонтакте приезжает в телеграм, если телеграм задан. Ответ
// уходит обратно на площадку человека — её помнит очередь исходящих.
type Author struct {
	// TG и VK — идентификаторы автора на площадках. Пусто на обеих — связи с
	// автором в боте нет вовсе, и кнопка не рисуется.
	TG string
	VK string
	// LinkTG и LinkVK — куда звать друзей с каждой площадки.
	LinkTG string
	LinkVK string
}

// target — куда складывать обращения. ok == false означает, что автор не
// задан и кнопку показывать нельзя.
func (a Author) target() (platform, extID string, ok bool) {
	switch {
	case a.TG != "":
		return "tg", a.TG, true
	case a.VK != "":
		return "vk", a.VK, true
	}
	return "", "", false
}

// is сообщает, что это сам автор. Только ему видна кнопка «Ответить» под
// обращением и только у него работает ожидание ответа.
//
// Спрашивать приходится по обеим площадкам, а не только по той, куда уходят
// обращения: узнать автора в лицо — это то, на что обопрётся любая админская
// возможность внутри бота, откуда бы он ни писал.
func (a Author) is(platform, extID string) bool {
	if extID == "" {
		return false
	}
	switch platform {
	case "tg":
		return extID == a.TG
	case "vk":
		return extID == a.VK
	}
	return false
}

// link — ссылка на бота для площадки, с которой его собрались пересылать.
func (a Author) link(platform string) string {
	if platform == "vk" {
		return a.LinkVK
	}
	return a.LinkTG
}

// Option — необязательная настройка маршрутизатора.
type Option func(*Bot)

// WithAuthor задаёт автора: адресата обратной связи и ссылки на бота.
func WithAuthor(a Author) Option { return func(b *Bot) { b.author = a } }

// Bot — платформо-независимый маршрутизатор сценариев.
type Bot struct {
	api *api.Client
	log *slog.Logger
	// author — кому уходят обращения и куда ведут ссылки «поделиться».
	author Author
	// tz — часовой пояс по умолчанию для новых пользователей, минуты от UTC.
	tz int
	// userLocks не даёт двум быстрым нажатиям одного человека прочитать один
	// и тот же снимок настроек и затем перетереть изменения друг друга.
	// Фиксированное число полос не растёт вместе с пользовательской базой.
	userLocks [256]sync.Mutex
	// metrics — счётчики для админ-панели. Общие на все площадки: маршрутизатор
	// тоже один, а разделение идёт по метке платформы внутри.
	metrics *Metrics
}

// New собирает маршрутизатор.
func New(client *api.Client, tz int, log *slog.Logger, opts ...Option) *Bot {
	b := &Bot{api: client, tz: tz, log: log, metrics: NewMetrics()}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Metrics отдаёт счётчики бота. Нужны botd, чтобы отчитаться перед панелью.
func (b *Bot) Metrics() *Metrics { return b.metrics }

// searchLimit — сколько групп показывать в результатах поиска. Больше восьми
// кнопок в столбце уже не выбор, а список.
const searchLimit = 8

// Handle обрабатывает событие и возвращает ответы для отправки.
//
// Состояние диалога в памяти процесса не хранится: все многошаговые сценарии —
// выбор группы, обзор дерева, вкладка уведомлений — целиком кодируются в
// callback-данных кнопок. Каждое нажатие самодостаточно, поэтому перезапуск
// бота не может уронить человека на середине настройки.
//
// Единственное исключение — ожидание времени рассылки: его кнопкой не
// выбрать, не превратив клавиатуру в циферблат. Оно живёт в записи
// пользователя (store.User.Await), а не в процессе, и потому переживает
// перезапуск ровно так же.
func (b *Bot) Handle(ctx context.Context, u Update) ([]Reply, error) {
	b.metrics.Update(u.Platform)
	if replies, handled, err := b.webAuth(ctx, u); handled {
		return replies, err
	}

	lock := b.userLock(u.Platform, u.UserID)
	lock.Lock()
	defer lock.Unlock()

	user, err := b.loadUser(ctx, u.Platform, u.UserID)
	if err != nil {
		b.metrics.Error(u.Platform)
		return b.unavailable(err), nil
	}

	var replies []Reply
	if u.IsCallback() {
		replies, err = b.handleCallback(ctx, u, user)
	} else {
		replies, err = b.handleText(ctx, u, user)
	}
	if err != nil {
		b.metrics.Error(u.Platform)
	}
	return b.markMenu(user, replies), err
}

// userLock выбирает стабильную полосу блокировки по платформе и id.
func (b *Bot) userLock(platform, userID string) *sync.Mutex {
	// FNV-1a без отдельного объекта хэша на каждый апдейт.
	var hash uint32 = 2166136261
	for _, s := range []string{platform, "\x00", userID} {
		for i := 0; i < len(s); i++ {
			hash ^= uint32(s[i])
			hash *= 16777619
		}
	}
	return &b.userLocks[hash%uint32(len(b.userLocks))]
}

// markMenu сравнивает отправленную версию с предлагаемой, не отмечая доставку заранее.
func (b *Bot) markMenu(user api.UserResponse, replies []Reply) []Reply {
	for i := range replies {
		replies[i].MenuSeen = replies[i].Menu != nil && user.User.MenuVersion == replies[i].Menu.Version()
	}
	return replies
}

// MenuDelivered вызывается адаптером только после успешной отправки клавиатуры.
func (b *Bot) MenuDelivered(ctx context.Context, platform, userID string, menu *Menu) error {
	if menu == nil {
		return nil
	}
	return b.api.MarkMenuSent(ctx, platform, userID, menu.Version())
}

// loadUser читает пользователя и проставляет, кто он такой.
//
// Для незнакомого человека raspd отдаёт пустую запись — без platform и ext_id,
// потому что в базе его ещё нет. Если не заполнить их здесь, первое же
// сохранение настроек уйдёт без идентификатора и будет отвергнуто. Именно на
// этом ломался весь онбординг: человек доходил до выбора группы — поиском или
// обзором по институтам — и упирался в «сервис недоступен».
func (b *Bot) loadUser(ctx context.Context, platform, extID string) (api.UserResponse, error) {
	user, err := b.api.User(ctx, platform, extID)
	if err != nil {
		return user, err
	}
	if !user.Known {
		// Нулевая структура означает «все уведомления выключены», а новичок
		// должен получать утреннее расписание, ничего не настраивая. В базе те
		// же значения стоят как DEFAULT колонок, но записи там ещё нет.
		user.User = user.User.WithDefaults(b.tz)
	}
	user.User.Platform = platform
	user.User.ExtID = extID
	if user.User.TZOffset == 0 {
		user.User.TZOffset = b.tz
	}
	return user, nil
}

// unavailable — единая реакция на недоступность raspd.
func (b *Bot) unavailable(err error) []Reply {
	b.log.Error("raspd недоступен", "ошибка", err)
	return []Reply{{Text: "Сервис расписания сейчас недоступен. Попробуй через минуту."}}
}

// ── текстовые сообщения ─────────────────────────────────────────────────────

func (b *Bot) handleText(ctx context.Context, u Update, user api.UserResponse) ([]Reply, error) {
	text := strings.TrimSpace(u.Text)

	// Бот ждёт время рассылки — значит, это оно и есть. Команды и кнопки меню
	// проверяются раньше: человек вправе бросить настройку на полпути, нажав
	// «Сегодня», и застревать в диалоге, из которого не выйти, он не должен.
	if user.User.Await != "" {
		if !isCommand(text) {
			return b.awaited(ctx, user, text)
		}
		b.forgetAwait(ctx, &user)
	}

	switch {
	case text == "/start" || strings.HasPrefix(text, "/start "):
		return b.start(ctx, u, user, strings.TrimSpace(strings.TrimPrefix(text, "/start")))
	case text == "/help":
		return []Reply{{Text: helpText, Keyboard: HelpKeyboard(), Menu: menuFor(user)}}, nil
	case text == "/today" || text == MenuToday:
		return b.showDay(ctx, user, "", 0)
	case text == "/tomorrow" || text == MenuTomorrow || text == "⏭ Завтра":
		return b.showTomorrow(ctx, user)
	case text == "/week" || text == MenuWeek:
		return b.showWeek(ctx, user, "", 0)
	case text == "/next":
		return b.showNextLesson(ctx, user, 0)
	case text == "/now":
		return b.showNow(ctx, user, 0)
	case text == "/menu" || text == MenuMore:
		return b.more(user, false), nil
	case text == "/settings" || text == MenuSettings:
		// Кнопка «Настройки» осталась в панелях, розданных прошлой версией.
		// Ведёт туда же, куда вела: человек жал её ради настроек, а не ради
		// нового экрана.
		return b.settings(user, false)
	}

	// Всё остальное трактуем как название группы. Это же и есть путь
	// первичной настройки, так что новому пользователю не нужно знать команд.
	return b.searchGroup(ctx, text, user)
}

// isCommand сообщает, что текст — это команда или кнопка нижнего меню.
// Список обязан совпадать с разбором в handleText: расхождение означает, что
// человек, которого бот ждёт с временем, не сможет выйти из ожидания кнопкой.
func isCommand(text string) bool {
	// Ссылка с сайта приходит как «/start g802»: это та же команда, и человек,
	// которого бот ждёт с временем рассылки, должен выйти из ожидания и по ней.
	if strings.HasPrefix(text, "/start ") {
		return true
	}
	switch text {
	case "/start", "/help", "/today", "/tomorrow", "/week", "/now", "/settings", "/menu", "/next",
		MenuToday, MenuTomorrow, "⏭ Завтра", MenuWeek, MenuMore, MenuSettings:
		return true
	}
	return false
}

// awaited разбирает текст, которого бот ждал.
//
// Видов ожидания три, и все они живут в записи пользователя: время рассылки,
// обращение к автору и ответ автора на обращение. Разбор по виду, а не по
// содержимому: одна и та же строка «19:40» — это и время, и вполне возможное
// начало жалобы.
func (b *Bot) awaited(ctx context.Context, user api.UserResponse, text string) ([]Reply, error) {
	await := user.User.Await
	switch {
	case await == store.AwaitFeedback:
		return b.saveFeedback(ctx, user, text)
	case strings.HasPrefix(await, store.AwaitAnswer+":"):
		return b.sendAnswer(ctx, user, strings.TrimPrefix(await, store.AwaitAnswer+":"), text)
	}
	return b.setTime(ctx, user, await, text)
}

func (b *Bot) start(ctx context.Context, u Update, user api.UserResponse, payload string) ([]Reply, error) {
	greeting := "Привет!"
	if u.FirstName != "" {
		greeting = "Привет, " + esc(u.FirstName) + "!"
	}

	// Переход с сайта: t.me/<бот>?start=g802. Группа там уже выбрана, и искать
	// её второй раз руками незачем. Чужую настройку это не перебивает —
	// openGroup настроенному человеку просто покажет эту группу с кнопкой
	// «сделать своей», а ненастроенному предложит подгруппу.
	if id := startGroup(payload); id > 0 {
		replies, err := b.openGroup(ctx, user, id, false)
		if err == nil && len(replies) > 0 {
			replies[0].Text = greeting + "\n\n" + replies[0].Text
			return replies, nil
		}
		if err != nil {
			return b.unavailable(err), nil
		}
	}

	if !user.User.Configured() {
		return []Reply{{
			Text:     greeting + "\n\n" + profile.Current().Render(profile.Current().Text("onboarding", onboardingText), true),
			Keyboard: &Keyboard{Rows: [][]Button{{{Label: "📚 Выбрать из списка институтов", Data: cbBrowse}}}},
		}}, nil
	}

	// Настроенному человеку не нужен рассказ о боте — ему нужно расписание.
	replies, err := b.showDay(ctx, user, "", 0)
	if err != nil || len(replies) == 0 {
		return replies, err
	}
	replies[0].Text = greeting + "\n\n" + replies[0].Text
	replies[0].Menu = MainMenu()
	return replies, nil
}

// startGroup разбирает полезную нагрузку ссылки «?start=». Формат один —
// «g<номер группы>»; всё остальное считаем мусором и молча игнорируем, чтобы
// подделанная ссылка не могла увести человека в чужие настройки.
func startGroup(payload string) int64 {
	if len(payload) < 2 || payload[0] != 'g' {
		return 0
	}
	id, err := strconv.ParseInt(payload[1:], 10, 64)
	if err != nil || id <= 0 || id > 1_000_000_000 {
		return 0
	}
	return id
}

func (b *Bot) searchGroup(ctx context.Context, query string, user api.UserResponse) ([]Reply, error) {
	if query == "" {
		return []Reply{{Text: profile.Current().Render(profile.Current().Text("onboarding", onboardingText), true)}}, nil
	}
	// Спрашиваем на одну группу больше, чем покажем: лишняя — это признак
	// того, что список обрезан, и об этом надо сказать вслух. Молча
	// показанные восемь из двадцати читаются как «моей группы бот не знает».
	found, err := b.api.SearchGroups(ctx, query, searchLimit+1)
	if err != nil {
		return b.unavailable(err), nil
	}
	// Групп не нашлось, зато нашлась подгруппа: человек написал «ГР-22/2» —
	// ровно то, о чём его спросил экран выбора подгруппы. Отказывать тому, кто
	// всё написал верно, — худший из возможных ответов.
	if len(found.Groups) == 0 && found.Subgroup != nil {
		return b.openSubgroup(ctx, user, *found.Subgroup)
	}
	groups := found.Groups
	truncated := len(groups) > searchLimit
	if truncated {
		groups = groups[:searchLimit]
	}

	switch len(groups) {
	case 0:
		return []Reply{{
			Text:     fmt.Sprintf("Не нашёл группу «%s».\n\n%s", esc(shorten(query)), notFoundHint),
			Keyboard: &Keyboard{Rows: [][]Button{{{Label: "📚 Выбрать из списка институтов", Data: cbBrowse}}}},
			Menu:     menuFor(user),
		}}, nil
	case 1:
		// Единственное совпадение открываем без лишнего нажатия. Для новичка
		// это и есть настройка, для остальных — просмотр.
		return b.openGroup(ctx, user, groups[0].ID, false)
	}

	text := "Нашёл несколько групп — выбери свою:"
	if truncated {
		text = fmt.Sprintf("Групп с таким названием много — вот первые %d:", searchLimit)
	}
	return []Reply{{
		Text:     text,
		Keyboard: GroupsKeyboard(groups, true),
		Menu:     menuFor(user),
	}}, nil
}

// openSubgroup доводит до конца запрос, оказавшийся названием подгруппы.
//
// Три случая, и во всех человек уже сказал достаточно, чтобы не спрашивать
// снова. Чужую подгруппу себе не записываем: в гостях фильтр по подгруппе не
// работает вовсе, и запись чужого id только испортила бы собственную
// настройку.
func (b *Bot) openSubgroup(ctx context.Context, user api.UserResponse, sub store.Subgroup) ([]Reply, error) {
	switch {
	case user.User.GroupID == sub.GroupID:
		return b.pickSubgroup(ctx, user, sub.ID)

	case !user.User.Configured():
		// Ни группы, ни подгруппы — а названо и то и другое сразу.
		u := user.User
		u.GroupID = sub.GroupID
		u.SubgroupID = 0
		if err := b.saveUser(ctx, &user, u); err != nil {
			return b.unavailable(err), nil
		}
		return b.pickSubgroup(ctx, user, sub.ID)

	default:
		return b.openGroup(ctx, user, sub.GroupID, false)
	}
}

// shorten укорачивает эхо пользовательского ввода. В поиск прилетает что
// угодно, включая вставленный абзац, а сообщение с ним внутри выглядит как
// сбой бота.
func shorten(s string) string {
	const limit = 40
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return strings.TrimSpace(string(r[:limit])) + "…"
}

// ── нажатия кнопок ──────────────────────────────────────────────────────────

func (b *Bot) handleCallback(ctx context.Context, u Update, user api.UserResponse) ([]Reply, error) {
	prefix, args := parseCB(u.Callback)

	// Нажал кнопку вместо того, чтобы написать время: ожидание снимается,
	// иначе следующее сообщение человека уедет в разбор часов. Кнопки самого
	// диалога времени исключены — они управляют ожиданием сами.
	if user.User.Await != "" && prefix != cbNotifyAsk && prefix != cbNotifySet &&
		prefix != cbFeedback && prefix != cbAnswer {
		b.forgetAwait(ctx, &user)
	}

	switch prefix {
	case cbDay:
		return b.showDay(ctx, user, arg(args, 0), guestArg(args, 1))
	case cbWeek:
		return b.showWeek(ctx, user, arg(args, 0), guestArg(args, 1))
	case cbNextLesson:
		return b.showNextLesson(ctx, user, guestArg(args, 0))
	case cbNow:
		return b.showNow(ctx, user, guestArg(args, 0))
	case cbSettings:
		return b.settings(user, true)
	case cbClose:
		return []Reply{{Text: "Готово. Меню внизу всегда под рукой.", Edit: true, Menu: menuFor(user)}}, nil

	case cbMore:
		return b.more(user, true), nil
	case cbHelp:
		return []Reply{{Text: helpText, Edit: true, Keyboard: HelpKeyboard()}}, nil
	case cbAbout:
		return b.about(ctx, user), nil
	case cbShare:
		return b.share(user), nil
	case cbFeedback:
		return b.askFeedback(ctx, user)
	case cbAnswer:
		return b.askAnswer(ctx, user, arg(args, 0))

	case cbGroupEdit:
		return []Reply{{
			Text:     changeGroupText,
			Edit:     true,
			Keyboard: &Keyboard{Rows: [][]Button{{{Label: "📚 Выбрать из списка институтов", Data: cbBrowse}}}},
		}}, nil

	case cbBrowse:
		return b.browse(ctx, args)

	case cbGroupPick:
		id, err := strconv.ParseInt(arg(args, 0), 10, 64)
		if err != nil {
			return nil, err
		}
		return b.openGroup(ctx, user, id, true)

	case cbAdopt:
		id, err := strconv.ParseInt(arg(args, 0), 10, 64)
		if err != nil {
			return nil, err
		}
		return b.pickGroup(ctx, user, id, true)

	case cbTwinMen:
		return b.twinPicker(ctx, user)

	case cbTwinPick:
		id, err := strconv.ParseInt(arg(args, 0), 10, 64)
		if err != nil {
			return nil, err
		}
		return b.pickTwin(ctx, user, id)

	case cbSubEdit:
		return b.subgroupPicker(ctx, user, true, false)

	case cbSubPick:
		id, err := strconv.ParseInt(arg(args, 0), 10, 64)
		if err != nil {
			return nil, err
		}
		return b.pickSubgroup(ctx, user, id)

	case cbNotifyMen:
		return b.notifyMenu(user, true), nil

	case cbNotifyAll:
		u := user.User
		u.Notify = !u.Notify
		// Утреннее сообщение поднимается вместе с главным тумблером: человек,
		// включивший уведомления, ждёт расписание, а не пустую вкладку.
		if u.Notify {
			u.Morning = true
		}
		return b.saveNotify(ctx, user, u)

	case cbNotifyMor:
		u := user.User
		u.Morning = !u.Morning
		return b.saveNotify(ctx, user, u)

	case cbNotifyEve:
		u := user.User
		u.Evening = !u.Evening
		return b.saveNotify(ctx, user, u)

	case cbNotifyEmp:
		u := user.User
		u.EmptyDays = !u.EmptyDays
		// Кнопка приезжает и из разовой подсказки про пустой день, где
		// уведомления могли быть выключены целиком.
		if u.EmptyDays {
			u.Notify = true
		}
		return b.saveNotify(ctx, user, u)

	case cbNotifyChg:
		u := user.User
		u.Changes = !u.Changes
		return b.saveNotify(ctx, user, u)

	case cbChangesSet:
		return b.setChanges(ctx, user, arg(args, 0) == changesOn)

	case cbMorningSet:
		return b.setMorning(ctx, user, arg(args, 0) == morningOn)

	case cbNotifyAsk:
		return b.askTime(ctx, user, arg(args, 0))

	case cbNotifySet:
		return b.setTimeButton(ctx, user, args)

	case cbChanges:
		return b.changeList(ctx, user, guestArg(args, 0))

	case cbChangeDay:
		return b.changeDay(ctx, user, guestArg(args, 0), arg(args, 1))
	}

	b.log.Warn("неизвестная кнопка", "данные", u.Callback)
	return []Reply{{
		Text:     "Эта кнопка из старой версии бота. Открой меню заново.",
		Edit:     true,
		Keyboard: SettingsKeyboard(user),
	}}, nil
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// guestArg достаёт id просматриваемой чужой группы. Мусор в этой позиции —
// не повод отвечать ошибкой: покажем своё расписание, это безопасный исход.
func guestArg(args []string, i int) int64 {
	id, err := strconv.ParseInt(arg(args, i), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

// ── показ расписания ────────────────────────────────────────────────────────

// requireGroup возвращает подсказку, если человек ещё не выбрал группу.
func requireGroup(user api.UserResponse) []Reply {
	if user.User.Configured() {
		return nil
	}
	return []Reply{{
		Text:     profile.Current().Render(profile.Current().Text("onboarding", onboardingText), true),
		Keyboard: &Keyboard{Rows: [][]Button{{{Label: "📚 Выбрать из списка институтов", Data: cbBrowse}}}},
	}}
}

// target решает, чьё расписание показываем.
//
// guest — id группы из callback-данных; 0 означает «своё». Совпадение с
// собственной группой тоже считается своим: чужой шапки и кнопки «сделать
// моей» там быть не должно.
func target(user api.UserResponse, guest int64) (groupID, subgroupID, asGuest int64) {
	if guest == 0 || guest == user.User.GroupID {
		return user.User.GroupID, user.User.SubgroupID, 0
	}
	// В гостях подгруппа не фильтруется: своей у чужой группы у человека нет,
	// а показать половину пар молча — худшее, что можно сделать.
	return guest, 0, guest
}

func (b *Bot) showDay(ctx context.Context, user api.UserResponse, date string, guest int64) ([]Reply, error) {
	groupID, subgroupID, asGuest := target(user, guest)
	if asGuest == 0 {
		if r := requireGroup(user); r != nil {
			return r, nil
		}
	}
	resp, err := b.api.Day(ctx, groupID, subgroupID, date)
	if err != nil {
		return b.unavailable(err), nil
	}
	return []Reply{{
		Text:     guestPrefix(asGuest) + FormatDay(resp),
		Keyboard: DayKeyboard(resp, asGuest),
		Edit:     true,
		Menu:     menuFor(user),
	}}, nil
}

// guestPrefix подписывает чужое расписание.
//
// Подпись повторяется на каждом экране просмотра намеренно: человек листает
// стрелками, забывает, куда зашёл, и «почему у меня не те пары» — самый
// дорогой вопрос, который бот может породить.
func guestPrefix(guest int64) string {
	if guest == 0 {
		return ""
	}
	return guestNote + "\n\n"
}

// showTomorrow opens the first day with lessons starting tomorrow.
func (b *Bot) showTomorrow(ctx context.Context, user api.UserResponse) ([]Reply, error) {
	if r := requireGroup(user); r != nil {
		return r, nil
	}
	r, err := b.api.NextStudyDay(ctx, user.User.GroupID, user.User.SubgroupID)
	if err != nil {
		return b.unavailable(err), nil
	}
	return []Reply{formatNextStudyDay(r, user)}, nil
}

func (b *Bot) showWeek(ctx context.Context, user api.UserResponse, monday string, guest int64) ([]Reply, error) {
	groupID, subgroupID, asGuest := target(user, guest)
	if asGuest == 0 {
		if r := requireGroup(user); r != nil {
			return r, nil
		}
	}
	resp, err := b.api.Week(ctx, groupID, subgroupID, monday)
	if err != nil {
		return b.unavailable(err), nil
	}
	return []Reply{{
		Text:     guestPrefix(asGuest) + FormatWeek(resp),
		Keyboard: WeekKeyboard(resp, asGuest),
		Edit:     true,
		Menu:     menuFor(user),
	}}, nil
}

func (b *Bot) showNow(ctx context.Context, user api.UserResponse, guest int64) ([]Reply, error) {
	groupID, subgroupID, asGuest := target(user, guest)
	if asGuest == 0 {
		if r := requireGroup(user); r != nil {
			return r, nil
		}
	}
	tz := user.User.TZOffset
	if tz == 0 {
		tz = b.tz
	}
	resp, err := b.api.Now(ctx, groupID, subgroupID, tz)
	if err != nil {
		return b.unavailable(err), nil
	}
	refresh := Button{Label: "🔄 Обновить", Data: cbNow}
	if asGuest != 0 {
		refresh.Data = cb(cbNow, strconv.FormatInt(asGuest, 10))
	}
	k := &Keyboard{Rows: [][]Button{{
		refresh,
		{Label: MenuToday, Data: cbView(cbDay, resp.Today, asGuest)},
	}}}
	if asGuest != 0 {
		k.Rows = append(k.Rows, guestRow(asGuest))
	}
	return []Reply{{
		Text:     guestPrefix(asGuest) + FormatNow(resp),
		Keyboard: k,
		Edit:     true,
		Menu:     menuFor(user),
	}}, nil
}

// ── выбор группы и подгруппы ────────────────────────────────────────────────

// browse ведёт по дереву: институты → курсы → группы.
//
// Каждый шаг проверяет, что список не пустой. Пустым он бывает по-настоящему:
// сразу после первого запуска каталог групп ещё качается, и экран без единой
// кнопки выглядел бы как зависший бот.
//
// Длинные списки листаются: номер страницы едет последним аргументом кнопки и
// отделяется здесь, до разбора самого пути по дереву.
func (b *Bot) browse(ctx context.Context, args []string) ([]Reply, error) {
	args, page := splitPage(args)

	switch len(args) {
	case 0:
		deps, err := b.api.Departments(ctx)
		if err != nil {
			return b.unavailable(err), nil
		}
		if len(deps) == 0 {
			return b.catalogueEmpty(), nil
		}
		total := pages(len(deps), depsPerPage)
		page = clampPage(page, total)
		return []Reply{{
			Text:     pageTitle("Выбери институт", page, total),
			Keyboard: DepartmentsKeyboard(deps, page),
			Edit:     true,
		}}, nil

	case 1:
		dep, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return nil, err
		}
		courses, err := b.api.Courses(ctx, dep)
		if err != nil {
			return b.unavailable(err), nil
		}
		if len(courses) == 0 {
			return b.catalogueEmpty(), nil
		}
		return []Reply{{Text: "Выбери курс:", Keyboard: CoursesKeyboard(dep, courses), Edit: true}}, nil

	default:
		dep, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return nil, err
		}
		course, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
		groups, err := b.api.GroupsOf(ctx, dep, course)
		if err != nil {
			return b.unavailable(err), nil
		}
		if len(groups) == 0 {
			return []Reply{{
				Text:     "На этом курсе групп не нашлось.",
				Edit:     true,
				Keyboard: CoursesKeyboard(dep, nil),
			}}, nil
		}
		total := pages(len(groups), groupsPerPage)
		page = clampPage(page, total)
		return []Reply{{
			Text:     pageTitle("Выбери группу", page, total),
			Keyboard: BrowseGroupsKeyboard(dep, course, groups, page),
			Edit:     true,
		}}, nil
	}
}

// catalogueEmpty — каталог групп ещё не загрузился.
func (b *Bot) catalogueEmpty() []Reply {
	b.log.Warn("каталог групп пуст")
	return []Reply{{
		Text:     catalogueEmptyText,
		Edit:     true,
		Keyboard: &Keyboard{Rows: [][]Button{{{Label: "🔄 Попробовать снова", Data: cbBrowse}}}},
	}}
}

// openGroup показывает выбранную группу, не трогая привязку.
//
// Это главная поправка сценария. Раньше любой выбор группы — хоть кнопкой из
// поиска, хоть из обзора институтов — молча переписывал привязку человека,
// сбрасывал подгруппу и менял адресата утренней рассылки. При этом /help
// прямо приглашал «просто напиши название другой группы, чтобы посмотреть её
// расписание»: обещание и поведение расходились, а цена расхождения —
// незаметно потерянная собственная настройка.
//
// Теперь чужая группа открывается в гостевом режиме, а забрать её себе можно
// отдельной кнопкой. Исключение — человек, у которого группы ещё нет: ему
// выбор и есть настройка, и лишний шаг в онбординге не нужен.
func (b *Bot) openGroup(ctx context.Context, user api.UserResponse, groupID int64, edit bool) ([]Reply, error) {
	if !user.User.Configured() {
		return b.pickGroup(ctx, user, groupID, edit)
	}
	replies, err := b.showDay(ctx, user, "", groupID)
	if err != nil || len(replies) == 0 {
		return replies, err
	}
	replies[0].Edit = edit
	return replies, nil
}

// pickGroup сохраняет выбранную группу и сразу ведёт к выбору подгруппы.
func (b *Bot) pickGroup(ctx context.Context, user api.UserResponse, groupID int64, edit bool) ([]Reply, error) {
	u := user.User
	if u.GroupID != groupID {
		// Подгруппа принадлежит конкретной группе, поэтому при смене группы
		// её надо сбросить, иначе фильтр будет прятать пары по чужому id.
		u.SubgroupID = 0
	}
	u.GroupID = groupID
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}

	replies, err := b.subgroupPicker(ctx, user, edit, true)
	if err != nil {
		return nil, err
	}
	if replies != nil {
		return replies, nil
	}

	// Подгрупп нет — настройка окончена, показываем расписание.
	day, err := b.showDay(ctx, user, "", 0)
	if err != nil {
		return nil, err
	}
	if len(day) > 0 {
		name := ""
		if user.Group != nil {
			name = user.Group.Name
		}
		day[0].Text = fmt.Sprintf("✅ Группа <b>%s</b> выбрана.\n\n", esc(name)) + day[0].Text
		day[0].Edit = edit
	}
	return day, nil
}

// subgroupPicker показывает выбор подгруппы или nil, если подгрупп нет.
//
// justPicked — человек пришёл сюда прямо с выбора группы, а не из настроек.
func (b *Bot) subgroupPicker(ctx context.Context, user api.UserResponse, edit, justPicked bool) ([]Reply, error) {
	if !user.User.Configured() {
		return requireGroup(user), nil
	}
	subs, err := b.api.Subgroups(ctx, user.User.GroupID)
	if err != nil {
		return b.unavailable(err), nil
	}
	if len(subs) == 0 {
		return nil, nil
	}
	name := ""
	if user.Group != nil {
		name = user.Group.Name
	}
	return []Reply{{
		Text:     subgroupPrompt(name, subs, justPicked),
		Keyboard: SubgroupsKeyboard(subs, user.User.SubgroupID),
		Edit:     edit,
	}}, nil
}

func (b *Bot) pickSubgroup(ctx context.Context, user api.UserResponse, subgroupID int64) ([]Reply, error) {
	if r := requireGroup(user); r != nil {
		return r, nil
	}
	// Кнопка могла приехать из старого сообщения — например, человек сменил
	// группу и вернулся к прежнему экрану. Записать чужую подгруппу нельзя:
	// её id не совпадёт ни с одним занятием, и фильтр молча спрячет всё
	// расписание, а человек не поймёт, за что.
	if subgroupID != 0 {
		subs, err := b.api.Subgroups(ctx, user.User.GroupID)
		if err != nil {
			return b.unavailable(err), nil
		}
		if !hasSubgroup(subs, subgroupID) {
			return []Reply{{
				Text:     staleSubgroupText,
				Edit:     true,
				Keyboard: SubgroupsKeyboard(subs, user.User.SubgroupID),
			}}, nil
		}
	}
	u := user.User
	u.SubgroupID = subgroupID
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}

	day, err := b.showDay(ctx, user, "", 0)
	if err != nil {
		return nil, err
	}
	if len(day) > 0 {
		what := "Показываю пары всей группы."
		if user.Subgroup != nil {
			what = fmt.Sprintf("Подгруппа <b>%s</b> выбрана — чужие пары скрыты.", esc(user.Subgroup.Name))
		}
		day[0].Text = "✅ " + what + "\n\n" + day[0].Text
	}
	return day, nil
}

// ── копии группы ────────────────────────────────────────────────────────────
//
// Какая из одноимённых записей каталога настоящая, raspd решает сам (см.
// store/shadow.go), и ошибиться он может только в одном месте — на стыке
// учебных годов, когда расписание нового года ещё не завели ни у одной. Цена
// ошибки высокая: человек привязывается к записи, в которой сентябрь пуст
// навсегда, и никакая последующая автоматика его привязку уже не тронет.
// Поэтому выбор всегда можно забрать себе.

func (b *Bot) twinPicker(ctx context.Context, user api.UserResponse) ([]Reply, error) {
	if r := requireGroup(user); r != nil {
		return r, nil
	}
	twins, err := b.api.GroupTwins(ctx, user.User.GroupID)
	if err != nil {
		return b.unavailable(err), nil
	}
	if len(twins) < 2 {
		return []Reply{{Text: twinsSingleText, Edit: true, Keyboard: SettingsKeyboard(user)}}, nil
	}
	return []Reply{{
		Text:     twinsText,
		Edit:     true,
		Keyboard: TwinsKeyboard(twins, user.User.GroupID),
	}}, nil
}

// pickTwin переносит привязку на другую копию той же группы.
func (b *Bot) pickTwin(ctx context.Context, user api.UserResponse, groupID int64) ([]Reply, error) {
	if r := requireGroup(user); r != nil {
		return r, nil
	}
	twins, err := b.api.GroupTwins(ctx, user.User.GroupID)
	if err != nil {
		return b.unavailable(err), nil
	}
	// Кнопка могла приехать из старого сообщения, а каталог с тех пор
	// обновиться. Переставлять привязку на что попало нельзя: сюда приходит
	// id из callback-данных, и проверка держит его в границах одноимённых.
	var target *store.Twin
	for i := range twins {
		if twins[i].ID == groupID {
			target = &twins[i]
			break
		}
	}
	if target == nil {
		return []Reply{{
			Text:     "Этой копии больше нет в каталоге. Открой список заново.",
			Edit:     true,
			Keyboard: SettingsKeyboard(user),
		}}, nil
	}

	u := user.User
	u.GroupID = groupID
	// У копии свои подгруппы с другими id, но с теми же названиями: «ГР-22/1»
	// остаётся «ГР-22/1». Переносим по имени, иначе человеку пришлось бы
	// выбирать подгруппу заново, а до тех пор фильтр прятал бы все пары.
	u.SubgroupID = 0
	movedSubgroup := ""
	if user.Subgroup != nil {
		subs, err := b.api.Subgroups(ctx, groupID)
		if err != nil {
			return b.unavailable(err), nil
		}
		for _, s := range subs {
			if s.Name == user.Subgroup.Name {
				u.SubgroupID = s.ID
				movedSubgroup = s.Name
				break
			}
		}
	}
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}

	day, err := b.showDay(ctx, user, "", 0)
	if err != nil {
		return nil, err
	}
	if len(day) > 0 {
		head := fmt.Sprintf("✅ Переключил на другую копию <b>%s</b> (%s).",
			esc(target.Name), twinLabel(target.Group))
		switch {
		case movedSubgroup != "":
			head += fmt.Sprintf("\nПодгруппа <b>%s</b> перенесена.", esc(movedSubgroup))
		case user.Subgroup != nil:
			head += "\nПодгруппу пришлось сбросить — у этой копии её нет."
		}
		day[0].Text = head + "\n\n" + day[0].Text
		day[0].Edit = true
	}
	return day, nil
}

// ── уведомления ─────────────────────────────────────────────────────────────

// Границы, в которых можно назначить рассылку.
//
// Они не про технику, а про смысл: утреннее сообщение должно застать человека
// до выхода из дома, вечернее — после того, как учебный день кончился.
// Разрешить «утро в 19:00» значит завести вторую вечернюю рассылку под чужим
// названием и запутать того, кто потом будет её искать.
const (
	morningFrom, morningTo = 0, 8*60 + 30       // 00:00 … 08:30
	eveningFrom, eveningTo = 9 * 60, 23*60 + 30 // 09:00 … 23:30
)

// notifyMenu рисует вкладку уведомлений по уже загруженному пользователю.
func (b *Bot) notifyMenu(user api.UserResponse, edit bool) []Reply {
	text := notifyText
	if !user.User.Notify {
		text = notifyOffText
	}
	return []Reply{{Text: text, Edit: edit, Keyboard: NotifyKeyboard(user.User)}}
}

// saveNotify сохраняет переключённую настройку и перерисовывает вкладку.
//
// Что именно изменилось, видно в самой кнопке, поэтому отдельного
// подтверждения нет: экран после нажатия — тот же, с переставленной подписью.
func (b *Bot) saveNotify(ctx context.Context, user api.UserResponse, u store.User) ([]Reply, error) {
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}
	return b.notifyMenu(user, true), nil
}

// forgetAwait снимает ожидание ввода.
//
// Неудача здесь не повод отказывать в ответе: худшее последствие — следующее
// сообщение человека уедет в разбор времени и получит подсказку с кнопкой
// «назад».
func (b *Bot) forgetAwait(ctx context.Context, user *api.UserResponse) {
	u := user.User
	u.Await = store.AwaitNothing
	if err := b.saveUser(ctx, user, u); err != nil {
		b.log.Warn("не удалось снять ожидание ввода", "ошибка", err)
	}
}

// askTime просит написать время и запоминает, какой именно рассылки ждать.
func (b *Bot) askTime(ctx context.Context, user api.UserResponse, kind string) ([]Reply, error) {
	u := user.User
	text, current := timePromptMorning, u.MorningAt
	u.Await = store.AwaitMorningTime
	if kind == askEvening {
		text, current = timePromptEvening, u.EveningAt
		u.Await = store.AwaitEveningTime
	}
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}
	return []Reply{{Text: strings.ReplaceAll(text, "{{timezone}}", esc(profile.Current().Timezone)), Edit: true, Keyboard: TimeKeyboard(kind, current)}}, nil
}

// setTimeButton обрабатывает быстрый выбор времени кнопкой.
//
// Формат «n:m:420». Кнопки прошлой версии выглядели иначе — «n:420» и
// «n:off», — и они живут в переписке сколько угодно; разбор их понимает, иначе
// нажатие старой кнопки молча роняло бы человека в «эта кнопка из старой
// версии».
func (b *Bot) setTimeButton(ctx context.Context, user api.UserResponse, args []string) ([]Reply, error) {
	kind, value := arg(args, 0), arg(args, 1)
	if kind != askMorning && kind != askEvening {
		if kind == "off" {
			u := user.User
			u.Morning = false
			u.Await = store.AwaitNothing
			return b.saveNotify(ctx, user, u)
		}
		kind, value = askMorning, kind
	}
	minutes, err := strconv.Atoi(value)
	if err != nil {
		return b.notifyMenu(user, true), nil
	}
	return b.applyTime(ctx, user, kind, minutes, true)
}

// setTime обрабатывает время, написанное текстом.
func (b *Bot) setTime(ctx context.Context, user api.UserResponse, await, text string) ([]Reply, error) {
	kind, current := askMorning, user.User.MorningAt
	if await == store.AwaitEveningTime {
		kind, current = askEvening, user.User.EveningAt
	}
	minutes, ok := parseClock(text)
	if !ok {
		// Ожидание не снимаем: человек хотел настроить время и промахнулся
		// форматом, а не передумал. Выход из диалога — кнопкой «назад».
		return []Reply{{Text: badTimeText, Keyboard: TimeKeyboard(kind, current)}}, nil
	}
	return b.applyTime(ctx, user, kind, minutes, false)
}

// applyTime проверяет время диапазоном и сохраняет его.
func (b *Bot) applyTime(ctx context.Context, user api.UserResponse, kind string, minutes int, edit bool) ([]Reply, error) {
	u := user.User
	from, to, what, current := morningFrom, morningTo, "Утреннее сообщение", u.MorningAt
	if kind == askEvening {
		from, to, what, current = eveningFrom, eveningTo, "Вечернее сообщение", u.EveningAt
	}
	if minutes < from || minutes > to {
		// Галочка на клавиатуре остаётся у сохранённого времени, а не уезжает
		// к отвергнутому: человек должен видеть, что ничего не поменялось.
		return []Reply{{
			Text: fmt.Sprintf("%s можно назначить с <b>%s</b> до <b>%s</b>. Напиши время из этого промежутка.",
				what, formatMinute(from), formatMinute(to)),
			Edit:     edit,
			Keyboard: TimeKeyboard(kind, current),
		}}, nil
	}

	if kind == askEvening {
		u.EveningAt, u.Evening = minutes, true
	} else {
		u.MorningAt, u.Morning = minutes, true
	}
	// Выбор времени — это и согласие получать сообщение: иначе настройка
	// молча уходит в пустоту при выключенном тумблере.
	u.Notify = true
	u.Await = store.AwaitNothing
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}

	replies := b.notifyMenu(user, edit)
	replies[0].Text = fmt.Sprintf("✅ %s — в <b>%s</b>.\n\n", what, formatMinute(minutes)) + replies[0].Text
	return replies, nil
}

// parseClock разбирает время дня в минуты от полуночи.
//
// Принимает «7:15», «7.15», «715» и просто «7»: люди пишут время как придётся,
// и спорить с человеком о разделителе — худший способ настроить рассылку.
func parseClock(s string) (int, bool) {
	var digits []rune
	sep := -1
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, r)
		case strings.ContainsRune(":.,-— ", r):
			if sep >= 0 {
				return 0, false
			}
			sep = len(digits)
		default:
			return 0, false
		}
	}

	var hh, mm string
	switch {
	case sep >= 0:
		hh, mm = string(digits[:sep]), string(digits[sep:])
	case len(digits) > 0 && len(digits) <= 2:
		hh, mm = string(digits), "0" // «7» — это 07:00
	case len(digits) == 3 || len(digits) == 4:
		hh, mm = string(digits[:len(digits)-2]), string(digits[len(digits)-2:])
	default:
		return 0, false
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// saveUser сохраняет настройки и обновляет локальную копию пользователя,
// чтобы дальнейшие шаги в этом же обработчике видели свежие данные.
func (b *Bot) saveUser(ctx context.Context, user *api.UserResponse, u store.User) error {
	// Идентификатор проставляет loadUser. Если его тут нет — сломан вызывающий
	// код, и лучше упасть с внятной ошибкой, чем молча получить отказ от API.
	if u.Platform == "" || u.ExtID == "" {
		return fmt.Errorf("botcore: сохранение пользователя без идентификатора: %+v", u)
	}
	if err := b.api.SaveUser(ctx, u); err != nil {
		return err
	}
	fresh, err := b.loadUser(ctx, u.Platform, u.ExtID)
	if err != nil {
		return err
	}
	*user = fresh
	return nil
}

// settings рисует меню настроек по уже загруженному пользователю: Handle
// прочитал его в начале обработки, и ходить за ним второй раз незачем.
func (b *Bot) settings(user api.UserResponse, edit bool) ([]Reply, error) {
	return []Reply{{
		Text:     settingsText,
		Keyboard: SettingsKeyboard(user),
		Edit:     edit,
		Menu:     menuFor(user),
	}}, nil
}

// menuFor показывает нижнее меню только тому, кому есть что им открыть.
func menuFor(user api.UserResponse) *Menu {
	if user.User.Configured() {
		return MainMenu()
	}
	return nil
}

// hasSubgroup сообщает, есть ли такая подгруппа у группы.
func hasSubgroup(subs []store.Subgroup, id int64) bool {
	for _, s := range subs {
		if s.ID == id {
			return true
		}
	}
	return false
}

// ChangeMessage готовит сообщение о правке расписания.
//
// Задетые дни перечисляются прямо в тексте, а «до/после» прячется за кнопкой.
// Так и должно быть: список дат отвечает на вопрос «меня это касается?» —
// самый частый и самый быстрый, — а разбираться, что именно переехало, идут
// единицы, и платить за них запросом на каждого адресата рассылки нельзя.
//
// Обращений к raspd тут по-прежнему нет: дни приезжают в самом сообщении
// очереди, потому что raspd посчитал их один раз на всю группу.
func (b *Bot) ChangeMessage(u store.User, p store.ChangePayload) (string, *Keyboard) {
	tz := u.TZOffset
	if tz == 0 {
		tz = b.tz
	}
	today := TodayIn(tz)
	group := strconv.FormatInt(p.GroupID, 10)

	text := "📌 <b>Расписание изменилось</b>\n\nВуз поправил " + esc(monthPhrase(p.Year, p.Month)) + "."
	var rows [][]Button

	switch days := p.Days; len(days) {
	case 0:
		// Разобрать правку по дням не вышло — снимок мог не сохраниться, или
		// правка приехала от версии, которая их ещё не считала. Сообщение
		// остаётся тем, чем было: «загляни».
		text += " Загляни — возможно, поменялись твои пары."
	case 1:
		text += "\n\nИзменился день: <b>" + esc(changeDaysPhrase(p.Year, p.Month, days, changeDaysInText)) + "</b>."
		rows = append(rows, []Button{{
			Label: "🔍 Что изменилось",
			Data:  cb(cbChangeDay, group, p.Date(days[0])),
		}})
	default:
		text += "\n\nИзменились дни: <b>" + esc(changeDaysPhrase(p.Year, p.Month, days, changeDaysInText)) + "</b>."
		rows = append(rows, []Button{{Label: "🔍 Подробнее", Data: cb(cbChanges, group)}})
	}

	rows = append(rows,
		[]Button{
			{Label: MenuToday, Data: cb(cbDay, today)},
			{Label: MenuWeek, Data: cb(cbWeek, today)},
		},
		// Новости о правках включены по умолчанию, а приходят без спроса —
		// значит и отписка должна быть там же, где само сообщение. Искать её в
		// «Настройки → Уведомления» человек не пойдёт: он либо стерпит, либо
		// заблокирует бота целиком, и тогда мы потеряем и утреннее расписание.
		[]Button{{Label: "🔕 Не сообщать о правках", Data: cb(cbChangesSet, changesOff)}},
	)
	return text, &Keyboard{Rows: rows}
}

// Что делает кнопка правок вне вкладки настроек; едет в callback-данных.
const (
	changesOn  = "on"
	changesOff = "off"
)

// setChanges включает или выключает новости о правках кнопкой из сообщения, а
// не из вкладки настроек.
//
// Здесь не переключатель: и новость, и подтверждение отписки живут в переписке
// вечно, а нажатие спустя месяц должно делать ровно то, что написано на
// кнопке. Поэтому нужное состояние едет в самой кнопке, и повторное нажатие
// ничего не возвращает обратно.
//
// Отписка отвечает новым сообщением, а не правкой: в новости есть кнопка «что
// изменилось», и затирать её подтверждением значило бы отнять у человека
// подробности той самой правки, ради которой он и открыл переписку. Возврат,
// наоборот, правит подтверждение на вкладку уведомлений — она и показывает,
// что рассылка снова включена.
func (b *Bot) setChanges(ctx context.Context, user api.UserResponse, on bool) ([]Reply, error) {
	if user.User.Changes != on {
		u := user.User
		u.Changes = on
		// Возврат поднимает и главный тумблер: за время жизни сообщения
		// уведомления могли выключить целиком, а включать «правки» внутри
		// выключенных значит обещать то, что не придёт.
		if on {
			u.Notify = true
		}
		if err := b.saveUser(ctx, &user, u); err != nil {
			return b.unavailable(err), nil
		}
	}
	if on {
		return b.notifyMenu(user, true), nil
	}
	return []Reply{{
		Text: changesOffText,
		Keyboard: &Keyboard{Rows: [][]Button{
			{{Label: "🔔 Вернуть новости о правках", Data: cb(cbChangesSet, changesOn)}},
			{{Label: "⚙️ Настройки уведомлений", Data: cbNotifyMen}},
		}},
		Menu: menuFor(user),
	}}, nil
}

// Что делает кнопка утренней рассылки вне вкладки настроек; едет в
// callback-данных.
const (
	morningOn  = "on"
	morningOff = "off"
)

// setMorning включает или выключает утреннее расписание кнопкой из самой
// рассылки, а не из вкладки настроек.
//
// Здесь, как и с правками, не переключатель: утреннее сообщение приходит
// каждый будний день и остаётся в переписке навсегда, а нажатие на
// позавчерашнее должно делать ровно то, что написано на кнопке, а не зависеть
// от того, сколько раз человек её нажимал раньше.
//
// Отписка отвечает новым сообщением, а не правкой: под кнопкой стоит
// расписание на сегодня, и затирать его подтверждением значило бы отнять то,
// ради чего человек и открыл переписку. Возврат, наоборот, правит
// подтверждение на вкладку уведомлений — она показывает, что рассылка снова
// включена, и там же меняется её время.
func (b *Bot) setMorning(ctx context.Context, user api.UserResponse, on bool) ([]Reply, error) {
	if user.User.Morning != on {
		u := user.User
		u.Morning = on
		// Возврат поднимает и главный тумблер: за время жизни сообщения
		// уведомления могли выключить целиком, а включать «утро» внутри
		// выключенных значит обещать то, что не придёт.
		if on {
			u.Notify = true
		}
		if err := b.saveUser(ctx, &user, u); err != nil {
			return b.unavailable(err), nil
		}
	}
	if on {
		return b.notifyMenu(user, true), nil
	}
	return []Reply{{
		Text: morningOffText,
		Keyboard: &Keyboard{Rows: [][]Button{
			{{Label: "🔔 Вернуть утреннее расписание", Data: cb(cbMorningSet, morningOn)}},
			{{Label: "⚙️ Настройки уведомлений", Data: cbNotifyMen}},
		}},
		Menu: menuFor(user),
	}}, nil
}

// changeList показывает, в каких днях недавно правили расписание.
//
// Список берётся из raspd живым, а не из кнопки: сообщение живёт в переписке
// сколько угодно, а снимки — трое суток, и обещать по старой кнопке то, чего
// уже нет, нельзя.
func (b *Bot) changeList(ctx context.Context, user api.UserResponse, group int64) ([]Reply, error) {
	groupID, _, asGuest := target(user, group)
	if groupID == 0 {
		return requireGroup(user), nil
	}
	resp, err := b.api.ChangedDates(ctx, groupID)
	if err != nil {
		return b.unavailable(err), nil
	}
	if len(resp.Dates) == 0 {
		return []Reply{{
			Text: changesGoneText,
			Edit: true,
			Keyboard: &Keyboard{Rows: [][]Button{{
				{Label: MenuToday, Data: cbView(cbDay, todayArg, asGuest)},
				{Label: MenuWeek, Data: cbView(cbWeek, "", asGuest)},
			}}},
			Menu: menuFor(user),
		}}, nil
	}

	text := "<b>🔍 Что изменилось</b>\n"
	if asGuest == 0 && user.Group != nil {
		text += "<i>" + esc(user.Group.Name) + "</i>\n"
	}
	text += "\n" + changesListText
	return []Reply{{
		Text:     guestPrefix(asGuest) + text,
		Keyboard: ChangeDatesKeyboard(resp.Dates, resp.Today, groupID, asGuest),
		Edit:     true,
		Menu:     menuFor(user),
	}}, nil
}

// changeDay показывает «до/после» одного дня.
func (b *Bot) changeDay(ctx context.Context, user api.UserResponse, group int64, date string) ([]Reply, error) {
	groupID, subgroupID, asGuest := target(user, group)
	if groupID == 0 {
		return requireGroup(user), nil
	}
	if _, err := schedule.ParseDate(date); err != nil {
		return b.changeList(ctx, user, group)
	}

	resp, err := b.api.ChangedDay(ctx, groupID, subgroupID, date)
	if err != nil {
		return b.unavailable(err), nil
	}
	return []Reply{{
		Text:     guestPrefix(asGuest) + FormatChangeDay(resp),
		Keyboard: ChangeDayKeyboard(date, groupID, asGuest),
		Edit:     true,
		Menu:     menuFor(user),
	}}, nil
}

// Digest — готовое сообщение рассылки.
type Digest struct {
	Text string
	KB   *Keyboard
	// Empty — в этот день занятий нет. Слать такое сообщение или промолчать,
	// решает не бот, а настройка человека: см. store.User.EmptyDays.
	Empty bool
	// When — о каком дне речь, словом: «Сегодня», «Завтра». Нужно подсказке
	// про пустые дни, которая говорит о том же дне другими словами.
	When string
}

// Digest собирает сообщение рассылки: утреннее — про сегодня, вечернее — про
// следующий учебный день.
//
// Вечером ищем ближайший день с занятиями, учитывая подгруппу и пустые
// учебные дни в сетке.
func (b *Bot) Digest(ctx context.Context, u store.User, kind store.NotifyKind) (Digest, error) {
	var resp api.DayResponse
	if kind == store.NotifyEvening {
		next, err := b.api.NextStudyDay(ctx, u.GroupID, u.SubgroupID)
		if err != nil {
			return Digest{}, err
		}
		if next.Lesson == nil {
			reply := formatNextStudyDay(next, api.UserResponse{User: u})
			return Digest{Text: reply.Text, KB: reply.Keyboard,
				Empty: !next.Missing && !next.Stale, When: "В ближайшие 14 дней"}, nil
		}
		resp = api.DayResponse{Context: next.Context, Day: next.Day,
			Prev: next.Prev, Next: next.Next, Freshness: next.Freshness}
	} else {
		var err error
		resp, err = b.api.Day(ctx, u.GroupID, u.SubgroupID, "")
		if err != nil {
			return Digest{}, err
		}
	}

	head, when := "🌅 <b>Доброе утро!</b>", "Сегодня"
	if kind == store.NotifyEvening {
		head, when = "🌙 <b>Расписание на завтра</b>", "Завтра"
		if relativeHint(resp.Day.Date, resp.Today) != "завтра" {
			head, when = "🌙 <b>Следующий учебный день</b>", "В следующий учебный день"
		}
	}
	kb := DayKeyboard(resp, 0)
	if kind == store.NotifyMorning {
		// Утренняя рассылка включена по умолчанию и приходит до того, как
		// человек проснулся, — значит и отписка должна быть в самом сообщении.
		// Искать её в «Настройки → Уведомления» он не пойдёт: он либо стерпит,
		// либо заблокирует бота целиком, и тогда мы потеряем и новости о
		// правках.
		kb.Rows = append(kb.Rows, []Button{
			{Label: "🔕 Не писать по утрам", Data: cb(cbMorningSet, morningOff)},
		})
	}
	return Digest{
		Text:  head + "\n\n" + FormatDay(resp),
		KB:    kb,
		Empty: resp.Day.Empty() && !resp.Missing && !resp.Stale,
		When:  when,
	}, nil
}

// EmptyHint — разовое объяснение того, почему в пустой день бот промолчал.
//
// Уведомления о пустых днях выключены по умолчанию, и это правильно: «занятий
// нет» — сообщение, которого никто не просил. Но человек, включивший рассылку
// и не получивший её в субботу, считает бота сломанным, а не тактичным.
// Поэтому молчание объясняется вслух ровно один раз за всю жизнь записи, и
// тут же даётся кнопка, которая его отменяет.
func (b *Bot) EmptyHint(when string) (string, *Keyboard) {
	return fmt.Sprintf(emptyHintText, when), &Keyboard{Rows: [][]Button{
		{{Label: "📭 Присылать и в пустые дни", Data: cbNotifyEmp}},
		{{Label: "⚙️ Настройки уведомлений", Data: cbNotifyMen}},
	}}
}

// TodayIn возвращает сегодняшнюю дату в поясе пользователя — нужно
// рассыльщику, чтобы не слать расписание за вчера.
func TodayIn(tzOffset int) string { return schedule.FormatDate(userTime(tzOffset)) }
