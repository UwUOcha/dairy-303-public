package schedule

import (
	"testing"
	"time"
)

// testGrid — пример сетки звонков: шесть пар, учебные дни пн–сб.
func testGrid() Grid {
	return Grid{
		Times: []GridTime{
			{ID: 1, Number: 1, MinuteFrom: 8*60 + 30, MinuteTo: 10*60 + 5, Label: "08:30 - 10:05"},
			{ID: 2, Number: 2, MinuteFrom: 10*60 + 25, MinuteTo: 12 * 60, Label: "10:25 - 12:00"},
			{ID: 3, Number: 3, MinuteFrom: 12*60 + 20, MinuteTo: 13*60 + 55, Label: "12:20 - 13:55"},
			{ID: 4, Number: 4, MinuteFrom: 14*60 + 10, MinuteTo: 15*60 + 45, Label: "14:10 - 15:45"},
			{ID: 5, Number: 5, MinuteFrom: 15*60 + 55, MinuteTo: 17*60 + 30, Label: "15:55 - 17:30"},
			{ID: 6, Number: 6, MinuteFrom: 17*60 + 40, MinuteTo: 19*60 + 15, Label: "17:40 - 19:15"},
		},
		Workdays: map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true},
	}
}

// lesson — короткий конструктор занятия на слот сетки.
func lesson(id int64, date string, slot int, name string, subgroup int64) Lesson {
	g := testGrid()
	t := g.Times[slot-1]
	return Lesson{
		ID: id, Date: date, TimeID: t.ID,
		MinuteFrom: t.MinuteFrom, MinuteTo: t.MinuteTo, TimeLabel: t.Label,
		Discipline: name, SubgroupID: subgroup,
	}
}

func TestVisibleTo(t *testing.T) {
	// 334 и 335 — настоящие глобальные id подгрупп ГР-12/1 и ГР-12/2.
	all := []Lesson{
		lesson(1, "2025-09-19", 1, "Лекция всей группе", 0),
		lesson(2, "2025-09-19", 2, "Химия перв. подгруппе", 334),
		lesson(3, "2025-09-19", 2, "Химия втор. подгруппе", 335),
	}

	tests := []struct {
		name     string
		subgroup int64
		wantIDs  []int64
	}{
		{"подгруппа не выбрана — видно всё", 0, []int64{1, 2, 3}},
		{"первая подгруппа", 334, []int64{1, 2}},
		{"вторая подгруппа", 335, []int64{1, 3}},
		{"чужая подгруппа — только общее", 999, []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VisibleTo(all, tt.subgroup)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("получено %d занятий, ожидалось %d", len(got), len(tt.wantIDs))
			}
			for i, id := range tt.wantIDs {
				if got[i].ID != id {
					t.Errorf("занятие[%d] = %d, ожидалось %d", i, got[i].ID, id)
				}
			}
		})
	}
}

func TestBuildDayNumbersAndOrder(t *testing.T) {
	g := testGrid()
	// Занятия приходят из базы в произвольном порядке.
	in := []Lesson{
		lesson(3, "2025-09-19", 4, "Четвёртая", 0),
		lesson(1, "2025-09-19", 1, "Первая", 0),
		lesson(2, "2025-09-19", 3, "Третья", 0),
		lesson(9, "2025-09-20", 1, "Другой день", 0),
	}
	d := BuildDay("2025-09-19", in, g, 0)

	if d.Weekday != time.Friday {
		t.Errorf("день недели = %v, ожидалась пятница", d.Weekday)
	}
	if !d.Workday {
		t.Error("пятница должна быть учебным днём")
	}
	if len(d.Items) != 3 {
		t.Fatalf("пар %d, ожидалось 3 (занятие другого дня должно отсеяться)", len(d.Items))
	}

	wantNumbers := []int{1, 3, 4}
	for i, want := range wantNumbers {
		if d.Items[i].Number != want {
			t.Errorf("пара[%d].Number = %d, ожидалось %d", i, d.Items[i].Number, want)
		}
	}
	for i := 1; i < len(d.Items); i++ {
		if d.Items[i-1].MinuteFrom > d.Items[i].MinuteFrom {
			t.Fatalf("пары не отсортированы по времени на позиции %d", i)
		}
	}
}

