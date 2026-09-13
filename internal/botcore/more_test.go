package botcore

import (
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// testAuthor — автор для тестов: сам сидит в телеграме под id «777».
var testAuthor = Author{TG: "777", LinkTG: "t.me/example_bot", LinkVK: "vk.ru/example_bot"}

func testBotWithAuthor(t *testing.T) (*Bot, *fakeRasp) {
	t.Helper()
	client, state := newFakeRaspWithState(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(client, 180, log, WithAuthor(testAuthor)), state
}

// settle доводит человека до настроенного состояния: без группы нижнего меню
// ему не показывают, а половина проверок здесь как раз про меню.
func settle(t *testing.T, b *Bot, id string) {
	t.Helper()
	handle(t, b, Update{Platform: "tg", UserID: id, Text: "гр12"})
	handle(t, b, Update{Platform: "tg", UserID: id, Callback: "s:334"})
}

// В нижней панели вместо настроек стоит «Ещё», а сами настройки лежат внутри.
func TestMenuHasMore(t *testing.T) {
	b, _ := testBotWithAuthor(t)
	settle(t, b, "1")

	rs := handle(t, b, Update{Platform: "tg", UserID: "1", Text: MenuMore})
	if !strings.Contains(firstText(rs), "Ещё") {
		t.Fatalf("кнопка «Ещё» не открыла экран: %q", firstText(rs))
	}
	if !strings.Contains(firstText(rs), "Автора") {
		t.Errorf("на экране «Ещё» нет марки: %q", firstText(rs))
	}
	if !hasLabel(rs[0].Keyboard, "Настройки") {
		t.Error("настройки должны открываться из «Ещё»")
	}

	// Панель раздаётся с новой кнопкой, а старой в ней быть не должно.
	var panel []string
	for _, row := range MainMenu().Rows {
		panel = append(panel, row...)
	}
	if !slices.Contains(panel, MenuMore) || slices.Contains(panel, MenuSettings) {
		t.Errorf("нижняя панель = %v", panel)
	}
}

// Кнопка «Настройки» осталась в панелях, розданных прошлой версией: пока
// клиент не получил новую клавиатуру, она обязана работать.
func TestOldSettingsButtonStillWorks(t *testing.T) {
	b, _ := testBotWithAuthor(t)
	settle(t, b, "1")

	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Text: MenuSettings}))
	if !strings.Contains(got, "Настройки") {
		t.Fatalf("старая кнопка панели перестала открывать настройки: %q", got)
	}
}

// Выход из настроек ведёт в «Ещё», а не закрывает навигацию совсем.
func TestSettingsGoBackToMore(t *testing.T) {
	b, _ := testBotWithAuthor(t)
	settle(t, b, "1")

	rs := handle(t, b, Update{Platform: "tg", UserID: "1", Callback: "set"})
	if !hasCallback(rs[0].Keyboard, cbMore) {
		t.Fatal("из настроек некуда вернуться")
	}
}

// Полный путь обращения: кнопка — текст — очередь автору.
func TestFeedbackFlow(t *testing.T) {
	b, state := testBotWithAuthor(t)
	settle(t, b, "1")

	if got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbFeedback})); !strings.Contains(got, "автору") {
		t.Fatalf("приглашение написать: %q", got)
	}

	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Text: "не приходит утро"}))
	if !strings.Contains(got, "Отправлено") {
		t.Fatalf("обращение не принято: %q", got)
	}
	if len(state.feedback) != 1 {
		t.Fatalf("обращений в базе: %d", len(state.feedback))
	}
	fb := state.feedback[1]
	if fb.Text != "не приходит утро" || fb.GroupName != "ГР-12" {
		t.Errorf("обращение сохранено как %+v", fb)
	}

	// Сообщение автору ставится в очередь: доставляет её botd, и она же даёт
	// ретраи, если автор в этот момент недоступен.
	if len(state.outbox) != 1 {
		t.Fatalf("в очереди %d сообщений, ожидалось одно", len(state.outbox))
	}
	out := state.outbox[0]
	if out.Platform != "tg" || out.ExtID != testAuthor.TG || out.Kind != store.OutboxFeedback {
		t.Errorf("сообщение уехало не автору: %+v", out)
	}

	// Ожидание снято: следующее сообщение — это снова поиск группы, а не
	// второе обращение.
	if u := state.users[key("tg", "1")]; u.Await != store.AwaitNothing {
		t.Errorf("после отправки бот всё ещё ждёт текст: %q", u.Await)
	}
}

// Обращение от того, кто ещё не выбрал группу, принимается: «не могу найти
// свою группу» — самая ценная жалоба, и приходит она именно от таких.
func TestFeedbackWithoutGroup(t *testing.T) {
	b, state := testBotWithAuthor(t)
	handle(t, b, Update{Platform: "tg", UserID: "5", Text: "/start"})
	handle(t, b, Update{Platform: "tg", UserID: "5", Callback: cbFeedback})

	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "5", Text: "не нахожу свою группу"}))
	if !strings.Contains(got, "Отправлено") {
		t.Fatalf("обращение без группы отвергнуто: %q", got)
	}
	if state.feedback[1].GroupName != "" {
		t.Errorf("группа взялась ниоткуда: %q", state.feedback[1].GroupName)
	}
}

