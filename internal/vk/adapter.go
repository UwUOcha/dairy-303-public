// Package vk — тонкий адаптер ВКонтакте поверх botcore.
//
// Здесь только транспорт: приём событий Bots Long Poll, перевод клавиатур в
// объекты VK API и отправка. Ни одного решения о том, что показать, — сценарии
// живут в botcore и одинаковы для обеих платформ.
//
// Отличий от телеграма ровно три, и все они спрятаны здесь: разметки в
// сообщениях нет (см. plain), клавиатур две с разными лимитами (см.
// keyboard.go), а нижняя панель одна на диалог и к сообщению не привязана.
package vk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	vkapi "github.com/SevereCloud/vksdk/v3/api"
	"github.com/SevereCloud/vksdk/v3/events"
	longpoll "github.com/SevereCloud/vksdk/v3/longpoll-bot"
	"github.com/SevereCloud/vksdk/v3/object"

	"github.com/UwUOcha/dairy-303-public/internal/botcore"
)

// Platform — метка платформы в хранилище пользователей.
const Platform = "vk"

// Что сейчас лежит на нижней панели диалога.
//
// Панель у ВКонтакте одна, и претендентов на неё двое: постоянное меню и
// длинные списки, не влезающие в инлайн-клавиатуру. Помнить, кто её занял,
// нужно, чтобы не показывать меню дважды и не оставлять протухший список.
const (
	panelNone   = ""
	panelMenu   = "menu"
	panelPicker = "picker"
)

// Adapter связывает VK API и botcore.
type Adapter struct {
	vk   *vkapi.VK
	core *botcore.Bot
	log  *slog.Logger

	// panel помнит состояние нижней панели по каждому диалогу.
	panel sync.Map
	// names кэширует имена: users.get нужен ровно ради приветствия в /start,
	// и дёргать его на каждое сообщение было бы расточительством.
	names sync.Map

	// requireSub — пускать в сценарии только подписчиков сообщества.
	requireSub bool
	// members — подписчики сообщества; ими и открывается доступ (см. gate.go).
	members registry

	// comm — кто мы такие во ВКонтакте; узнаётся лениво и один раз.
	commMu sync.Mutex
	comm   group

	// chats не даёт напоминать о себе в одной беседе чаще раза в сутки.
	chats botcore.GroupChats
}

// New поднимает адаптер.
//
// Сети здесь нет намеренно: сообщество опрашивается уже в Run, и недоступность
// ВКонтакте при старте не должна ронять весь botd вместе с телеграмом.
func New(token string, requireSub bool, core *botcore.Bot, log *slog.Logger) (*Adapter, error) {
	if token == "" {
		return nil, errors.New("vk: пустой ключ доступа сообщества")
	}
	return &Adapter{vk: vkapi.NewVK(token), core: core, log: log, requireSub: requireSub}, nil
}

// pollRetry — пауза перед новой попыткой поднять длинный опрос.
const pollRetry = 15 * time.Second

// handleTimeout — потолок на обработку одного события.
//
// Пользовательский путь не должен зависеть от того, жив ли сервер вуза: если
// raspd задумался, честнее сказать об этом, чем молчать.
const handleTimeout = 20 * time.Second

// Run запускает длинный опрос и держит его поднятым.
//
// Библиотека выходит из опроса на первой же ошибке — в том числе на обычном
// обрыве связи. Поэтому цикл здесь свой: одна сетевая неприятность не должна
// молча выключать половину бота до перезапуска процесса.
func (a *Adapter) Run(ctx context.Context) {
	a.diagnose(ctx)
	if a.requireSub {
		// Отдельной горутиной, а не в цикле опроса: обход списка подписчиков
		// идёт своим ритмом и переживает обрывы длинного опроса.
		go a.keepMembers(ctx)
	}

	for ctx.Err() == nil {
		err := a.poll(ctx)
		if ctx.Err() != nil {
			return
		}
		a.log.Error("вконтакте: длинный опрос прерван", "ошибка", err, "повтор_через", pollRetry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollRetry):
		}
	}
}

