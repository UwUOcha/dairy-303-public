package vk

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	vkapi "github.com/SevereCloud/vksdk/v3/api"
	"github.com/SevereCloud/vksdk/v3/events"
	"github.com/SevereCloud/vksdk/v3/object"

	"github.com/UwUOcha/dairy-303-public/internal/botcore"
)

// Доступ только для подписчиков сообщества.
//
// Прямолинейная реализация — groups.isMember перед каждым ответом — стоила бы
// лишнего запроса на каждое нажатие: это и десятки миллисекунд к задержке, и
// треть от лимита в 20 запросов в секунду, одного на все методы ключа
// сообщества.
//
// Поэтому бот держит список подписчиков целиком и отвечает по нему из памяти,
// не трогая сеть вовсе. Список приезжает полным обходом раз в сутки — это
// несколько запросов в день на весь бот, сколько бы человек им ни
// пользовалось, — а между обходами его правят события group_join и
// group_leave, приезжающие тем же длинным опросом бесплатно.
//
// Ровно так, а не наоборот: события — это ускорение, а не источник истины.
// Полагаться на них нельзя, и дыры тут не теоретические. Длинный опрос после
// любого обрыва поднимается с новым ts (см. poll), то есть события за время
// обрыва не доезжают; сервер и сам просит начать заново, отвечая failed 1 и 3;
// а рестарт botd при деплое обнуляет и память процесса. Полный обход — то, что
// закрывает всё это разом.

// cbSubscribed — callback кнопки «Я подписался». Единственный callback,
// который придумал адаптер, а не botcore: до сценариев он не доезжает.
const cbSubscribed = "vk:sub"

// Ритм обновления списка.
//
// Сутки — это потолок задержки ровно для одного сценария: человек отписался, а
// событие об этом до нас не дошло. Всё остальное — вступление, выход, бан —
// видно сразу, событием.
//
// Точнее и не надо. Отписка сразу после подписки — редкость, а тот, кто
// отписался ради того, чтобы через день упереться в ту же просьбу, скорее
// перестанет пробовать, чем найдёт в этом лазейку. Гнаться здесь за часом
// значило бы платить запросами за строгость, которая никому не нужна.
const (
	refreshInterval = 24 * time.Hour
	// refreshRetry — пауза после неудачного обхода. Пока список не загружен
	// ни разу, бот пускает всех, так что тянуть с повтором незачем.
	refreshRetry = time.Minute
	// membersPage — сколько идентификаторов ВКонтакте отдаёт за запрос.
	membersPage = 1000
)

// checkTimeout — потолок на проверку подписки по кнопке. Отдельный от
// handleTimeout: человек ждёт этот запрос впустую, и тянуть здесь двадцать
// секунд незачем.
const checkTimeout = 5 * time.Second

// group — сообщество, от имени которого работает бот.
type group struct {
	id     int
	name   string
	screen string
}

// registry — подписчики сообщества в памяти.
//
// Идентификаторы, а не структуры: десять тысяч подписчиков — это меньше
// мегабайта, и хранить о них больше нечего.
type registry struct {
	mu  sync.RWMutex
	set map[int]struct{}
	// loaded — список хотя бы раз загружен целиком. Пока нет, гейт открыт:
	// запирать всех из-за того, что ВКонтакте не ответил на служебный запрос,
	// куда хуже, чем пустить лишнего.
	loaded bool
	// build — набор, который прямо сейчас собирает обход.
	//
	// Обход идёт секунды, и события за это время должны попасть в оба набора:
	// человек, вступивший после того, как его страница уже проехала, иначе
	// потерялся бы до следующего часа.
	build map[int]struct{}
}

func (r *registry) has(id int) (member, known bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.loaded {
		return false, false
	}
	_, member = r.set[id]
	return member, true
}

func (r *registry) add(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.set == nil {
		r.set = make(map[int]struct{})
	}
	r.set[id] = struct{}{}
	if r.build != nil {
		r.build[id] = struct{}{}
	}
}

