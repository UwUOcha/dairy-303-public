// Package tg — тонкий адаптер телеграма поверх botcore.
//
// Здесь только транспорт: приём апдейтов длинным опросом, перевод клавиатур в
// типы Bot API и отправка. Ни одного решения о том, что показать, — все
// сценарии живут в botcore и одинаковы для всех платформ.
package tg

import (
	"context"
	"errors"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/UwUOcha/dairy-303-public/internal/botcore"
)

// Platform — метка платформы в хранилище пользователей.
const Platform = "tg"

// Adapter связывает Bot API и botcore.
//
// Telegram допускает ровно одну разметку на сообщение, а расписание почти
// всегда идёт с инлайн-кнопками. Поэтому постоянное меню отправляется
// отдельным коротким сообщением при изменении версии. Подтверждение доставки
// хранится в базе, поэтому перезапуск не вызывает повторную отправку меню.
type Adapter struct {
	bot  *tgbot.Bot
	core *botcore.Bot
	log  *slog.Logger
}

// New поднимает адаптер.
func New(token string, core *botcore.Bot, log *slog.Logger) (*Adapter, error) {
	a := &Adapter{core: core, log: log}
	b, err := tgbot.New(token,
		tgbot.WithDefaultHandler(a.handle),
		// Список типов апдейтов задаётся явно, а не оставляется на усмотрение
		// телеграма. Если его не передать, телеграм берёт последнее значение,
		// когда-либо выставленное для этого токена, — в том числе оставшееся
		// от прошлой версии бота. Тогда нажатия кнопок просто не приходят, и
		// снаружи это выглядит как «жму, и ничего не происходит».
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		// Ошибки библиотеки по умолчанию уходят в стандартный log мимо нашего
		// журнала. Самая важная из них — конфликт getUpdates, когда тем же
		// токеном пользуется кто-то ещё.
		tgbot.WithErrorsHandler(func(err error) {
			if isConflict(err) {
				log.Error("телеграм отдаёт апдейты кому-то ещё: тем же токеном "+
					"пользуется другой запущенный бот или установлен вебхук",
					"ошибка", err)
				return
			}
			log.Error("телеграм", "ошибка", err)
		}),
	)
	if err != nil {
		return nil, err
	}
	a.bot = b
	return a, nil
}

// Run запускает длинный опрос.
//
// Длинный опрос вместо вебхука — сознательный выбор: ни nginx, ни сертификата,
// ни открытого порта. При тысячах сообщений в день вебхук не даёт ничего.
func (a *Adapter) Run(ctx context.Context) {
	a.diagnose(ctx)
	a.setCommands(ctx)
	a.bot.Start(ctx)
}

// diagnose печатает при старте всё, что нужно, чтобы понять, почему бот молчит.
//
// Проверять это руками по одному запросу — потерянный вечер, а причин ровно
// три: не тот токен, установленный вебхук и чужая копия бота на том же токене.
func (a *Adapter) diagnose(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	me, err := a.bot.GetMe(ctx)
	if err != nil {
		a.log.Error("не удалось представиться телеграму", "ошибка", err)
		return
	}
	a.log.Info("бот подключён", "имя", "@"+me.Username, "id", me.ID)

	info, err := a.bot.GetWebhookInfo(ctx)
	if err != nil {
		a.log.Warn("не удалось узнать состояние вебхука", "ошибка", err)
		return
	}
	if info.URL != "" {
		// Вебхук и длинный опрос несовместимы: пока он установлен, getUpdates
		// отвечает конфликтом и бот не получает ни одного апдейта.
		a.log.Error("для этого бота установлен вебхук — длинный опрос работать не будет",
			"url", info.URL,
			"ожидает_апдейтов", info.PendingUpdateCount,
			"как_снять", "curl -s 'https://api.telegram.org/bot<токен>/deleteWebhook'")
		return
	}
	a.log.Info("вебхука нет, идём длинным опросом",
		"ожидает_апдейтов", info.PendingUpdateCount,
		"типы_апдейтов_у_телеграма", allowedOrAll(info.AllowedUpdates))
}

// allowedOrAll описывает, какие типы апдейтов телеграм помнит для токена.
func allowedOrAll(allowed []string) string {
	if len(allowed) == 0 {
		return "по умолчанию (все)"
	}
	return strings.Join(allowed, ",")
}

// isConflict распознаёт 409 от getUpdates — признак того, что тем же токеном
// пользуется кто-то ещё.
func isConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, tgbot.ErrorConflict) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "Conflict") || strings.Contains(msg, "terminated by other getUpdates")
}