func (a *Adapter) poll(ctx context.Context) error {
	lp, err := longpoll.NewLongPollCommunity(a.vk)
	if err != nil {
		return err
	}
	lp.MessageNew(a.onMessage)
	lp.MessageEvent(a.onEvent)
	if a.requireSub {
		lp.GroupJoin(a.onJoin)
		lp.GroupLeave(a.onLeave)
	}
	return lp.RunWithContext(ctx)
}

// diagnose печатает при старте всё, что нужно, чтобы понять, почему бот молчит.
//
// Причин, как и у телеграма, немного: не тот ключ, выключенные сообщения
// сообщества и нехватка права «Управление сообществом», без которого нельзя
// включить типы событий Long Poll. Первую видно сразу, про остальные две
// напоминаем текстом — искать это по одному запросу руками значит потерять
// вечер.
func (a *Adapter) diagnose(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	g, err := a.identify(ctx)
	if err != nil {
		a.log.Error("вконтакте: не удалось представиться — проверь VK_TOKEN, "+
			"это должен быть ключ доступа сообщества", "ошибка", err)
		return
	}
	a.log.Info("вконтакте: сообщество на связи",
		"имя", g.name, "id", g.id, "адрес", "vk.com/"+g.screen,
		"нужны_права", "сообщения сообщества + управление сообществом")
	if a.requireSub {
		a.log.Info("вконтакте: бот отвечает только подписчикам",
			"обход_списка_каждые", refreshInterval,
			"между_обходами", "события group_join и group_leave")
	}
}

// onMessage обрабатывает входящее сообщение.
func (a *Adapter) onMessage(ctx context.Context, obj events.MessageNewObject) {
	m := obj.Message
	if m.FromID <= 0 {
		// Сообщение от сообщества или без отправителя — отвечать некому.
		return
	}

	upd := botcore.Update{
		Platform: Platform,
		UserID:   strconv.Itoa(m.FromID),
		Text:     m.Text,
		Private:  m.PeerID == m.FromID,
	}
	if !upd.Private {
		// В беседе ни подписку не проверяем, ни сценарии не запускаем —
		// только зовём в личку (см. botcore/groupchat.go).
		go a.refuseChat(ctx, m.PeerID)
		return
	}

	// Сообщением приходят только команды, названия групп и кнопки нижнего
	// меню — последние botcore разбирает по подписи, как обычный текст. Наши
	// кнопки уезжают в message_event и сюда не попадают. Служебный payload
	// присылает сам мессенджер, и его надо разобрать.
	switch _, command := parsePayload([]byte(m.Payload)); command {
	case object.CommandNotSupportedButton:
		// Клиент слишком старый для наших кнопок. Молча проглотить это значит
		// оставить человека жать на то, что никогда не сработает.
		a.log.Info("вконтакте: клиент не поддерживает кнопки", "пользователь", upd.UserID)
		go a.send(ctx, m.PeerID, 0, botcore.Reply{Text: unsupportedText})
		return
	case "start":
		// Кнопка «Начать» в новом диалоге.
		upd.Text = "/start"
	}

	if strings.HasPrefix(m.Ref, "web_") && upd.Private && !strings.HasPrefix(upd.Text, "/login ") && upd.Text != "/web_logout" {
		upd.Text = "/start " + m.Ref
	}
	a.log.Info("вконтакте: сообщение", "пользователь", upd.UserID, "текст", botcore.AuthText(upd.Text))
	go a.process(ctx, upd, m.PeerID, 0)
}

