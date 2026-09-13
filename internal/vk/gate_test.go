package vk

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/SevereCloud/vksdk/v3/events"
	"github.com/SevereCloud/vksdk/v3/object"
)

// Гейт обязан отвечать из памяти: сеть здесь не подключена, и любой поход в
// неё повесил бы тест на таймауте vksdk. В этом вся суть — на пути человека
// сетевых вызовов нет.
func TestSubscribedAnswersFromMemory(t *testing.T) {
	a := testAdapter(true)
	a.members.startSync(0)
	a.members.addPage([]int{42})
	a.members.finishSync()

	if !a.subscribed("42") {
		t.Error("подписчик не пущен, хотя он есть в списке")
	}
	if a.subscribed("43") {
		t.Error("посторонний пущен в сценарии")
	}
}

// Пока список не загружен ни разу, бот работает для всех: запереть площадку
// из-за того, что ВКонтакте не ответил на служебный запрос, — худший исход.
func TestUnknownListLetsEveryoneIn(t *testing.T) {
	a := testAdapter(true)

	if !a.subscribed("42") {
		t.Error("список не загружен, а человека уже не пускают")
	}
}

// Выключенная проверка не должна стоить ни одного запроса — иначе отладка в
// тестовом сообществе упиралась бы в те же лимиты, что и бой.
func TestSubscriptionCheckOff(t *testing.T) {
	a := testAdapter(false)

	if !a.subscribed("42") {
		t.Error("проверка выключена, а человек не пущен")
	}
}

// События правят список между обходами — это вся их роль.
func TestJoinLeaveUpdateRegistry(t *testing.T) {
	a := testAdapter(true)
	ctx := context.Background()
	a.members.startSync(0)
	a.members.finishSync()

	a.onJoin(ctx, events.GroupJoinObject{UserID: 42, JoinType: "join"})
	if !a.subscribed("42") {
		t.Error("вступление в сообщество не попало в список")
	}

	a.onLeave(ctx, events.GroupLeaveObject{UserID: 42})
	if a.subscribed("42") {
		t.Error("после выхода из сообщества человек всё ещё считается подписчиком")
	}

	// Заявка в закрытое сообщество — ещё не членство.
	a.onJoin(ctx, events.GroupJoinObject{UserID: 43, JoinType: "request"})
	if a.subscribed("43") {
		t.Error("заявка на вступление принята за подписку")
	}
}

// Полный обход перезаписывает список целиком — иначе отписавшийся, о котором
// событие не доехало, остался бы подписчиком навсегда.
func TestRefreshDropsGoneMembers(t *testing.T) {
	a := testAdapter(true)
	a.members.startSync(0)
	a.members.addPage([]int{42, 43})
	a.members.finishSync()

	a.members.startSync(0)
	a.members.addPage([]int{43})
	a.members.finishSync()

	if a.subscribed("42") {
		t.Error("выбывший подписчик пережил полный обход")
	}
	if !a.subscribed("43") {
		t.Error("оставшийся подписчик потерялся при обходе")
	}
}

// Обход идёт секунды, и события за это время должны попасть в собираемый
// набор тоже: иначе вступивший в середине обхода потерялся бы до следующего.
func TestEventsDuringRefresh(t *testing.T) {
	a := testAdapter(true)
	ctx := context.Background()

	a.members.startSync(0)
	a.members.addPage([]int{42})
	a.onJoin(ctx, events.GroupJoinObject{UserID: 43, JoinType: "join"})
	a.onLeave(ctx, events.GroupLeaveObject{UserID: 42})
	a.members.finishSync()

	if !a.subscribed("43") {
		t.Error("вступивший во время обхода потерялся")
	}
	if a.subscribed("42") {
		t.Error("вышедший во время обхода остался в списке")
	}
}

// Недособранный список хуже прошлого полного: половина подписчиков упёрлась бы
// в просьбу подписаться на сообщество, в котором и так состоит.
func TestAbortedRefreshKeepsOldList(t *testing.T) {
	a := testAdapter(true)
	a.members.startSync(0)
	a.members.addPage([]int{42})
	a.members.finishSync()

	a.members.startSync(0)
	a.members.addPage([]int{99})
	a.members.abortSync()

	if !a.subscribed("42") {
		t.Error("оборванный обход снёс прежний список")
	}
	if a.subscribed("99") {
		t.Error("недособранный список пошёл в дело")
	}
}

// Кнопка «Я подписался» опознаётся в onEvent по payload. Разорвётся эта пара —
// и человек, выполнивший просьбу, останется стучаться в закрытую дверь.
func TestSubscribeButtonRoundTrip(t *testing.T) {
	kb := subscribeKeyboard(group{id: 1, screen: "example_university"})

	var parsed struct {
		Buttons [][]struct {
			Action struct {
				Type    string `json:"type"`
				Link    string `json:"link"`
				Payload string `json:"payload"`
			} `json:"action"`
		} `json:"buttons"`
	}
	if err := json.Unmarshal([]byte(kb.ToJSON()), &parsed); err != nil {
		t.Fatalf("клавиатура не разбирается: %v", err)
	}

	var gotLink, gotCB string
	for _, row := range parsed.Buttons {
		for _, b := range row {
			switch b.Action.Type {
			case "open_link":
				gotLink = b.Action.Link
			case "callback":
				cb, _ := parsePayload([]byte(b.Action.Payload))
				gotCB = cb
			}
		}
	}

	if gotLink != "https://vk.com/example_university" {
		t.Errorf("ссылка на сообщество = %q, ожидалась https://vk.com/example_university", gotLink)
	}
	if gotCB != cbSubscribed {
		t.Errorf("callback кнопки = %q, ожидался %q", gotCB, cbSubscribed)
	}
}

// Без адреса сообщества ссылку дать не из чего, но кнопка проверки нужна всё
// равно: без неё экран становится тупиком.
func TestSubscribeKeyboardWithoutScreenName(t *testing.T) {
	kb := subscribeKeyboard(group{id: 1})

	if n := len(kb.Buttons); n != 1 {
		t.Fatalf("строк в клавиатуре %d, ожидалась одна — только кнопка проверки", n)
	}
	if got := kb.Buttons[0][0].Action.Type; got != object.ButtonCallback {
		t.Errorf("тип кнопки %q, ожидался callback", got)
	}
	if text := subscribeText(group{id: 1}, subscribeLead); text != subscribeLead {
		t.Errorf("в текст без адреса сообщества что-то дописалось: %q", text)
	}
}

func TestSubscribeTextCarriesAddress(t *testing.T) {
	text := subscribeText(group{id: 1, screen: "example_university"}, subscribeLead)

	if !strings.HasPrefix(text, subscribeLead) {
		t.Error("просьба подписаться потерялась")
	}
	if !strings.Contains(text, "vk.com/example_university") {
		t.Errorf("в тексте нет адреса сообщества: %q", text)
	}
}

func testAdapter(requireSub bool) *Adapter {
	return &Adapter{
		requireSub: requireSub,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}
