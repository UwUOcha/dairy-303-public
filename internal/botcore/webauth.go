package botcore

import (
	"context"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"strings"
)

// AuthText hides browser challenges from ordinary bot logs.
func AuthText(text string) string {
	if strings.Contains(text, "web_") || strings.HasPrefix(text, "/login ") {
		return "[вход на сайт]"
	}
	return text
}
func (b *Bot) webAuth(ctx context.Context, u Update) ([]Reply, bool, error) {
	text := strings.TrimSpace(u.Text)
	challenge := ""
	if strings.HasPrefix(text, "/start web_") {
		challenge = strings.TrimPrefix(text, "/start web_")
	}
	if strings.HasPrefix(text, "/login ") {
		challenge = strings.TrimSpace(strings.TrimPrefix(text, "/login "))
	}
	reset := text == "/web_logout"
	if challenge == "" && !reset && u.Callback != "web:logout" {
		return nil, false, nil
	}
	reply := func(t string) ([]Reply, bool, error) { return []Reply{{Text: t}}, true, nil }
	if !u.Private {
		return reply("Для входа на сайт открой личный диалог с ботом.")
	}
	if reset {
		return []Reply{{Text: "Завершить все сеансы сайта? На каждом устройстве потребуется войти снова.", Keyboard: &Keyboard{Rows: [][]Button{{{Label: "Выйти на всех устройствах", Data: "web:logout"}}}}}}, true, nil
	}
	op := "issue"
	if u.Callback == "web:logout" {
		op = "bot_revoke"
	}
	if op == "issue" && len(challenge) != 43 {
		return reply("Ссылка входа некорректна. Начни вход заново на сайте.")
	}
	out, err := b.api.WebAuth(ctx, store.WebAuthRequest{Op: op, Platform: u.Platform, ExtID: u.UserID, Challenge: challenge})
	if err != nil {
		return b.unavailable(err), true, nil
	}
	switch out.Error {
	case "not_allowed":
		return reply("Этот аккаунт пока не включён в закрытую бету «Между парами». Если тебя пригласили, напиши автору бота.")
	case "issued":
		return reply("Код для этой ссылки уже выдан. Используй предыдущее сообщение или начни вход на сайте заново.")
	case "":
	default:
		return reply("Ссылка входа истекла. Начни вход заново на сайте.")
	}
	if op == "bot_revoke" {
		return reply("Все сеансы сайта завершены. Можно снова войти на нужных устройствах.")
	}
	return reply("Код входа на " + profile.Current().Render("{{public_url}}", true) + ": <code>" + out.Code + "</code>\n\nВведи его в том браузере, где начал вход. Код действует до конца пяти минут с начала входа. Не пересылай код другим людям. Если ты не открывал сайт — просто проигнорируй сообщение.\n\nПотерял устройство? /web_logout завершит все сеансы сайта.")
}
