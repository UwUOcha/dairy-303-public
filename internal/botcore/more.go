package botcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/buildinfo"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"strconv"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Экран «Ещё» и всё, что за ним: помощь, рассказ о боте, приглашение друзей и
// переписка с автором.
//
// Отдельным файлом, потому что тема отдельная: расписание здесь не участвует
// вовсе, и единственное, что связывает эти сценарии с остальным ботом, —
// нижняя панель, из которой они открываются.

// more рисует экран «Ещё».
func (b *Bot) more(user api.UserResponse, edit bool) []Reply {
	_, _, canWrite := b.author.target()
	return []Reply{{
		Text:     profile.Current().Render(moreText, true),
		Edit:     edit,
		Keyboard: MoreKeyboard(canWrite),
		Menu:     menuFor(user),
	}}
}

// about рассказывает о боте.
//
// Число пользователей запрашивается здесь, а не приезжает вместе с
// пользователем: экран открывают редко, а лишнее поле в ответе на каждое
// сообщение стоило бы дороже. Неудача запроса не отменяет экран — строка
// просто не появится.
func (b *Bot) about(ctx context.Context, user api.UserResponse) []Reply {
	line := ""
	if n, err := b.api.CountUsers(ctx); err != nil {
		b.log.Warn("не удалось посчитать пользователей", "ошибка", err)
	} else if n >= aboutUsersFloor {
		line = fmt.Sprintf("\nСейчас им пользуются %s %s.\n",
			spaceNum(n), plural(n, "студент", "студента", "студентов"))
	}

	_, _, canWrite := b.author.target()
	return []Reply{{
		Text:     profile.Current().Render(fmt.Sprintf(profile.Current().Text("about", aboutText), line), true) + "\n\nИсходный код и лицензия: " + buildinfo.SourceCodeURL,
		Edit:     true,
		Keyboard: AboutKeyboard(canWrite),
	}}
}

// share присылает сообщение, которое пересылают друзьям.
//
// Отдельным сообщением и без кнопок: его пересылают целиком, и всё нужное —
// что это и куда идти — должно уместиться в самом тексте.
func (b *Bot) share(user api.UserResponse) []Reply {
	return []Reply{{
		Text: profile.Current().Render(fmt.Sprintf(profile.Current().Text("share", shareText), b.author.link(user.User.Platform)), true),
		Menu: menuFor(user),
	}}
}

// ── обратная связь ──────────────────────────────────────────────────────────

// maxFeedbackLen — сколько символов обращения доедет до автора.
//
// Полторы тысячи с запасом покрывают любую осмысленную жалобу и оставляют
// место шапке: сообщение ВКонтакте обрывается на четырёх тысячах символов, и
// обрезать лучше здесь, где видно, что именно обрезается.
const maxFeedbackLen = 1500

// askFeedback просит написать обращение.
func (b *Bot) askFeedback(ctx context.Context, user api.UserResponse) ([]Reply, error) {
	if _, _, ok := b.author.target(); !ok {
		return []Reply{{Text: feedbackOffText, Edit: true, Keyboard: HelpKeyboard()}}, nil
	}
	u := user.User
	u.Await = store.AwaitFeedback
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}
	return []Reply{{Text: feedbackAskText, Edit: true, Keyboard: FeedbackKeyboard()}}, nil
}

// saveFeedback принимает написанное обращение и отправляет его автору.
func (b *Bot) saveFeedback(ctx context.Context, user api.UserResponse, text string) ([]Reply, error) {
	target, targetID, ok := b.author.target()
	if !ok {
		b.forgetAwait(ctx, &user)
		return []Reply{{Text: feedbackOffText, Menu: menuFor(user)}}, nil
	}

	text = strings.TrimSpace(text)
	if text == "" {
		// Ожидание не снимаем: человек собирался написать, а не передумал.
		return []Reply{{Text: feedbackEmptyText, Keyboard: FeedbackKeyboard()}}, nil
	}
	text = clampRunes(text, maxFeedbackLen)

	group := ""
	if user.Group != nil {
		group = user.Group.Name
	}
	id, wait, err := b.api.AddFeedback(ctx, store.Feedback{
		Platform:  user.User.Platform,
		ExtID:     user.User.ExtID,
		GroupName: group,
		Text:      text,
	})
	if err != nil {
		return b.unavailable(err), nil
	}
	if wait > 0 {
		// Ожидание остаётся: человеку отказали в скорости, а не в разговоре, и
		// повторить он должен тем же сообщением, не нажимая кнопку заново.
		return []Reply{{
			Text:     fmt.Sprintf(feedbackOftenText, waitWords(wait)),
			Keyboard: FeedbackKeyboard(),
		}}, nil
	}
	b.forgetAwait(ctx, &user)

	payload, err := json.Marshal(store.FeedbackPayload{
		ID:        id,
		Platform:  user.User.Platform,
		GroupName: group,
		Text:      text,
	})
	if err != nil {
		return nil, err
	}
	// Неудача постановки в очередь не отменяет приёма: обращение уже в базе, и
	// говорить человеку «не отправилось» — значит звать его написать ещё раз,
	// а второй заход упрётся в паузу между обращениями.
	if err := b.api.PutOutbox(ctx, api.OutboxPutRequest{
		Platform: target,
		ExtID:    targetID,
		Kind:     store.OutboxFeedback,
		DedupKey: strconv.FormatInt(id, 10),
		Payload:  string(payload),
	}); err != nil {
		b.log.Error("обращение принято, но не поставлено в очередь", "номер", id, "ошибка", err)
	}
	return []Reply{{Text: profile.Current().Render(feedbackSentText, true), Menu: menuFor(user)}}, nil
}