func TestBuildDayGaps(t *testing.T) {
	g := testGrid()
	tests := []struct {
		name  string
		slots []int
		// wantGaps — ожидаемое окно перед каждой парой, в минутах.
		wantGaps []int
	}{
		{
			name:     "пары подряд — окон нет, перемена окном не считается",
			slots:    []int{1, 2, 3},
			wantGaps: []int{0, 0, 0},
		},
		{
			name:     "пропущен один слот — окно",
			slots:    []int{1, 3},
			wantGaps: []int{0, 12*60 + 20 - (10*60 + 5)},
		},
		{
			name:     "пропущено два слота — окно длиннее",
			slots:    []int{1, 4},
			wantGaps: []int{0, 14*60 + 10 - (10*60 + 5)},
		},
		{
			name:     "одна пара за день",
			slots:    []int{5},
			wantGaps: []int{0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in []Lesson
			for i, s := range tt.slots {
				in = append(in, lesson(int64(i+1), "2025-09-19", s, "Предмет", 0))
			}
			d := BuildDay("2025-09-19", in, g, 0)
			if len(d.Items) != len(tt.wantGaps) {
				t.Fatalf("пар %d, ожидалось %d", len(d.Items), len(tt.wantGaps))
			}
			for i, want := range tt.wantGaps {
				if d.Items[i].GapBefore != want {
					t.Errorf("пара[%d].GapBefore = %d, ожидалось %d", i, d.Items[i].GapBefore, want)
				}
			}
		})
	}
}

// Две подгруппы в одном слоте не должны порождать фантомное окно у следующей пары.
func TestBuildDayParallelSubgroupsDoNotCreateGap(t *testing.T) {
	g := testGrid()
	in := []Lesson{
		lesson(1, "2025-09-19", 2, "Химия 1", 334),
		lesson(2, "2025-09-19", 2, "Химия 2", 335),
		lesson(3, "2025-09-19", 3, "Следующая пара", 0),
	}
	d := BuildDay("2025-09-19", in, g, 0)
	if len(d.Items) != 3 {
		t.Fatalf("пар %d, ожидалось 3", len(d.Items))
	}
	for i, it := range d.Items {
		if it.GapBefore != 0 {
			t.Errorf("пара[%d] (%s): окно %d, ожидалось 0", i, it.Discipline, it.GapBefore)
		}
	}
}

func TestShiftWorkday(t *testing.T) {
	g := testGrid()
	tests := []struct {
		name  string
		from  string
		delta int
		want  string
	}{
		// 2025-09-19 — пятница, 20-е суббота, 21-е воскресенье, 22-е понедельник.
		{"вперёд внутри недели", "2025-09-19", 1, "2025-09-20"},
		{"вперёд через воскресенье", "2025-09-20", 1, "2025-09-22"},
		{"назад через воскресенье", "2025-09-22", -1, "2025-09-20"},
		{"нулевой сдвиг", "2025-09-22", 0, "2025-09-22"},
		{"на неделю вперёд", "2025-09-22", 6, "2025-09-29"},
		{"кривая дата возвращается как есть", "не дата", 1, "не дата"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShiftWorkday(tt.from, tt.delta, g); got != tt.want {
				t.Errorf("ShiftWorkday(%q, %d) = %q, ожидалось %q", tt.from, tt.delta, got, tt.want)
			}
		})
	}
}