// setCommands регистрирует список команд, чтобы телеграм показывал их
// подсказкой у поля ввода.
func (a *Adapter) setCommands(ctx context.Context) {
	_, err := a.bot.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "web_logout", Description: "Завершить все сеансы сайта"},
			{Command: "today", Description: "Расписание на сегодня"},
			{Command: "tomorrow", Description: "Ближайший день с занятиями начиная с завтра"},
			{Command: "week", Description: "Расписание на неделю"},
			{Command: "next", Description: "Когда следующая пара моей подгруппы"},
			{Command: "now", Description: "Что идёт сейчас"},
			{Command: "settings", Description: "Группа, подгруппа, рассылка"},
			{Command: "help", Description: "Что я умею"},
		},
	})
	if err != nil {
		a.log.Warn("не удалось зарегистрировать команды", "ошибка", err)
	}
}

// handleTimeout — потолок на обработку одного апдейта.
//
// Пользовательский путь не должен зависеть от того, жив ли сервер вуза: если
// raspd задумался, честнее сказать об этом, чем молчать.
const handleTimeout = 20 * time.Second

// handle — единственный обработчик апдейтов.
func (a *Adapter) handle(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	upd, chatID, messageID, queryID, ok := convert(u)
	upd.Private = chatID > 0 && strconv.FormatInt(chatID, 10) == upd.UserID

	// Факт получения апдейта логируем всегда. Без этой строчки невозможно
	// отличить «бот получил нажатие и не справился» от «бот его вообще не
	// получил» — а лечится это совершенно по-разному.
	a.log.Info("апдейт", "id", u.ID, "пользователь", upd.UserID,
		"кнопка", upd.Callback, "текст", botcore.AuthText(upd.Text))

	// Ответить на нажатие надо первым делом и любой ценой.
	//
	// Пока бот не вызовет answerCallbackQuery, телеграм крутит на кнопке
	// часик — и терпит с этим всего около пятнадцати секунд, после чего
	// спиннер просто гаснет, не показав ни ошибки, ни изменений. Раньше
	// ответ уходил после всей работы, а работа умеет ходить к вузу за
	// недостающим месяцем: стоило серверу вуза стать недоступным, и кнопка
	// «зависала» без единого сообщения.
	if queryID != "" {
		a.answerCallback(ctx, queryID)
	}
	if !ok {
		a.log.Warn("апдейт без опознаваемого отправителя пропущен", "апдейт", u.ID)
		return
	}

	started := time.Now()
	hctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()

	replies, err := a.core.Handle(hctx, upd)
	if err != nil {
		a.log.Error("обработка апдейта", "пользователь", upd.UserID, "ошибка", err)
		replies = []botcore.Reply{{Text: "Что-то пошло не так. Попробуй ещё раз."}}
	}
	if d := time.Since(started); d > 3*time.Second {
		a.log.Warn("медленная обработка", "пользователь", upd.UserID,
			"кнопка", upd.Callback, "текст", botcore.AuthText(upd.Text), "длительность", d.Round(time.Millisecond))
	}

	delivered := ""
	for _, r := range replies {
		if r.Menu != nil && r.Menu.Version() == delivered {
			r.MenuSeen = true
		}
		if a.send(ctx, chatID, messageID, r) && !r.MenuSeen {
			delivered = r.Menu.Version()
			if err := a.core.MenuDelivered(ctx, Platform, upd.UserID, r.Menu); err != nil {
				a.log.Warn("не удалось сохранить версию меню", "пользователь", upd.UserID, "ошибка", err)
			}
		}
	}
}

// convert переводит апдейт телеграма в платформо-независимый вид.
//
// queryID возвращается всегда, когда пришло нажатие кнопки, — даже если само
// сообщение опознать не удалось. Ответить телеграму важнее, чем разобраться,
// куда слать результат.
func convert(u *models.Update) (upd botcore.Update, chatID int64, messageID int, queryID string, ok bool) {
	switch {
	case u.CallbackQuery != nil:
		q := u.CallbackQuery
		upd = botcore.Update{
			Platform:  Platform,
			UserID:    strconv.FormatInt(q.From.ID, 10),
			Callback:  q.Data,
			FirstName: q.From.FirstName,
		}
		switch {
		case q.Message.Message != nil:
			return upd, q.Message.Message.Chat.ID, q.Message.Message.ID, q.ID, true
		case q.Message.InaccessibleMessage != nil:
			// Сообщение слишком старое, чтобы его читать или править, но чат
			// известен — отвечаем новым сообщением вместо редактирования.
			return upd, q.Message.InaccessibleMessage.Chat.ID, 0, q.ID, true
		}
		return upd, 0, 0, q.ID, false

	case u.Message != nil && u.Message.From != nil:
		m := u.Message
		return botcore.Update{
			Platform:  Platform,
			UserID:    strconv.FormatInt(m.From.ID, 10),
			Text:      m.Text,
			FirstName: m.From.FirstName,
		}, m.Chat.ID, 0, "", true
	}
	return upd, 0, 0, "", false
}

