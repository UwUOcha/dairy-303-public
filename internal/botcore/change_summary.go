package botcore

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// PersonalChangeMessage возвращает пустой текст, если видимое расписание
// подгруппы не изменилось, в том числе после отката правки до доставки.
func PersonalChangeMessage(r api.ChangeSummaryResponse) (string, *Keyboard) {
	var blocks []string
	var dates []string
	allBefore, allAfter := map[int64]schedule.Item{}, map[int64]schedule.Item{}
	for _, d := range r.Days {
		for _, it := range realItems(d.Before) {
			allBefore[it.ID] = it
		}
		for _, it := range realItems(d.After) {
			allAfter[it.ID] = it
		}
	}
	for _, d := range r.Days {
		lines := personalDayChanges(d, allBefore, allAfter)
		if len(lines) == 0 {
			continue
		}
		dates = append(dates, d.Date)
		block := "<b>" + esc(dateHeader(d.Date, r.Today)) + "</b>\n"
		shownLines := 0
		for _, line := range lines[:min(len(lines), 6)] {
			rendered := "• " + esc(clampRunes(line, 350)) + "\n"
			if len([]rune(block+rendered)) > 2400 {
				break
			}
			block += rendered
			shownLines++
		}
		if len(lines) > shownLines {
			block += fmt.Sprintf("Ещё правок: %d — в подробностях.\n", len(lines)-shownLines)
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return "", nil
	}
	text := "📌 <b>Изменения в твоём расписании</b>\n<i>" + esc(groupLine(r.Context)) + "</i>\n\n"
	// Не раздуваем сообщение на перезаливке месяца: подробности доступны кнопкой.
	shown := 0
	for _, block := range blocks {
		if len([]rune(text+block)) > 3000 {
			break
		}
		text += block + "\n"
		shown++
	}
	if shown < len(blocks) {
		text += fmt.Sprintf("Есть ещё изменения: дней %d. Открой подробности.\n", len(blocks)-shown)
	}
	group := strconv.FormatInt(r.Group.ID, 10)
	kb := &Keyboard{Rows: [][]Button{
		{{Label: "📅 Открыть день", Data: cb(cbDay, dates[0], group)}, {Label: "🔍 Все изменения", Data: cb(cbChanges, group)}},
		{{Label: "🔕 Не сообщать о правках", Data: cb(cbChangesSet, changesOff)}},
	}}
	return strings.TrimSpace(text), kb
}

func personalDayChanges(d api.ChangeDayResponse, allBefore, allAfter map[int64]schedule.Item) []string {
	before, after := realItems(d.Before), realItems(d.After)
	was := map[int64]schedule.Item{}
	now := map[int64]schedule.Item{}
	for _, it := range before {
		was[it.ID] = it
	}
	for _, it := range after {
		now[it.ID] = it
	}
	var lines []string
	for _, it := range before {
		other, ok := now[it.ID]
		label := orDash(it.Discipline, "Пара") + " в " + startTime(it)
		if note := audienceNote(it.Lesson, viewerSubgroup(d.Context)); note != "" {
			label += " (" + note + ")"
		}
		moved := false
		if !ok {
			other, ok = allAfter[it.ID]
			moved = ok && other.Date != it.Date
		}
		if !ok {
			lines = append(lines, label+" — отменена.")
			continue
		}
		var fields []string
		if moved {
			fields = append(fields, "перенесена на "+dateHeader(other.Date, d.Today)+" в "+startTime(other))
		}
		add := func(name, a, b string) {
			if a != b {
				fields = append(fields, name+": "+orDash(a, "не указано")+" → "+orDash(b, "не указано"))
			}
		}
		add("время", lessonTime(it), lessonTime(other))
		add("предмет", it.Discipline, other.Discipline)
		add("аудитория", it.Classroom, other.Classroom)
		add("тип занятия", it.ClassType, other.ClassType)
		staff := func(v []string) string {
			v = append([]string(nil), v...)
			sort.Strings(v)
			return strings.Join(v, ", ")
		}
		add("преподаватель", staff(it.Staff), staff(other.Staff))
		mode := func(it schedule.Item) string {
			if m := marks(it.Lesson); m != "" {
				return m
			}
			return "очно"
		}
		add("формат", mode(it), mode(other))
		add("примечание", it.Comments, other.Comments)
		add("для кого", audienceNote(it.Lesson, viewerSubgroup(d.Context)), audienceNote(other.Lesson, viewerSubgroup(d.Context)))
		if len(fields) > 0 {
			lines = append(lines, label+" — "+strings.Join(fields, "; ")+".")
		}
	}
	for _, it := range after {
		if _, ok := was[it.ID]; !ok {
			if old, ok := allBefore[it.ID]; ok && old.Date != it.Date {
				continue
			}
			line := orDash(it.Discipline, "Пара") + " — добавлена в " + startTime(it)
			if it.Classroom != "" {
				line += ", " + it.Classroom
			}
			if m := marks(it.Lesson); m != "" {
				line += ", " + m
			}
			lines = append(lines, line+".")
		}
	}
	if len(lines) > 0 && len(before) > 0 {
		if len(after) == 0 {
			lines = append(lines, "В этот день занятий больше нет.")
		} else if before[0].MinuteFrom != after[0].MinuteFrom {
			lines = append(lines, "Начало дня теперь в "+startTime(after[0])+".")
		}
	}
	return lines
}

func realItems(d schedule.Day) []schedule.Item {
	var out []schedule.Item
	for _, it := range d.Items {
		if !it.Flags.Has(schedule.FlagEmpty) && !it.Flags.Has(schedule.FlagNonStudy) {
			out = append(out, it)
		}
	}
	return out
}