// Слишком частые обращения отклоняются, но ожидание остаётся: человек должен
// иметь возможность повторить тем же сообщением, не нажимая кнопку заново.
func TestFeedbackTooOften(t *testing.T) {
	b, state := testBotWithAuthor(t)
	settle(t, b, "1")
	state.tooOften = 3 * time.Minute

	handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbFeedback})
	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Text: "ещё раз"}))
	if !strings.Contains(got, "3 минуты") {
		t.Fatalf("отказ по частоте не объясняет паузу: %q", got)
	}
	if len(state.outbox) != 0 {
		t.Errorf("отклонённое обращение всё равно уехало автору: %+v", state.outbox)
	}
	if u := state.users[key("tg", "1")]; u.Await != store.AwaitFeedback {
		t.Errorf("после отказа ожидание снялось: %q", u.Await)
	}
}

// Кнопка нижнего меню выводит из ожидания: застрять в диалоге, из которого не
// выйти, человек не должен.
func TestFeedbackReleasedByMenuButton(t *testing.T) {
	b, state := testBotWithAuthor(t)
	settle(t, b, "1")
	handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbFeedback})

	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Text: MenuToday}))
	if !strings.Contains(got, "ГР-12") {
		t.Fatalf("кнопка расписания не вывела из ожидания: %q", got)
	}
	if len(state.feedback) != 0 {
		t.Error("нажатие кнопки уехало автору как обращение")
	}
}

// Автор не задан — кнопки нет вовсе: писать в пустоту хуже, чем не предлагать.
func TestFeedbackHiddenWithoutAuthor(t *testing.T) {
	b := testBot(t)
	settle(t, b, "1")

	rs := handle(t, b, Update{Platform: "tg", UserID: "1", Text: MenuMore})
	if hasLabel(rs[0].Keyboard, "Написать автору") {
		t.Error("кнопка связи с автором показана, хотя адресат не задан")
	}
	// Кнопка из старой переписки не должна оставлять человека в ожидании.
	handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbFeedback})
	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Text: "гр12"}))
	if strings.Contains(got, "Отправлено") {
		t.Errorf("обращение принято, хотя автора нет: %q", got)
	}
}

// Ответ автора доезжает до того, кто писал, и на его площадку.
func TestAnswerFlow(t *testing.T) {
	b, state := testBotWithAuthor(t)
	// Пишет человек из ВКонтакте, автор сидит в телеграме.
	handle(t, b, Update{Platform: "vk", UserID: "9", Text: "гр12"})
	handle(t, b, Update{Platform: "vk", UserID: "9", Callback: "s:334"})
	handle(t, b, Update{Platform: "vk", UserID: "9", Callback: cbFeedback})
	handle(t, b, Update{Platform: "vk", UserID: "9", Text: "пары не те"})
	state.outbox = nil

	author := Update{Platform: "tg", UserID: testAuthor.TG}
	if got := firstText(handle(t, b, Update{Platform: author.Platform, UserID: author.UserID, Callback: "ans:1"})); !strings.Contains(got, "Ответ на №1") {
		t.Fatalf("автору не предложили ответить: %q", got)
	}
	got := firstText(handle(t, b, Update{Platform: author.Platform, UserID: author.UserID, Text: "починил, проверь"}))
	if !strings.Contains(got, "отправлен") {
		t.Fatalf("ответ не отправлен: %q", got)
	}

	if len(state.outbox) != 1 {
		t.Fatalf("в очереди %d сообщений, ожидалось одно", len(state.outbox))
	}
	out := state.outbox[0]
	if out.Platform != "vk" || out.ExtID != "9" || out.Kind != store.OutboxAnswer {
		t.Fatalf("ответ уехал не туда: %+v", out)
	}
	var p store.AnswerPayload
	if err := json.Unmarshal([]byte(out.Payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.Text != "починил, проверь" {
		t.Errorf("текст ответа = %q", p.Text)
	}
	if !state.feedback[1].Answered() {
		t.Error("обращение не отмечено отвеченным")
	}
}

// Кнопка «Ответить» работает только у автора: она приходит в его переписку, но
// подделать нажатие ничего не должно стоить.
func TestAnswerOnlyForAuthor(t *testing.T) {
	b, state := testBotWithAuthor(t)
	settle(t, b, "1")
	handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbFeedback})
	handle(t, b, Update{Platform: "tg", UserID: "1", Text: "жалоба"})
	state.outbox = nil

	handle(t, b, Update{Platform: "tg", UserID: "1", Callback: "ans:1"})
	handle(t, b, Update{Platform: "tg", UserID: "1", Text: "я тут за автора"})
	if len(state.outbox) != 0 {
		t.Fatalf("посторонний ответил от имени автора: %+v", state.outbox)
	}
}