func (r *registry) remove(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.set, id)
	delete(r.build, id)
}

// startSync, addPage, finishSync и abortSync — один полный обход.
func (r *registry) startSync(size int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.build = make(map[int]struct{}, size)
}

func (r *registry) addPage(ids []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		r.build[id] = struct{}{}
	}
}

func (r *registry) finishSync() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set, r.build = r.build, nil
	r.loaded = true
	return len(r.set)
}

// abortSync выбрасывает недособранный набор: половина списка хуже прошлого
// полного, а на первом обходе — хуже открытого гейта.
func (r *registry) abortSync() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.build = nil
}

// identify узнаёт, каким сообществом мы работаем, и запоминает ответ.
//
// Лениво, а не один раз при старте: если ВКонтакте был недоступен в момент
// запуска, идентификатор сообщества остался бы неизвестен до перезапуска
// процесса — и проверка подписки молча не работала бы весь этот срок.
func (a *Adapter) identify(ctx context.Context) (group, error) {
	a.commMu.Lock()
	defer a.commMu.Unlock()

	if a.comm.id != 0 {
		return a.comm, nil
	}
	resp, err := a.vk.GroupsGetByID(vkapi.Params{}.WithContext(ctx))
	if err != nil {
		return group{}, err
	}
	if len(resp.Groups) == 0 {
		return group{}, errors.New("ключ доступа не принадлежит ни одному сообществу")
	}
	g := resp.Groups[0]
	a.comm = group{id: g.ID, name: g.Name, screen: g.ScreenName}
	return a.comm, nil
}

// subscribed решает, пускать ли человека в сценарии. Только память, без сети.
func (a *Adapter) subscribed(userID string) bool {
	if !a.requireSub {
		return true
	}
	id, err := strconv.Atoi(userID)
	if err != nil {
		return true
	}
	member, known := a.members.has(id)
	return member || !known
}

