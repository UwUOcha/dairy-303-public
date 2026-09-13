package tg

import (
	"context"
	"encoding/json"
	tgbot "github.com/go-telegram/bot"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/UwUOcha/dairy-303-public/internal/botcore"
)

// Адаптер обязан оставаться и отправителем, и тем, кого можно спросить о
// живости адресата: обе роли он играет в botd.
var _ botcore.Reacher = (*Adapter)(nil)

func TestTelegramTextKeepsShortHTML(t *testing.T) {
	in := "<b>Расписание</b> &amp; аудитория"
	got, mode := telegramText(in)
	if got != in || mode != models.ParseModeHTML {
		t.Errorf("короткое сообщение изменено: text=%q mode=%q", got, mode)
	}
}

func TestTelegramTextShortensLongHTMLSafely(t *testing.T) {
	in := "<b>Расписание &amp; детали</b>\n" + strings.Repeat("я", maxMessageRunes+500)
	got, mode := telegramText(in)
	if mode != "" {
		t.Errorf("сокращённый текст отправится как HTML: mode=%q", mode)
	}
	if utf8.RuneCountInString(got) > maxMessageRunes {
		t.Errorf("длина после сокращения = %d", utf8.RuneCountInString(got))
	}
	if strings.Contains(got, "<b>") || !strings.Contains(got, "Расписание & детали") {
		t.Errorf("HTML снят неверно: %.80q", got)
	}
	if !strings.HasSuffix(got, shortenedMessage) {
		t.Errorf("нет пометки о сокращении: %q", got[len(got)-40:])
	}
}

// Нажатие кнопки обязано вернуть queryID при любом раскладе.
//
// Пока бот не ответит на callback_query, телеграм крутит спиннер на кнопке и
// сдаётся молча, без ошибки. Раньше апдейт с «недоступным» сообщением уходил
// из convert с пустым queryID и никогда не получал ответа — снаружи это
// выглядело как вечно грузящаяся кнопка.
func TestConvertAlwaysReturnsQueryID(t *testing.T) {
	from := models.User{ID: 42, FirstName: "Оча"}

	tests := []struct {
		name          string
		message       models.MaybeInaccessibleMessage
		wantChatID    int64
		wantMessageID int
		wantOK        bool
	}{
		{
			name: "обычное сообщение — можно редактировать",
			message: models.MaybeInaccessibleMessage{
				Type:    models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{ID: 7, Chat: models.Chat{ID: 100}},
			},
			wantChatID: 100, wantMessageID: 7, wantOK: true,
		},
		{
			// Сообщение слишком старое: править нельзя, но чат известен —
			// значит, ответить новым сообщением мы всё ещё можем.
			name: "недоступное сообщение — отвечаем новым",
			message: models.MaybeInaccessibleMessage{
				Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
				InaccessibleMessage: &models.InaccessibleMessage{MessageID: 7, Chat: models.Chat{ID: 100}},
			},
			wantChatID: 100, wantMessageID: 0, wantOK: true,
		},
		{
			name:    "сообщения нет вовсе",
			message: models.MaybeInaccessibleMessage{},
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upd, chatID, messageID, queryID, ok := convert(&models.Update{
				CallbackQuery: &models.CallbackQuery{
					ID: "q1", From: from, Data: "br", Message: tt.message,
				},
			})
			if queryID != "q1" {
				t.Errorf("queryID = %q, ожидалось q1 — иначе кнопка зависнет", queryID)
			}
			if ok != tt.wantOK {
				t.Errorf("ok = %v, ожидалось %v", ok, tt.wantOK)
			}
			if chatID != tt.wantChatID {
				t.Errorf("chatID = %d, ожидалось %d", chatID, tt.wantChatID)
			}
			if messageID != tt.wantMessageID {
				t.Errorf("messageID = %d, ожидалось %d", messageID, tt.wantMessageID)
			}
			if tt.wantOK {
				if upd.Callback != "br" || upd.UserID != "42" || upd.Platform != Platform {
					t.Errorf("апдейт разобран неверно: %+v", upd)
				}
			}
		})
	}
}