// Шаг вперёд с последующим шагом назад обязан вернуть в исходную точку.
// Старый бот на этом спотыкался: кнопка «вперёд» срабатывала со второго раза.
func TestShiftWorkdayRoundTrip(t *testing.T) {
	g := testGrid()
	start, _ := ParseDate("2025-09-15")
	for i := 0; i < 30; i++ {
		date := FormatDate(start.AddDate(0, 0, i))
		if !g.IsWorkday(mustParse(t, date).Weekday()) {
			continue
		}
		fwd := ShiftWorkday(date, 1, g)
		back := ShiftWorkday(fwd, -1, g)
		if back != date {
			t.Errorf("%s → вперёд %s → назад %s, ожидалось вернуться в %s", date, fwd, back, date)
		}
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := ParseDate(s)
	if err != nil {
		t.Fatalf("ParseDate(%q): %v", s, err)
	}
	return v
}

func TestNearestWorkday(t *testing.T) {
	g := testGrid()
	tests := []struct{ from, want string }{
		{"2025-09-19", "2025-09-19"}, // пятница
		{"2025-09-21", "2025-09-22"}, // воскресенье → понедельник
		{"2025-09-20", "2025-09-20"}, // суббота учебная
	}
	for _, tt := range tests {
		if got := NearestWorkday(tt.from, g); got != tt.want {
			t.Errorf("NearestWorkday(%q) = %q, ожидалось %q", tt.from, got, tt.want)
		}
	}
}

func TestMondayOf(t *testing.T) {
	tests := []struct{ from, want string }{
		{"2025-09-15", "2025-09-15"}, // сам понедельник
		{"2025-09-19", "2025-09-15"}, // пятница
		{"2025-09-21", "2025-09-15"}, // воскресенье относится к прошедшей неделе
		{"2025-09-22", "2025-09-22"},
	}
	for _, tt := range tests {
		got := FormatDate(MondayOf(mustParse(t, tt.from)))
		if got != tt.want {
			t.Errorf("MondayOf(%q) = %q, ожидалось %q", tt.from, got, tt.want)
		}
	}
}

func TestBuildWeek(t *testing.T) {
	g := testGrid()
	in := []Lesson{
		lesson(1, "2025-09-15", 1, "Понедельник", 0),
		lesson(2, "2025-09-20", 2, "Суббота", 0),
	}
	w := BuildWeek("2025-09-15", in, g, 0)

	if len(w.Days) != 6 {
		t.Fatalf("дней %d, ожидалось 6 (пн–сб, воскресенье выходное)", len(w.Days))
	}
	if w.Days[0].Date != "2025-09-15" || w.Days[5].Date != "2025-09-20" {
		t.Errorf("границы недели: %s … %s", w.Days[0].Date, w.Days[5].Date)
	}
	if w.Empty() {
		t.Error("неделя с двумя занятиями не должна считаться пустой")
	}
	if len(w.Days[0].Items) != 1 || len(w.Days[1].Items) != 0 {
		t.Errorf("занятия разложены по дням неверно")
	}
}

// Перенос пары на воскресенье — редкость, но терять её нельзя.
func TestBuildWeekKeepsLessonOnDayOff(t *testing.T) {
	g := testGrid()
	in := []Lesson{lesson(1, "2025-09-21", 1, "Воскресная пересдача", 0)}
	w := BuildWeek("2025-09-15", in, g, 0)

	if len(w.Days) != 7 {
		t.Fatalf("дней %d, ожидалось 7: воскресенье с парой должно попасть в выдачу", len(w.Days))
	}
	sunday := w.Days[6]
	if sunday.Date != "2025-09-21" || sunday.Workday || len(sunday.Items) != 1 {
		t.Errorf("воскресенье = %+v", sunday)
	}
}

func TestComputeNow(t *testing.T) {
	g := testGrid()
	in := []Lesson{
		lesson(1, "2025-09-19", 2, "Вторая", 0),
		lesson(2, "2025-09-19", 3, "Третья", 0),
	}
	d := BuildDay("2025-09-19", in, g, 0)
	at := func(h, m int) time.Time { return time.Date(2025, 9, 19, h, m, 0, 0, time.UTC) }

	tests := []struct {
		name        string
		at          time.Time
		wantCurrent string
		wantNext    string
		wantMinutes int
		wantRest    int
	}{
		{"до начала занятий", at(8, 0), "", "Вторая", 145, 2},
		{"идёт вторая пара", at(11, 0), "Вторая", "Третья", 80, 2},
		{"перемена между парами", at(12, 10), "", "Третья", 10, 1},
		{"занятия кончились", at(18, 0), "", "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := ComputeNow(d, tt.at)
			gotCur, gotNext := "", ""
			if n.Current != nil {
				gotCur = n.Current.Discipline
			}
			if n.Next != nil {
				gotNext = n.Next.Discipline
			}
			if gotCur != tt.wantCurrent {
				t.Errorf("Current = %q, ожидалось %q", gotCur, tt.wantCurrent)
			}
			if gotNext != tt.wantNext {
				t.Errorf("Next = %q, ожидалось %q", gotNext, tt.wantNext)
			}
			if n.MinutesToNext != tt.wantMinutes {
				t.Errorf("MinutesToNext = %d, ожидалось %d", n.MinutesToNext, tt.wantMinutes)
			}
			if n.RestToday != tt.wantRest {
				t.Errorf("RestToday = %d, ожидалось %d", n.RestToday, tt.wantRest)
			}
		})
	}
}