// askAnswer открывает автору ввод ответа на обращение.
//
// Новым сообщением, а не правкой: редактирование затёрло бы само обращение, на
// которое автор собрался отвечать.
func (b *Bot) askAnswer(ctx context.Context, user api.UserResponse, arg string) ([]Reply, error) {
	if !b.author.is(user.User.Platform, user.User.ExtID) {
		return b.more(user, true), nil
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return []Reply{{Text: answerGoneText}}, nil
	}
	u := user.User
	u.Await = store.AwaitAnswer + ":" + arg
	if err := b.saveUser(ctx, &user, u); err != nil {
		return b.unavailable(err), nil
	}
	return []Reply{{Text: fmt.Sprintf(answerAskText, id)}}, nil
}

// sendAnswer отправляет ответ автора тому, кто писал.
func (b *Bot) sendAnswer(ctx context.Context, user api.UserResponse, arg, text string) ([]Reply, error) {
	if !b.author.is(user.User.Platform, user.User.ExtID) {
		b.forgetAwait(ctx, &user)
		return b.more(user, false), nil
	}

	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		b.forgetAwait(ctx, &user)
		return []Reply{{Text: answerGoneText}}, nil
	}
	f, err := b.api.Feedback(ctx, id)
	if errors.Is(err, api.ErrNotFound) {
		b.forgetAwait(ctx, &user)
		return []Reply{{Text: answerGoneText}}, nil
	}
	if err != nil {
		return b.unavailable(err), nil
	}

	text = clampRunes(strings.TrimSpace(text), maxFeedbackLen)
	if text == "" {
		return []Reply{{Text: feedbackEmptyText}}, nil
	}
	payload, err := json.Marshal(store.AnswerPayload{ID: id, Text: text})
	if err != nil {
		return nil, err
	}
	if err := b.api.PutOutbox(ctx, api.OutboxPutRequest{
		Platform: f.Platform,
		ExtID:    f.ExtID,
		Kind:     store.OutboxAnswer,
		DedupKey: strconv.FormatInt(id, 10),
		Payload:  string(payload),
	}); err != nil {
		return b.unavailable(err), nil
	}
	if err := b.api.MarkAnswered(ctx, id); err != nil {
		// Отметка — это только пометка в архиве: ответ уже в очереди, и
		// отказывать из-за неё автору незачем.
		b.log.Warn("не удалось отметить обращение отвеченным", "номер", id, "ошибка", err)
	}
	b.forgetAwait(ctx, &user)
	return []Reply{{Text: fmt.Sprintf(answerSentText, id)}}, nil
}

// ── сообщения очереди ───────────────────────────────────────────────────────

// FeedbackMessage собирает обращение в том виде, в каком его увидит автор.
func (b *Bot) FeedbackMessage(p store.FeedbackPayload) (string, *Keyboard) {
	who := platformName(p.Platform)
	if p.GroupName != "" {
		who = esc(p.GroupName) + " · " + who
	}
	return fmt.Sprintf(feedbackLead, p.ID, who, esc(p.Text)), AnswerKeyboard(p.ID)
}

// AnswerMessage собирает ответ автора в том виде, в каком его увидит человек.
//
// Текст экранируется, хотя пишет его автор: «<3» в ответе иначе обрывает
// отправку, и объяснять это себе же в проде — сомнительное удовольствие.
func (b *Bot) AnswerMessage(p store.AnswerPayload) (string, *Keyboard) {
	return fmt.Sprintf(profile.Current().Render(answerLead, true), esc(p.Text)), ReplyKeyboard()
}

// platformName называет площадку по-русски: в шапке обращения важно, куда
// уйдёт ответ.
func platformName(platform string) string {
	if platform == "vk" {
		return "ВКонтакте"
	}
	return "телеграм"
}

// ── мелочи ──────────────────────────────────────────────────────────────────

// clampRunes обрезает текст по числу символов, а не байтов.
func clampRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return strings.TrimSpace(string(r[:limit])) + "…"
}

// spaceNum разбивает число на разряды: «1 240» читается с одного взгляда, а
// «1240» приходится разбирать.
func spaceNum(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// waitWords печатает остаток паузы словами.
//
// Округление вверх намеренное: сказать «через минуту» и отказать ещё раз —
// худший вариант из возможных.
func waitWords(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m < 1 {
		m = 1
	}
	return fmt.Sprintf("%d %s", m, plural(m, "минуту", "минуты", "минут"))
}
