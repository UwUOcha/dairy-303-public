package schedule

import (
	"sort"
	"time"
)

// DateLayout — единственный формат дат, который ходит по системе.
const DateLayout = "2006-01-02"

// ParseDate разбирает ISO-дату.
func ParseDate(s string) (time.Time, error) { return time.Parse(DateLayout, s) }

// FormatDate печатает дату в ISO.
func FormatDate(t time.Time) string { return t.Format(DateLayout) }

// MondayOf возвращает понедельник той недели, в которую попадает дата.
func MondayOf(t time.Time) time.Time {
	// В Go воскресенье — нулевой день недели, поэтому его надо отматывать
	// на шесть дней назад, а не на ноль.
	shift := int(t.Weekday()) - int(time.Monday)
	if t.Weekday() == time.Sunday {
		shift = 6
	}
	return t.AddDate(0, 0, -shift)
}

// VisibleTo отбирает занятия, которые видит пользователь указанной подгруппы.
//
// Занятие для всей группы (SubgroupID == 0) видно всем. Занятие подгруппы
// видно только ей. Если подгруппа не выбрана, показываем всё: увидеть лишнюю
// пару не так обидно, как пропустить свою.
func VisibleTo(lessons []Lesson, subgroupID int64) []Lesson {
	if subgroupID == 0 {
		return lessons
	}
	out := make([]Lesson, 0, len(lessons))
	for _, l := range lessons {
		if l.SubgroupID == 0 || l.SubgroupID == subgroupID {
			out = append(out, l)
		}
	}
	return out
}

// BuildDay собирает расписание одного дня.
//
// lessons — занятия, уже отобранные по дате из базы; фильтрация по подгруппе,
// сортировка, нумерация пар и вычисление окон происходят здесь.
func BuildDay(date string, lessons []Lesson, g Grid, subgroupID int64) Day {
	d := Day{Date: date}
	if t, err := ParseDate(date); err == nil {
		d.Weekday = t.Weekday()
		d.Workday = g.IsWorkday(t.Weekday())
	}

	visible := VisibleTo(lessons, subgroupID)
	items := make([]Item, 0, len(visible))
	for _, l := range visible {
		if l.Date != date {
			continue
		}
		items = append(items, Item{Lesson: l, Number: g.NumberOf(l.TimeID)})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].MinuteFrom != items[j].MinuteFrom {
			return items[i].MinuteFrom < items[j].MinuteFrom
		}
		// Одинаковое время — разные подгруппы или поток плюс группа.
		// Порядок фиксируем по id, чтобы вывод был стабильным между запросами
		// и не ломал сравнение хэшей.
		return items[i].ID < items[j].ID
	})

	markGaps(items)
	d.Items = items
	return d
}

// markGaps проставляет окна между парами.
//
// Окно — это не любой промежуток: между соседними парами всегда есть перемена
// в 10–20 минут, и называть её окном бессмысленно. Настоящее окно — когда в
// сетке звонков пропущен хотя бы один слот. Без нумерации считаем окном
// перерыв от 60 минут; короткие перемены не отмечаем.
func markGaps(items []Item) {
	var prev *Item
	for i := range items {
		cur := &items[i]
		if cur.MinuteTo <= cur.MinuteFrom {
			continue
		}
		if prev != nil {
			gap := cur.MinuteFrom - prev.MinuteTo
			numbered := prev.Number > 0 && cur.Number > 0
			if gap > 0 && ((numbered && cur.Number > prev.Number+1) || (!numbered && gap >= 60)) {
				cur.GapBefore = gap
			}
		}
		// Пересекающиеся занятия отсчитываем от самого позднего окончания.
		if prev == nil || cur.MinuteTo > prev.MinuteTo {
			prev = cur
		}
	}
}

// BuildWeek собирает учебную неделю начиная с понедельника.
//
// В выдачу попадают учебные дни по сетке плюс любые дни, где занятия всё-таки
// есть: перенос пары на воскресенье — редкость, но молча её потерять нельзя.
func BuildWeek(monday string, lessons []Lesson, g Grid, subgroupID int64) Week {
	w := Week{Monday: monday}
	start, err := ParseDate(monday)
	if err != nil {
		return w
	}
	for i := 0; i < 7; i++ {
		date := FormatDate(start.AddDate(0, 0, i))
		day := BuildDay(date, lessons, g, subgroupID)
		if day.Workday || !day.Empty() {
			w.Days = append(w.Days, day)
		}
	}
	return w
}

// ShiftWorkday сдвигает дату на delta учебных дней, пропуская выходные.
//
// Именно здесь лежит лекарство от старого бага навигации: раньше бот считал
// смещения относительно «сегодня» и отдельно подпихивал воскресенье, из-за
// чего кнопка «вперёд» срабатывала со второго раза. Теперь шаг делается от
// показанной даты и всегда попадает в учебный день с первого нажатия.
func ShiftWorkday(date string, delta int, g Grid) string {
	t, err := ParseDate(date)
	if err != nil || delta == 0 {
		return date
	}
	step := 1
	if delta < 0 {
		step, delta = -1, -delta
	}
	// Потолок обхода: если вдруг сетка объявит выходными все дни, цикл не
	// должен уйти в бесконечность.
	const maxScan = 30
	for moved, scanned := 0, 0; moved < delta && scanned < maxScan; scanned++ {
		t = t.AddDate(0, 0, step)
		if g.IsWorkday(t.Weekday()) {
			moved++
		}
	}
	return FormatDate(t)
}

// NearestWorkday возвращает саму дату, если день учебный, иначе ближайший
// учебный день вперёд. Нужен, чтобы «сегодня» в воскресенье показывало
// понедельник, а не пустой экран.
func NearestWorkday(date string, g Grid) string {
	t, err := ParseDate(date)
	if err != nil {
		return date
	}
	for i := 0; i < 7; i++ {
		if g.IsWorkday(t.Weekday()) {
			return FormatDate(t)
		}
		t = t.AddDate(0, 0, 1)
	}
	return date
}

// ComputeNow отвечает на вопрос «что сейчас и что дальше» для уже собранного
// дня. at должен быть в часовом поясе расписания.
func ComputeNow(d Day, at time.Time) Now {
	n := Now{At: at}
	minute := at.Hour()*60 + at.Minute()

	for i := range d.Items {
		it := &d.Items[i]
		if it.Flags.Has(FlagEmpty) {
			continue
		}
		switch {
		case minute >= it.MinuteFrom && minute < it.MinuteTo:
			if n.Current == nil {
				n.Current = it
			}
			n.RestToday++
		case minute < it.MinuteFrom:
			if n.Next == nil {
				n.Next = it
				n.MinutesToNext = it.MinuteFrom - minute
			}
			n.RestToday++
		}
	}
	return n
}