func TestConvertMessage(t *testing.T) {
	upd, chatID, messageID, queryID, ok := convert(&models.Update{
		Message: &models.Message{
			ID:   9,
			Chat: models.Chat{ID: 100},
			Text: "гр12",
			From: &models.User{ID: 42, FirstName: "Оча"},
		},
	})
	if !ok {
		t.Fatal("обычное сообщение не разобрано")
	}
	if upd.Text != "гр12" || upd.UserID != "42" {
		t.Errorf("апдейт = %+v", upd)
	}
	if chatID != 100 {
		t.Errorf("chatID = %d, ожидалось 100", chatID)
	}
	// Сообщение — не нажатие кнопки: редактировать нечего и отвечать некому.
	if messageID != 0 || queryID != "" {
		t.Errorf("messageID = %d, queryID = %q, ожидались нули", messageID, queryID)
	}
}

func TestConvertIgnoresUnsupportedUpdates(t *testing.T) {
	if _, _, _, _, ok := convert(&models.Update{}); ok {
		t.Error("пустой апдейт не должен разбираться")
	}
	// Сообщение без отправителя (например, из канала) обрабатывать нечем.
	if _, _, _, _, ok := convert(&models.Update{Message: &models.Message{ID: 1}}); ok {
		t.Error("сообщение без отправителя не должно разбираться")
	}
}

func TestIsNotModified(t *testing.T) {
	if isNotModified(nil) {
		t.Error("nil — не ошибка редактирования")
	}
	if !isNotModified(errString("Bad Request: message is not modified")) {
		t.Error("ответ телеграма о неизменённом сообщении не распознан")
	}
	if isNotModified(errString("Bad Request: chat not found")) {
		t.Error("посторонняя ошибка принята за неизменённое сообщение")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// Проверяем реальные запросы адаптера: обновление меню не зависит от того,
// удалось ли изменить текст расписания, а отказ доставки нельзя подтверждать.
func TestMenuDelivery(t *testing.T) {
	for _, tc := range []struct {
		name                                                   string
		edit, unchanged, seen, attached, failMenu, failMessage bool
		wantDelivered                                          bool
		wantMenus                                              int
	}{
		{name: "новая версия под расписанием", wantDelivered: true, wantMenus: 1},
		{name: "та же версия", seen: true},
		{name: "редактирование", edit: true, wantDelivered: true, wantMenus: 1},
		{name: "текст не изменился", edit: true, unchanged: true, wantDelivered: true, wantMenus: 1},
		{name: "ошибка меню", failMenu: true, wantMenus: 1},
		{name: "ошибка расписания", failMessage: true},
		{name: "меню в обычном сообщении", attached: true, wantDelivered: true, wantMenus: 1},
		{name: "ошибка обычного сообщения", attached: true, failMenu: true, wantMenus: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			menus := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				}
				defer r.MultipartForm.RemoveAll()
				markup := r.FormValue("reply_markup")
				var kb struct {
					Keyboard [][]models.KeyboardButton `json:"keyboard"`
				}
				if markup != "" {
					if err := json.Unmarshal([]byte(markup), &kb); err != nil {
						t.Error(err)
					}
				}
				isMenu := len(kb.Keyboard) > 0
				if isMenu {
					menus++
					if kb.Keyboard[0][1].Text != botcore.MenuTomorrow {
						t.Error("старая подпись", markup)
					}
				}
				if strings.HasSuffix(r.URL.Path, "editMessageText") && tc.unchanged {
					io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`)
				} else if isMenu && tc.failMenu || !isMenu && tc.failMessage {
					io.WriteString(w, `{"ok":false,"error_code":500,"description":"delivery failed"}`)
				} else {
					io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":42,"type":"private"},"date":1}}`)
				}
			}))
			defer srv.Close()
			bot, err := tgbot.New("test", tgbot.WithSkipGetMe(), tgbot.WithServerURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			a := &Adapter{bot: bot, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			reply := botcore.Reply{Text: "Расписание", Menu: botcore.MainMenu(), MenuSeen: tc.seen, Edit: tc.edit}
			if !tc.attached {
				reply.Keyboard = &botcore.Keyboard{Rows: [][]botcore.Button{{{Label: "День", Data: "d:today"}}}}
			}
			messageID := 0
			if tc.edit {
				messageID = 1
			}
			if got := a.send(context.Background(), 42, messageID, reply); got != tc.wantDelivered {
				t.Fatalf("доставка=%v", got)
			}
			if menus != tc.wantMenus {
				t.Fatalf("отправок меню=%d, нужно %d", menus, tc.wantMenus)
			}
		})
	}
}
