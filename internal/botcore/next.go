package botcore

import (
	"context"
	"strconv"
	"strings"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

func (b *Bot) showNextLesson(ctx context.Context, user api.UserResponse, guest int64) ([]Reply, error) {
	group, sub, asGuest := target(user, guest)
	if group == 0 {
		return requireGroup(user), nil
	}
	r, err := b.api.NextLesson(ctx, group, sub)
	if err != nil {
		return b.unavailable(err), nil
	}
	var text strings.Builder
	text.WriteString(guestPrefix(asGuest) + "<b>⏭ Когда следующая пара?</b>\n<i>" + esc(groupLine(r.Context)) + "</i>\n\n")
	var rows [][]Button
	if r.Lesson != nil {
		if r.Missing || r.Stale {
			text.WriteString("Ближайшая в сохранённых данных — ")
		} else {
			text.WriteString("Ближайшая пара — ")
		}
		text.WriteString("<b>" + esc(dateHeader(r.Day.Date, r.Today)) + "</b>\n\n")
		it := *r.Lesson
		it.GapBefore = 0
		writeItems(&text, []schedule.Item{it}, sub)
		rows = append(rows, []Button{{Label: "📅 Открыть день", Data: cbView(cbDay, r.Day.Date, asGuest)}})
	} else {
		if r.Missing || r.Stale {
			text.WriteString("Пока не могу надёжно определить следующую пару.")
		} else {
			text.WriteString("В загруженном расписании ближайших 14 дней ещё не начавшихся занятий нет.")
		}
	}
	text.WriteString("\nПоиск: " + esc(dateHeader(r.From, r.Today)) + " — " + esc(dateHeader(r.To, r.Today)) + ".")
	writeFreshness(&text, r.Freshness)
	data := cbNextLesson
	if asGuest != 0 {
		data = cb(cbNextLesson, strconv.FormatInt(asGuest, 10))
	}
	rows = append(rows, []Button{{Label: "🔄 Проверить снова", Data: data}, {Label: MenuToday, Data: cbView(cbDay, todayArg, asGuest)}})
	return []Reply{{Text: text.String(), Keyboard: &Keyboard{Rows: rows}, Edit: true, Menu: menuFor(user)}}, nil
}

func formatNextStudyDay(r api.NextLessonResponse, user api.UserResponse) Reply {
	reply := Reply{Edit: true, Menu: menuFor(user)}
	if r.Lesson != nil {
		day := api.DayResponse{Context: r.Context, Day: r.Day, Prev: r.Prev, Next: r.Next, Freshness: r.Freshness}
		prefix := ""
		if r.Missing || r.Stale {
			prefix = "Ближайший учебный день в сохранённом расписании. Данные могут быть неполными или устаревшими.\n\n"
		} else if r.Day.Date != r.From {
			prefix = "Завтра занятий нет. Ближайший учебный день:\n\n"
		}
		reply.Text = prefix + FormatDay(day)
		reply.Keyboard = DayKeyboard(day, 0)
		return reply
	}
	var text strings.Builder
	text.WriteString("<b>⏭ Ближайший учебный день</b>\n<i>" + esc(groupLine(r.Context)) + "</i>\n\n")
	if r.Missing || r.Stale {
		text.WriteString("Пока не могу надёжно определить ближайший учебный день.")
	} else {
		text.WriteString("В расписании на 14 дней начиная с завтра занятий нет.")
	}
	text.WriteString("\nПоиск: " + esc(dateHeader(r.From, r.Today)) + " — " + esc(dateHeader(r.To, r.Today)) + ".")
	writeFreshness(&text, r.Freshness)
	reply.Text = text.String()
	reply.Keyboard = &Keyboard{Rows: [][]Button{{{Label: MenuToday, Data: cbView(cbDay, todayArg, 0)}, {Label: MenuWeek, Data: cbView(cbWeek, r.Today, 0)}}}}
	return reply
}