// onEvent обрабатывает нажатие кнопки.
func (a *Adapter) onEvent(ctx context.Context, obj events.MessageEventObject) {
	cb, _ := parsePayload(obj.Payload)
	a.log.Info("вконтакте: нажатие", "пользователь", obj.UserID, "кнопка", cb)

	// Ответить на нажатие надо первым делом и любой ценой: пока бот молчит,
	// ВКонтакте крутит на кнопке часик, а потом показывает человеку ошибку.
	// Ровно та же история, что и с answerCallbackQuery у телеграма.
	if obj.PeerID != obj.UserID {
		// Нажатие в беседе: подсказку видит только нажавший, в беседу ничего
		// не уходит.
		a.answer(ctx, obj, botcore.GroupChatToast)
		return
	}
	a.answer(ctx, obj, "")

	if cb == "" {
		return
	}
	if cb == cbSubscribed {
		go a.confirm(ctx, strconv.Itoa(obj.UserID), obj.PeerID)
		return
	}
	go a.process(ctx, botcore.Update{
		Platform: Platform,
		UserID:   strconv.Itoa(obj.UserID),
		Callback: cb,
		Private:  obj.PeerID == obj.UserID,
	}, obj.PeerID, obj.ConversationMessageID)
}

// answer снимает с кнопки крутилку; непустой toast ВКонтакте покажет
// нажавшему всплывающей плашкой.
func (a *Adapter) answer(ctx context.Context, obj events.MessageEventObject, toast string) {
	// Отдельный короткий контекст: этот вызов не должен ждать вместе с
	// основной работой — он существует ровно чтобы её не ждал пользователь.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	params := vkapi.Params{
		"event_id": obj.EventID,
		"user_id":  obj.UserID,
		"peer_id":  obj.PeerID,
	}
	if toast != "" {
		params["event_data"] = snackbar(toast)
	}
	_, err := a.vk.MessagesSendMessageEventAnswer(params.WithContext(actx))
	if err != nil {
		a.log.Warn("вконтакте: не удалось ответить на нажатие кнопки", "ошибка", err)
	}
}

// snackbar собирает event_data для всплывающей плашки.
func snackbar(text string) string {
	b, _ := json.Marshal(map[string]string{"type": "show_snackbar", "text": text})
	return string(b)
}

// refuseChat зовёт из беседы в личку — не чаще раза в сутки на беседу.
func (a *Adapter) refuseChat(ctx context.Context, peer int) {
	if !a.chats.Allow(strconv.Itoa(peer), time.Now()) {
		return
	}
	link := ""
	if g, err := a.identify(ctx); err == nil && g.screen != "" {
		link = "vk.me/" + g.screen
	}
	a.log.Info("вконтакте: сообщение в беседе, зову в личку", "диалог", peer)
	a.send(ctx, peer, 0, botcore.Reply{Text: botcore.GroupChatText(link)})
}

// process прогоняет событие через сценарии и отправляет, что вышло.
//
// Вызывается в своей горутине: длинный опрос выдаёт события по очереди и не
// заберёт следующее, пока обработчик не вернётся. Один медленный ответ raspd
// иначе тормозил бы всех сразу.
func (a *Adapter) process(ctx context.Context, upd botcore.Update, peer, cmID int) {
	// Входная дверь: сценарии botcore начинаются за ней. Ответ всегда из
	// памяти — на пути человека сети нет вовсе (см. gate.go).
	if !a.subscribed(upd.UserID) {
		a.log.Info("вконтакте: не подписан", "пользователь", upd.UserID)
		a.askSubscribe(ctx, peer, subscribeLead)
		return
	}

	if upd.Text == "/start" {
		upd.FirstName = a.firstName(ctx, upd.UserID)
	}

	started := time.Now()
	hctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()

	replies, err := a.core.Handle(hctx, upd)
	if err != nil {
		a.log.Error("вконтакте: обработка события", "пользователь", upd.UserID, "ошибка", err)
		replies = []botcore.Reply{{Text: "Что-то пошло не так. Попробуй ещё раз."}}
	}
	if d := time.Since(started); d > 3*time.Second {
		a.log.Warn("вконтакте: медленная обработка", "пользователь", upd.UserID,
			"кнопка", upd.Callback, "текст", botcore.AuthText(upd.Text), "длительность", d.Round(time.Millisecond))
	}

	for _, r := range replies {
		a.send(ctx, peer, cmID, r)
	}
}