// keepMembers держит список подписчиков свежим до конца работы бота.
func (a *Adapter) keepMembers(ctx context.Context) {
	for ctx.Err() == nil {
		wait := refreshInterval
		if err := a.refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("вконтакте: не удалось обновить список подписчиков",
				"ошибка", err, "повтор_через", refreshRetry)
			wait = refreshRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// refresh загружает список подписчиков целиком.
func (a *Adapter) refresh(ctx context.Context) error {
	g, err := a.identify(ctx)
	if err != nil {
		return err
	}

	started := time.Now()
	a.members.startSync(0)

	for offset := 0; ; offset += membersPage {
		resp, err := a.vk.GroupsGetMembers(vkapi.Params{
			"group_id": g.id,
			"offset":   offset,
			"count":    membersPage,
			// Порядок фиксируем, иначе страницы могут разъехаться и часть
			// подписчиков не попадёт в обход вовсе.
			"sort": "id_asc",
		}.WithContext(ctx))
		if err != nil {
			a.members.abortSync()
			return err
		}
		a.members.addPage(resp.Items)
		if len(resp.Items) == 0 || offset+len(resp.Items) >= resp.Count {
			break
		}
	}

	a.log.Info("вконтакте: список подписчиков обновлён",
		"подписчиков", a.members.finishSync(), "заняло", time.Since(started).Round(time.Millisecond))
	return nil
}

// onJoin и onLeave правят список между обходами.
//
// Включать эти типы событий в настройках сообщества руками не нужно: их
// включает сам botd при запуске длинного опроса — по тем обработчикам, что
// здесь зарегистрированы. Для этого и требуется право «Управление
// сообществом», о котором говорит diagnose.
func (a *Adapter) onJoin(_ context.Context, obj events.GroupJoinObject) {
	// «request» — это заявка в закрытое сообщество, а не вступление в него.
	if obj.JoinType == "request" {
		return
	}
	a.log.Debug("вконтакте: подписался", "пользователь", obj.UserID, "тип", obj.JoinType)
	a.members.add(obj.UserID)
}

func (a *Adapter) onLeave(_ context.Context, obj events.GroupLeaveObject) {
	a.log.Debug("вконтакте: отписался", "пользователь", obj.UserID)
	a.members.remove(obj.UserID)
}

// confirm обрабатывает нажатие «Я подписался».
//
// Единственное место во всём адаптере, где подписка проверяется запросом.
// Ждать здесь ближайшего обхода нельзя: человек только что сделал ровно то,
// о чём его попросили, и час до следующего часа он не простоит.
func (a *Adapter) confirm(ctx context.Context, userID string, peer int) {
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	if !a.checkMember(cctx, userID) {
		a.log.Info("вконтакте: подписка не подтвердилась", "пользователь", userID)
		a.askSubscribe(ctx, peer, notSubscribedText)
		return
	}
	a.log.Info("вконтакте: подписка подтверждена", "пользователь", userID)
	// Дальше — обычный первый экран бота: человек только что прошёл входную
	// дверь, и показать ему надо то же, что показали бы на «Начать».
	a.process(ctx, botcore.Update{
		Platform: Platform,
		UserID:   userID,
		Text:     "/start",
	}, peer, 0)
}

// checkMember спрашивает ВКонтакте про одного человека и запоминает «да».
//
// Неполадка на нашей стороне трактуется в пользу человека — как и незагруженный
// список: подписка здесь не про безопасность.
func (a *Adapter) checkMember(ctx context.Context, userID string) bool {
	g, err := a.identify(ctx)
	if err != nil {
		a.log.Warn("вконтакте: не знаю id сообщества, пропускаю проверку подписки", "ошибка", err)
		return true
	}
	resp, err := a.vk.GroupsIsMember(vkapi.Params{
		"group_id": g.id,
		"user_id":  userID,
	}.WithContext(ctx))
	if err != nil {
		a.log.Warn("вконтакте: не удалось проверить подписку",
			"пользователь", userID, "ошибка", err)
		return true
	}
	if resp != 1 {
		return false
	}
	if id, err := strconv.Atoi(userID); err == nil {
		a.members.add(id)
	}
	return true
}

// askSubscribe объясняет, почему бот молчит, и даёт оба способа это исправить.
func (a *Adapter) askSubscribe(ctx context.Context, peer int, lead string) {
	g, err := a.identify(ctx)
	if err != nil {
		// Сюда попадаем, только если сообщество не опозналось ни разу, — тогда
		// и ссылку дать не из чего.
		a.log.Warn("вконтакте: некуда звать подписываться", "ошибка", err)
	}
	if err := a.post(ctx, peer, subscribeText(g, lead), subscribeKeyboard(g)); err != nil {
		a.log.Error("вконтакте: просьба подписаться", "диалог", peer, "ошибка", err)
	}
}

// subscribeText дописывает к просьбе адрес сообщества.
//
// Адрес нужен и текстом, а не только кнопкой: кнопки-ссылки не увидит клиент,
// который их не умеет, — а таким людям бот и так отвечает отдельной подсказкой
// с командами.
func subscribeText(g group, lead string) string {
	if g.screen == "" {
		return lead
	}
	return lead + "\n\nСообщество: vk.com/" + g.screen
}

// subscribeKeyboard — ссылка на сообщество и кнопка проверки.
//
// Ссылка отдельной кнопкой, а не только в тексте: у неподписанного человека
// диалог с ботом открыт поверх всего, и путь «выйти, найти сообщество,
// подписаться, вернуться» он проходить не станет.
func subscribeKeyboard(g group) *object.MessagesKeyboard {
	kb := object.NewMessagesKeyboard(false)
	kb.Inline = true
	if g.screen != "" {
		kb.AddRow()
		kb.AddOpenLinkButton("https://vk.com/"+g.screen, openLabel, payload{})
	}
	kb.AddRow()
	kb.AddCallbackButton(checkLabel, payload{CB: cbSubscribed}, object.Primary)
	return kb
}