func (a *Adapter) answerCallback(ctx context.Context, queryID string) {
	// Отдельный короткий контекст: этот вызов не должен ждать вместе с
	// основной работой — он существует ровно чтобы её не ждал пользователь.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if _, err := a.bot.AnswerCallbackQuery(actx, &tgbot.AnswerCallbackQueryParams{
		CallbackQueryID: queryID,
	}); err != nil {
		a.log.Warn("не удалось ответить на нажатие кнопки", "ошибка", err)
	}
}

// send отправляет один ответ, редактируя сообщение, если это уместно.
// Возвращает true только при успешной отправке постоянной клавиатуры.
func (a *Adapter) send(ctx context.Context, chatID int64, messageID int, r botcore.Reply) bool {
	if r.Text == "" {
		return false
	}
	text, parseMode := telegramText(r.Text)

	// Редактируем только то сообщение, из которого пришло нажатие. Листание
	// стрелками не должно плодить копии расписания в ленте чата.
	if r.Edit && messageID != 0 {
		_, err := a.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   messageID,
			Text:        text,
			ParseMode:   parseMode,
			ReplyMarkup: inlineMarkup(r.Keyboard),
		})
		if err == nil {
			return a.sendMenu(ctx, chatID, r)
		}
		// Повторное нажатие той же кнопки не меняет текст, и телеграм отвечает
		// ошибкой. Это не сбой — пользователь просто увидел то же самое.
		if isNotModified(err) {
			return a.sendMenu(ctx, chatID, r)
		}
		a.log.Debug("не удалось отредактировать сообщение, отправляю новое", "ошибка", err)
	}

	params := &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   parseMode,
		ReplyMarkup: inlineMarkup(r.Keyboard),
	}
	// Если инлайн-кнопок нет, нижнее меню можно прицепить прямо сюда —
	// отдельное сообщение тогда не нужно вовсе.
	attached := r.Keyboard == nil && r.Menu != nil
	if attached {
		params.ReplyMarkup = replyMarkup(r.Menu)
	}

	if _, err := a.bot.SendMessage(ctx, params); err != nil {
		a.log.Error("отправка сообщения", "чат", chatID, "ошибка", err)
		return false
	}
	if !attached {
		return a.sendMenu(ctx, chatID, r)
	}
	return true
}

// sendMenu показывает нижнее меню отдельным сообщением — только тому, кто его
// ещё не видел в этой версии.
func (a *Adapter) sendMenu(ctx context.Context, chatID int64, r botcore.Reply) bool {
	if r.Menu == nil || r.MenuSeen {
		return false
	}
	if _, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        "Меню всегда внизу 👇",
		ReplyMarkup: replyMarkup(r.Menu),
	}); err != nil {
		a.log.Debug("отправка меню", "чат", chatID, "ошибка", err)
		return false
	}
	return true
}

// Send отправляет сообщение по инициативе бота — нужно для утренней рассылки.
func (a *Adapter) Send(ctx context.Context, extID, text string, kb *botcore.Keyboard) error {
	chatID, err := strconv.ParseInt(extID, 10, 64)
	if err != nil {
		return err
	}
	text, parseMode := telegramText(text)
	_, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   parseMode,
		ReplyMarkup: inlineMarkup(kb),
	})
	return err
}

// Telegram ограничивает сообщение 4096 видимыми символами. Обычное
// расписание заметно короче, но поля приходят от внешнего сервера и жёсткой
// границы у них нет. Если ответ всё-таки разросся, отправляем сокращённый
// простой текст: оборванный HTML Telegram отверг бы целиком.
const maxMessageRunes = 4096

const shortenedMessage = "\n\n… Сообщение сокращено."