// firstName спрашивает имя ради приветствия и запоминает его.
func (a *Adapter) firstName(ctx context.Context, userID string) string {
	if v, ok := a.names.Load(userID); ok {
		return v.(string)
	}
	resp, err := a.vk.UsersGet(vkapi.Params{"user_ids": userID}.WithContext(ctx))
	if err != nil || len(resp) == 0 {
		// Без имени поздороваемся безлично — это не повод отказывать в ответе.
		a.log.Debug("вконтакте: не удалось узнать имя", "пользователь", userID, "ошибка", err)
		return ""
	}
	a.names.Store(userID, resp[0].FirstName)
	return resp[0].FirstName
}

// send отправляет один ответ.
func (a *Adapter) send(ctx context.Context, peer, cmID int, r botcore.Reply) {
	text := plain(r.Text)
	if text == "" {
		return
	}

	p := place(buttonRows(r.Keyboard), r.Menu, a.panelState(peer))
	text = clamp(compose(text, p))

	// Редактируем только то сообщение, из которого пришло нажатие, и только
	// когда всё нужное едет вместе с ним: нижняя панель к сообщению не
	// привязана, и правкой её не сменить.
	if r.Edit && cmID != 0 && p.panel == nil && a.edit(ctx, peer, cmID, text, p.inline) {
		a.sendMenu(ctx, peer, r.Menu)
		return
	}

	kb := p.inline
	if p.panel != nil {
		kb = p.panel
	}
	if err := a.post(ctx, peer, text, kb); err != nil {
		if leftChat(err) {
			a.log.Info("вконтакте: беседа закрыта для бота, ответ не отправлен", "диалог", peer, "ошибка", err)
			return
		}
		a.log.Error("вконтакте: отправка сообщения", "диалог", peer, "ошибка", err)
		return
	}
	if p.want != panelNone {
		a.panel.Store(peer, p.want)
		return
	}
	a.sendMenu(ctx, peer, r.Menu)
}

// compose дописывает к тексту всё, что человек должен знать о кнопках этого
// ответа: куда они уехали и сколько не поместилось.
func compose(text string, p placement) string {
	if p.want == panelPicker {
		text += panelHint
	}
	if p.dropped > 0 {
		text += fmt.Sprintf(tooManyButtons, p.dropped)
	}
	return text
}

// sendMenu показывает нижнее меню отдельным сообщением.
//
// Как и в телеграме, разметка у сообщения ровно одна, а расписание почти всегда
// идёт с кнопками навигации. Поэтому меню уходит отдельным коротким сообщением
// — и только когда панель занята чем-то другим.
//
// Reply.MenuSeen здесь сознательно не смотрим, хотя в телеграме он и убирает
// повтор после перезапуска. Разница в природе клавиатур: телеграмная живёт у
// клиента и никуда не девается, а панель ВКонтакте — общий слот диалога, и в
// нём вполне может висеть список институтов с прошлой сессии. Знания, что там
// сейчас, у нас после перезапуска нет, и вернуть меню важнее, чем сэкономить
// одно сообщение.
func (a *Adapter) sendMenu(ctx context.Context, peer int, menu *botcore.Menu) {
	if menu == nil || a.panelState(peer) == panelMenu {
		return
	}
	if err := a.post(ctx, peer, menuHint, menuKeyboard(menu)); err != nil {
		a.log.Debug("вконтакте: отправка меню", "диалог", peer, "ошибка", err)
		return
	}
	a.panel.Store(peer, panelMenu)
}

func (a *Adapter) panelState(peer int) string {
	if v, ok := a.panel.Load(peer); ok {
		return v.(string)
	}
	return panelNone
}

// edit правит уже отправленное сообщение. Возвращает, получилось ли:
// сообщение старше суток ВКонтакте править не даёт, и тогда шлём новое.
func (a *Adapter) edit(ctx context.Context, peer, cmID int, text string, kb *object.MessagesKeyboard) bool {
	markup := emptyInline
	if kb != nil {
		markup = kb.ToJSON()
	}
	_, err := a.vk.MessagesEdit(vkapi.Params{
		"peer_id":                 peer,
		"conversation_message_id": cmID,
		"message":                 text,
		"keyboard":                markup,
	}.WithContext(ctx))
	if err != nil {
		a.log.Debug("вконтакте: не удалось отредактировать сообщение, отправляю новое", "ошибка", err)
		return false
	}
	return true
}