// Пустая пара — это заведённое расписанием окно, а не занятие: она не может
// оказаться «текущей» или «следующей».
func TestComputeNowSkipsEmptyLesson(t *testing.T) {
	g := testGrid()
	empty := lesson(1, "2025-09-19", 2, "Окно", 0)
	empty.Flags = FlagEmpty
	real := lesson(2, "2025-09-19", 3, "Настоящая пара", 0)

	d := BuildDay("2025-09-19", []Lesson{empty, real}, g, 0)
	n := ComputeNow(d, time.Date(2025, 9, 19, 11, 0, 0, 0, time.UTC))

	if n.Current != nil {
		t.Errorf("Current = %q, ожидалось пусто", n.Current.Discipline)
	}
	if n.Next == nil || n.Next.Discipline != "Настоящая пара" {
		t.Errorf("Next = %+v, ожидалась настоящая пара", n.Next)
	}
	if n.RestToday != 1 {
		t.Errorf("RestToday = %d, ожидалось 1", n.RestToday)
	}
}

func TestGridIsWorkdayFallback(t *testing.T) {
	// Сетка не загружена — считаем учебными понедельник–субботу.
	var empty Grid
	if !empty.IsWorkday(time.Monday) || !empty.IsWorkday(time.Saturday) {
		t.Error("без сетки пн–сб должны быть учебными")
	}
	if empty.IsWorkday(time.Sunday) {
		t.Error("без сетки воскресенье должно быть выходным")
	}
}

// Экран сессии — обещание «вот всё, что тебе сдавать», поэтому лишнее в нём
// хуже пропущенного: обычная пара, попавшая в список, обесценивает его весь.
func TestSessionKindTakesOnlyExams(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"Экз", "Экзамен"},
		{"экзамен", "Экзамен"},
		{" ЭКЗ ", "Экзамен"},
		{"Зач", "Зачёт"},
		{"Зачет", "Зачёт"},
		{"Диф. зач", "Дифзачёт"},
		{"Конс", "Консультация"},
		{"КП", "Курсовая"},
		{"Курсовая работа", "Курсовая"},
		{"Лек", ""},
		{"Пр", ""},
		{"Лб", ""},
		{"Инд", ""},
		{"", ""},
		{"Практика на базе", ""},
	} {
		if got := SessionKind(tt.in); got != tt.want {
			t.Errorf("SessionKind(%q) = %q, ожидалось %q", tt.in, got, tt.want)
		}
	}
	if !(Lesson{ClassType: "Экз"}).IsSession() || (Lesson{ClassType: "Лек"}).IsSession() {
		t.Error("IsSession расходится с SessionKind")
	}
}