func telegramText(s string) (string, models.ParseMode) {
	plain := plainHTML(s)
	if utf8.RuneCountInString(plain) <= maxMessageRunes {
		return s, models.ParseModeHTML
	}

	runes := []rune(plain)
	budget := maxMessageRunes - utf8.RuneCountInString(shortenedMessage)
	cut := budget
	prefix := string(runes[:budget])
	if i := strings.LastIndex(prefix, "\n"); i >= 0 {
		// До перевода строки лежат только целые руны, поэтому байтовый индекс
		// безопасно переводим обратно в число рун.
		line := utf8.RuneCountInString(prefix[:i])
		if line > budget/2 {
			cut = line
		}
	}
	return strings.TrimSpace(string(runes[:cut])) + shortenedMessage, ""
}

// plainHTML снимает небольшое подмножество тегов, которым верстает botcore.
// Пользовательские «<» к этому моменту экранированы и тегами не считаются.
func plainHTML(s string) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(s, '<')
		if open < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:open])
		close := strings.IndexByte(s[open:], '>')
		if close < 0 {
			b.WriteString(s[open:])
			break
		}
		s = s[open+close+1:]
	}
	return strings.TrimSpace(html.UnescapeString(b.String()))
}

func inlineMarkup(k *botcore.Keyboard) models.ReplyMarkup {
	if k == nil || len(k.Rows) == 0 {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(k.Rows))
	for _, row := range k.Rows {
		out := make([]models.InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			out = append(out, models.InlineKeyboardButton{Text: b.Label, CallbackData: b.Data})
		}
		if len(out) > 0 {
			rows = append(rows, out)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func replyMarkup(m *botcore.Menu) models.ReplyMarkup {
	if m == nil || len(m.Rows) == 0 {
		return nil
	}
	rows := make([][]models.KeyboardButton, 0, len(m.Rows))
	for _, row := range m.Rows {
		out := make([]models.KeyboardButton, 0, len(row))
		for _, label := range row {
			out = append(out, models.KeyboardButton{Text: label})
		}
		rows = append(rows, out)
	}
	return models.ReplyKeyboardMarkup{Keyboard: rows, ResizeKeyboard: true, IsPersistent: true}
}

// isNotModified распознаёт безобидный ответ телеграма на редактирование
// сообщения тем же содержимым.
//
// Отдельного типа под эту ошибку в библиотеке нет — приходится смотреть на
// текст описания, как его прислал Bot API.
func isNotModified(err error) bool {
	return err != nil && strings.Contains(err.Error(), "message is not modified")
}

// RetryAfter возвращает, сколько секунд телеграм просит подождать, если это
// был отказ по лимиту. Нужен рассыльщику: 30 сообщений в секунду — реальный
// потолок всей системы, и уважать retry_after дешевле, чем ловить бан.
func RetryAfter(err error) (int, bool) {
	var e *tgbot.TooManyRequestsError
	if errors.As(err, &e) {
		return e.RetryAfter, true
	}
	return 0, false
}

// IsBlocked сообщает, что пользователь заблокировал бота: такого адресата
// надо не ретраить, а выключать ему рассылку.
func IsBlocked(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, tgbot.ErrorForbidden) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "bot was blocked by the user") ||
		strings.Contains(msg, "user is deactivated") ||
		strings.Contains(msg, "chat not found")
}

// Platform реализует botcore.Sender.
func (a *Adapter) Platform() string { return Platform }

// Retryable реализует botcore.Sender: телеграм сам говорит, сколько ждать.
func (a *Adapter) Retryable(err error) (time.Duration, bool) {
	if secs, ok := RetryAfter(err); ok {
		return time.Duration(secs) * time.Second, true
	}
	return 0, false
}

// Undeliverable реализует botcore.Sender.
func (a *Adapter) Undeliverable(err error) bool { return IsBlocked(err) }

// Reachable реализует botcore.Reacher: жив ли ещё диалог с этим человеком.
//
// Через sendChatAction, потому что тихого способа узнать это у Bot API нет.
// getChat выглядит уместнее, но на заблокировавшем боте он как ни в чём не
// бывало отдаёт описание чата: телеграм считает вопрос «что это за чат»
// безобидным и отвечает на него всегда. Отказ приходит только на попытку
// что-то в диалог отправить — а «печатает…» из всего отправляемого самое
// дешёвое: сообщения не остаётся, уведомления не будет, и увидит его лишь
// тот, у кого именно в эту секунду открыт наш чат.
func (a *Adapter) Reachable(ctx context.Context, extID string) (bool, error) {
	chatID, err := strconv.ParseInt(extID, 10, 64)
	if err != nil {
		return false, err
	}
	_, err = a.bot.SendChatAction(ctx, &tgbot.SendChatActionParams{
		ChatID: chatID,
		Action: models.ChatActionTyping,
	})
	if IsBlocked(err) {
		return false, nil
	}
	return err == nil, err
}