func (a *Adapter) post(ctx context.Context, peer int, text string, kb *object.MessagesKeyboard) error {
	params := vkapi.Params{
		"peer_id": peer,
		"message": text,
		// random_id — защита ВКонтакте от дублей: одинаковые пары
		// (диалог, random_id) в пределах часа он схлопывает в одно сообщение.
		"random_id": rand.Int32(),
	}.WithContext(ctx)
	if kb != nil {
		params["keyboard"] = kb.ToJSON()
	}
	_, err := a.vk.MessagesSend(params)
	return err
}

// leftChat распознаёт отказ писать в беседу, из которой сообщество исключили.
//
// Кнопки под старыми сообщениями там остаются живыми, и нажатия продолжают
// приходить, а ответить некуда. Это не сбой, а следствие чужого решения.
func leftChat(err error) bool {
	if errors.Is(err, vkapi.ErrMessagesChatUserNoAccess) {
		return true
	}
	return errors.Is(err, vkapi.ErrPermission) &&
		strings.Contains(err.Error(), "kicked out of the conversation")
}

// Send отправляет сообщение по инициативе бота — нужно для утренней рассылки.
func (a *Adapter) Send(ctx context.Context, extID, text string, kb *botcore.Keyboard) error {
	peer, err := strconv.Atoi(extID)
	if err != nil {
		return err
	}
	// Клавиатура утреннего сообщения — навигация по дню, пять кнопок; в
	// инлайн-лимит она укладывается всегда. Если когда-нибудь перестанет,
	// сообщение важнее кнопок под ним.
	rows := buttonRows(kb)
	var markup *object.MessagesKeyboard
	if fitsInline(rows) {
		markup = keyboard(rows, true)
	}
	return a.post(ctx, peer, clamp(plain(text)), markup)
}

// Platform реализует botcore.Sender.
func (a *Adapter) Platform() string { return Platform }

// Retryable реализует botcore.Sender.
//
// Своего retry_after ВКонтакте не присылает — приходится назначать паузу
// самим. Обе ошибки означают «повтори позже», а не «не отправится никогда».
func (a *Adapter) Retryable(err error) (time.Duration, bool) {
	switch {
	case errors.Is(err, vkapi.ErrTooMany):
		return time.Second, true
	case errors.Is(err, vkapi.ErrServer):
		return 3 * time.Second, true
	}
	return 0, false
}

// Undeliverable реализует botcore.Sender: человек занёс сообщество в чёрный
// список, запретил ему писать или закрыл личные сообщения. Такого адресата
// надо не ретраить, а выключать ему рассылку.
func (a *Adapter) Undeliverable(err error) bool {
	return errors.Is(err, vkapi.ErrMessagesUserBlocked) ||
		errors.Is(err, vkapi.ErrMessagesDenySend) ||
		errors.Is(err, vkapi.ErrMessagesPrivacy)
}

// Reachable реализует botcore.Reacher: разрешает ли человек сообществу писать
// себе.
//
// Здесь, в отличие от телеграма, есть прямой вопрос ровно об этом —
// messages.isMessagesFromGroupAllowed. Человек ничего не замечает: ни
// сообщения, ни отметки «печатает».
func (a *Adapter) Reachable(ctx context.Context, extID string) (bool, error) {
	userID, err := strconv.Atoi(extID)
	if err != nil {
		return false, err
	}
	g, err := a.identify(ctx)
	if err != nil {
		return false, err
	}
	resp, err := a.vk.MessagesIsMessagesFromGroupAllowed(vkapi.Params{
		"group_id": g.id,
		"user_id":  userID,
	}.WithContext(ctx))
	if err != nil {
		// Запрет писать приезжает и ошибкой — например, когда страница
		// удалена или закрыта настройками приватности. Это ответ, а не сбой.
		if a.Undeliverable(err) {
			return false, nil
		}
		return false, err
	}
	return bool(resp.IsAllowed), nil
}