// Разметка в тексте не должна ломать отправку: сообщение верстается
// телеграмным HTML, и «<3» в жалобе — обычное дело.
func TestFeedbackTextEscaped(t *testing.T) {
	b, _ := testBotWithAuthor(t)
	text, _ := b.FeedbackMessage(store.FeedbackPayload{
		ID: 7, Platform: "tg", GroupName: "ГР-12", Text: "пара <b>не та</b>",
	})
	if strings.Contains(text, "<b>не та</b>") {
		t.Errorf("разметка из обращения уехала как есть: %q", text)
	}
	if !strings.Contains(text, "&lt;b&gt;") {
		t.Errorf("текст обращения не экранирован: %q", text)
	}
	answer, kb := b.AnswerMessage(store.AnswerPayload{ID: 7, Text: "ответ <3"})
	if !strings.Contains(answer, "&lt;3") {
		t.Errorf("текст ответа не экранирован: %q", answer)
	}
	// Под ответом — кнопка продолжить разговор: именно она превращает
	// обратную связь в переписку, а не в ящик для жалоб.
	if !hasCallback(kb, cbFeedback) {
		t.Error("под ответом автора нет кнопки ответить")
	}
}

// Счётчик пользователей появляется только начиная с порога: «им пользуются
// семь студентов» работает против того, ради чего строка заведена.
func TestAboutCounter(t *testing.T) {
	b, state := testBotWithAuthor(t)
	settle(t, b, "1")

	state.count = aboutUsersFloor - 1
	if got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbAbout})); strings.Contains(got, "пользуются") {
		t.Errorf("счётчик показан ниже порога: %q", got)
	}

	state.count = 1240
	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbAbout}))
	if !strings.Contains(got, "1 240 студентов") {
		t.Errorf("счётчик не показан или без разрядов: %q", got)
	}
	if !strings.Contains(got, "Автор") {
		t.Errorf("в рассказе о боте нет подписи: %q", got)
	}
}

// «Поделиться» отдаёт ссылку той площадки, с которой её собрались пересылать.
func TestShareLinkPerPlatform(t *testing.T) {
	b, _ := testBotWithAuthor(t)
	settle(t, b, "1")
	handle(t, b, Update{Platform: "vk", UserID: "9", Text: "гр12"})
	handle(t, b, Update{Platform: "vk", UserID: "9", Callback: "s:334"})

	tg := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cbShare}))
	if !strings.Contains(tg, testAuthor.LinkTG) {
		t.Errorf("телеграму отдали не его ссылку: %q", tg)
	}
	vk := firstText(handle(t, b, Update{Platform: "vk", UserID: "9", Callback: cbShare}))
	if !strings.Contains(vk, testAuthor.LinkVK) {
		t.Errorf("ВКонтакте отдали не его ссылку: %q", vk)
	}
}

// hasLabel ищет кнопку по подписи — в отличие от hasButton, который ищет по
// callback-данным.
func hasLabel(kb *Keyboard, label string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.Rows {
		for _, btn := range row {
			if strings.Contains(btn.Label, label) {
				return true
			}
		}
	}
	return false
}

func hasCallback(kb *Keyboard, data string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.Rows {
		for _, btn := range row {
			if btn.Data == data {
				return true
			}
		}
	}
	return false
}

func TestAnswerCanRetry(t *testing.T) {
	for _, input := range []string{"   ", "исправлено"} {
		t.Run(input, func(t *testing.T) {
			b, state := testBotWithAuthor(t)
			state.feedback[1] = store.Feedback{ID: 1, Platform: "vk", ExtID: "9"}
			handle(t, b, Update{Platform: "tg", UserID: testAuthor.TG, Callback: "ans:1"})
			state.outboxUnavailable = true
			handle(t, b, Update{Platform: "tg", UserID: testAuthor.TG, Text: input})
			if state.users[key("tg", testAuthor.TG)].Await != store.AwaitAnswer+":1" {
				t.Fatal("ожидание ответа потеряно до успешной отправки")
			}
			state.outboxUnavailable = false
			handle(t, b, Update{Platform: "tg", UserID: testAuthor.TG, Text: "повтор ответа"})
			if len(state.outbox) != 1 || state.users[key("tg", testAuthor.TG)].Await != "" {
				t.Fatal("повтор не доставлен или ожидание не сброшено")
			}
		})
	}
}

func TestFormerAuthorCannotAnswer(t *testing.T) {
	b, state := testBotWithAuthor(t)
	state.feedback[1] = store.Feedback{ID: 1, Platform: "vk", ExtID: "9"}
	handle(t, b, Update{Platform: "tg", UserID: testAuthor.TG, Callback: "ans:1"})
	b.author = Author{TG: "another-author"}
	handle(t, b, Update{Platform: "tg", UserID: testAuthor.TG, Text: "ответ"})
	if len(state.outbox) != 0 {
		t.Fatal("бывший автор отправил ответ")
	}
}
