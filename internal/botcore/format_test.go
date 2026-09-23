package botcore

import (
	"strings"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func item(number, from, to int, label, discipline string) schedule.Item {
	return schedule.Item{
		Lesson: schedule.Lesson{
			ID: int64(number), Date: "2025-09-19",
			TimeID: int64(number), MinuteFrom: from, MinuteTo: to, TimeLabel: label,
			Discipline: discipline, ClassType: "Пр", Classroom: "к. 2/423",
			Staff: []string{"Смирнов С.С."},
		},
		Number: number,
	}
}

func dayResponse(items ...schedule.Item) api.DayResponse {
	return api.DayResponse{
		Context: api.Context{
			Group: store.Group{ID: 232, Name: "ГР-12"},
			Today: "2025-09-19",
		},
		Day: schedule.Day{
			Date: "2025-09-19", Weekday: time.Friday, Workday: true, Items: items,
		},
		Prev: "2025-09-18",
		Next: "2025-09-20",
	}
}

func TestHumanDate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2025-09-19", "Пятница, 19 сентября"},
		{"2026-01-01", "Четверг, 1 января"},
		{"2025-05-31", "Суббота, 31 мая"},
		{"не дата", "не дата"},
	}
	for _, tt := range tests {
		if got := humanDate(tt.in); got != tt.want {
			t.Errorf("humanDate(%q) = %q, ожидалось %q", tt.in, got, tt.want)
		}
	}
}

func TestDateHeaderAddsYearOutsideCurrentYear(t *testing.T) {
	if got := dateHeader("2025-09-19", "2025-01-01"); got != "Пятница, 19 сентября" {
		t.Errorf("дата текущего года = %q", got)
	}
	if got := dateHeader("2026-01-01", "2025-09-19"); got != "Четверг, 1 января 2026" {
		t.Errorf("дата другого года = %q", got)
	}
}

func TestRelativeHint(t *testing.T) {
	tests := []struct{ date, today, want string }{
		{"2025-09-19", "2025-09-19", "сегодня"},
		{"2025-09-20", "2025-09-19", "завтра"},
		{"2025-09-18", "2025-09-19", "вчера"},
		{"2025-09-25", "2025-09-19", ""},
		{"кривая", "2025-09-19", ""},
	}
	for _, tt := range tests {
		if got := relativeHint(tt.date, tt.today); got != tt.want {
			t.Errorf("relativeHint(%q, %q) = %q, ожидалось %q", tt.date, tt.today, got, tt.want)
		}
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, ""},
		{-5, ""},
		{45, "45 мин"},
		{60, "1 ч"},
		{120, "2 ч"},
		{130, "2 ч 10 мин"},
	}
	for _, tt := range tests {
		if got := duration(tt.in); got != tt.want {
			t.Errorf("duration(%d) = %q, ожидалось %q", tt.in, got, tt.want)
		}
	}
}

// Названия дисциплин приходят от upstream и попадают в сообщение с
// parse_mode=HTML. Без экранирования угловая скобка в названии сломала бы
// разбор и телеграм отверг бы всё сообщение целиком.
func TestFormatDayEscapesHTML(t *testing.T) {
	it := item(1, 510, 605, "08:30 - 10:05", "Матан <для> тех & прочих")
	it.Staff = []string{"Иванов <И.И.>"}
	out := FormatDay(dayResponse(it))

	if strings.Contains(out, "<для>") || strings.Contains(out, "<И.И.>") {
		t.Errorf("HTML не экранирован:\n%s", out)
	}
	if !strings.Contains(out, "&lt;для&gt;") || !strings.Contains(out, "&amp;") {
		t.Errorf("ожидались экранированные последовательности:\n%s", out)
	}
}

func TestFormatDayStates(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*api.DayResponse)
		want    string
		wantNot string
	}{
		{
			name:   "выходной",
			mutate: func(r *api.DayResponse) { r.Day.Workday = false },
			want:   "Выходной",
		},
		{
			name:   "учебный день без пар",
			mutate: func(r *api.DayResponse) {},
			want:   "Занятий нет.",
		},
		{
			name:   "расписание не заведено",
			mutate: func(r *api.DayResponse) { r.Missing = true },
			want:   "пока не удалось загрузить",
		},
		{
			// Пометка о свежести — то, ради чего держится локальная копия:
			// упавший адаптер не должен превращаться в молчание бота.
			name: "устаревшие данные",
			mutate: func(r *api.DayResponse) {
				r.Day.Items = []schedule.Item{item(1, 510, 605, "08:30 - 10:05", "Химия")}
				r.Stale = true
				r.FetchedAt = time.Date(2025, 9, 17, 3, 15, 0, 0, time.UTC)
			},
			want: "Данные от 17.09 06:15",
		},
		{
			name: "свежие данные без пометки",
			mutate: func(r *api.DayResponse) {
				r.Day.Items = []schedule.Item{item(1, 510, 605, "08:30 - 10:05", "Химия")}
				r.FetchedAt = time.Now()
			},
			wantNot: "Данные от",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := dayResponse()
			tt.mutate(&r)
			out := FormatDay(r)
			if tt.want != "" && !strings.Contains(out, tt.want) {
				t.Errorf("ожидалось %q в:\n%s", tt.want, out)
			}
			if tt.wantNot != "" && strings.Contains(out, tt.wantNot) {
				t.Errorf("не ожидалось %q в:\n%s", tt.wantNot, out)
			}
		})
	}
}

func TestFormatDayGapAndFlags(t *testing.T) {
	first := item(1, 510, 605, "08:30 - 10:05", "Химия")
	late := item(4, 850, 945, "14:10 - 15:45", "Латинский")
	late.GapBefore = 850 - 605
	late.Flags = schedule.FlagRemote

	out := FormatDay(dayResponse(first, late))
	if !strings.Contains(out, "Окно 4 ч 5 мин") {
		t.Errorf("окно не показано:\n%s", out)
	}
	if !strings.Contains(out, "\n<i>⏳ Окно 4 ч 5 мин</i>\n\n<b>4 пара</b>") {
		t.Errorf("окно не отделено переносами с обеих сторон:\n%s", out)
	}
	if !strings.Contains(out, "💻 дистанционно") {
		t.Errorf("не показан признак дистанционки:\n%s", out)
	}
}

// Пустая пара показывается «окном», а не выбрасывается молча: расписание
// предусмотрело её явно, и человеку это важно знать.
func TestFormatDayEmptyLesson(t *testing.T) {
	empty := item(2, 625, 720, "10:25 - 12:00", "")
	empty.Flags = schedule.FlagEmpty

	out := FormatDay(dayResponse(empty))
	if !strings.Contains(out, "2 пара") || !strings.Contains(out, "Окно") {
		t.Errorf("пустая пара не показана окном:\n%s", out)
	}
}

func TestAudienceNote(t *testing.T) {
	sub := schedule.Lesson{Audience: schedule.AudienceSubgroup, SubgroupID: 335, AudienceLabel: "ГР-12/2"}
	flow := schedule.Lesson{Audience: schedule.AudienceFlow, AudienceLabel: "Поток 1 (…)"}
	own := schedule.Lesson{Audience: schedule.AudienceGroup, AudienceLabel: "ГР-12"}

	tests := []struct {
		name   string
		lesson schedule.Lesson
		viewer int64
		want   string
	}{
		{"своя подгруппа не подписывается", sub, 335, ""},
		{"чужая подгруппа подписывается", sub, 334, "ГР-12/2"},
		{"без выбранной подгруппы подписывается", sub, 0, "ГР-12/2"},
		{"поток подписывается всегда", flow, 335, "поток"},
		{"обычная пара не подписывается", own, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := audienceNote(tt.lesson, tt.viewer); got != tt.want {
				t.Errorf("audienceNote = %q, ожидалось %q", got, tt.want)
			}
		})
	}
}

func TestFormatWeek(t *testing.T) {
	r := api.WeekResponse{
		Context: api.Context{Group: store.Group{Name: "ГР-12"}, Today: "2025-09-19"},
		Week: schedule.Week{
			Monday: "2025-09-15",
			Days: []schedule.Day{
				{Date: "2025-09-15", Weekday: time.Monday, Workday: true,
					Items: []schedule.Item{item(1, 510, 605, "08:30 - 10:05", "Биология")}},
				{Date: "2025-09-16", Weekday: time.Tuesday, Workday: true},
				{Date: "2025-09-20", Weekday: time.Saturday, Workday: true,
					Items: []schedule.Item{item(2, 625, 720, "10:25 - 12:00", "Анатомия")}},
			},
		},
		ThisMonday: "2025-09-15",
	}
	out := FormatWeek(r)

	if !strings.Contains(out, "15 – 20 сентября") {
		t.Errorf("нет диапазона недели:\n%s", out)
	}
	// Месяц назван в шапке, поэтому в заголовках дней его быть не должно.
	if strings.Contains(out, "Понедельник, 15 сентября") {
		t.Errorf("месяц продублирован в заголовке дня:\n%s", out)
	}
	if !strings.Contains(out, "Понедельник, 15") || !strings.Contains(out, "Суббота, 20") {
		t.Errorf("нет заголовков дней:\n%s", out)
	}
	// Пустой учебный день остаётся в выдаче строкой «занятий нет». Молча
	// пропущенный вторник неотличим от вторника без пар, а спрашивают как раз
	// об этом.
	if !strings.Contains(out, "<b>Вторник, 16</b> — <i>занятий нет</i>") {
		t.Errorf("пустой учебный день выпал из недели:\n%s", out)
	}
	if !strings.Contains(out, "08:30") || !strings.Contains(out, "Биология") {
		t.Errorf("нет содержимого пары:\n%s", out)
	}
	// Тип занятия нужен и в недельной строке: без него лекцию не отличить от
	// практики по той же дисциплине.
	if !strings.Contains(out, "<b>Биология</b> · Пр") {
		t.Errorf("нет типа занятия в строке недели:\n%s", out)
	}
}

func TestFormatWeekEmpty(t *testing.T) {
	r := api.WeekResponse{
		Context: api.Context{Group: store.Group{Name: "ГР-12"}, Today: "2025-09-19"},
		Week:    schedule.Week{Monday: "2025-09-15"},
	}
	if out := FormatWeek(r); !strings.Contains(out, "занятий нет") {
		t.Errorf("пустая неделя:\n%s", out)
	}
	r.Missing = true
	if out := FormatWeek(r); !strings.Contains(out, "не удалось загрузить") {
		t.Errorf("неделя без расписания:\n%s", out)
	}
}

func TestFormatNow(t *testing.T) {
	current := item(2, 625, 720, "10:25 - 12:00", "Биология")
	next := item(3, 740, 835, "12:20 - 13:55", "Анатомия")
	at := time.Date(2025, 9, 19, 11, 0, 0, 0, time.UTC)

	r := api.NowResponse{
		Context: api.Context{Group: store.Group{Name: "ГР-12"}, Today: "2025-09-19"},
		Day:     schedule.Day{Date: "2025-09-19", Workday: true, Items: []schedule.Item{current, next}},
		Now: schedule.Now{
			At: at, Current: &current, Next: &next,
			MinutesToNext: 80, RestToday: 2,
		},
	}
	out := FormatNow(r)

	if !strings.Contains(out, "11:00") {
		t.Errorf("нет текущего времени:\n%s", out)
	}
	if !strings.Contains(out, "Сейчас идёт") || !strings.Contains(out, "Биология") {
		t.Errorf("нет текущей пары:\n%s", out)
	}
	if !strings.Contains(out, "1 ч 20 мин") || !strings.Contains(out, "Анатомия") {
		t.Errorf("нет следующей пары:\n%s", out)
	}
}

func TestFormatNowStates(t *testing.T) {
	base := func() api.NowResponse {
		return api.NowResponse{
			Context: api.Context{Group: store.Group{Name: "ГР-12"}, Today: "2025-09-19"},
			Day:     schedule.Day{Date: "2025-09-19", Workday: true},
			Now:     schedule.Now{At: time.Date(2025, 9, 19, 18, 0, 0, 0, time.UTC)},
		}
	}
	tests := []struct {
		name   string
		mutate func(*api.NowResponse)
		want   string
	}{
		{"выходной", func(r *api.NowResponse) { r.Day.Workday = false }, "выходной"},
		{"расписание не загружено", func(r *api.NowResponse) { r.Missing = true }, "пока не удалось загрузить"},
		{"нет пар сегодня", func(r *api.NowResponse) {}, "занятий нет"},
		{"пары кончились", func(r *api.NowResponse) {
			it := item(1, 510, 605, "08:30 - 10:05", "Химия")
			r.Day.Items = []schedule.Item{it}
		}, "закончились"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base()
			tt.mutate(&r)
			if out := FormatNow(r); !strings.Contains(out, tt.want) {
				t.Errorf("ожидалось %q в:\n%s", tt.want, out)
			}
		})
	}
}

func TestWeekRange(t *testing.T) {
	tests := []struct {
		name  string
		dates []string
		today string
		want  string
	}{
		{"внутри текущего года", []string{"2025-09-15", "2025-09-20"}, "2025-09-19", "15 – 20 сентября"},
		{"через месяц", []string{"2025-09-29", "2025-10-04"}, "2025-09-19", "29 сентября – 4 октября"},
		{"другой год", []string{"2026-09-14", "2026-09-19"}, "2025-09-19", "14 – 19 сентября 2026"},
		{"через новый год", []string{"2025-12-29", "2026-01-03"}, "2025-09-19", "29 декабря 2025 – 3 января 2026"},
		{"пустая неделя", nil, "2025-09-19", "Неделя"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w schedule.Week
			for _, d := range tt.dates {
				w.Days = append(w.Days, schedule.Day{Date: d})
			}
			if got := weekRange(w, tt.today); got != tt.want {
				t.Errorf("weekRange = %q, ожидалось %q", got, tt.want)
			}
		})
	}
}

func TestFormatMinute(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{{0, "00:00"}, {7 * 60, "07:00"}, {7*60 + 30, "07:30"}, {23*60 + 59, "23:59"}}
	for _, tt := range tests {
		if got := formatMinute(tt.in); got != tt.want {
			t.Errorf("formatMinute(%d) = %q, ожидалось %q", tt.in, got, tt.want)
		}
	}
}

func TestStartTime(t *testing.T) {
	tests := []struct {
		name string
		it   schedule.Item
		want string
	}{
		{"из подписи сетки", item(1, 510, 605, "08:30 - 10:05", "х"), "08:30"},
		{"из минут, если подписи нет", item(1, 510, 605, "", "х"), "08:30"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := startTime(tt.it); got != tt.want {
				t.Errorf("startTime = %q, ожидалось %q", got, tt.want)
			}
		})
	}
}

// Даже в воскресенье возможна перенесённая пара; отсутствие загрузки не
// подтверждает отсутствие занятий.
func TestFormatDayMissingSundayIsUnknown(t *testing.T) {
	r := dayResponse()
	r.Day.Workday = false
	r.Missing = true
	out := FormatDay(r)
	if strings.Contains(out, "Выходной") || !strings.Contains(out, "не удалось загрузить") {
		t.Fatalf("неизвестное расписание выдано за выходной: %s", out)
	}
}

func TestFormatDayMissingOnWorkday(t *testing.T) {
	r := api.DayResponse{
		Context:   api.Context{Group: store.Group{Name: "ГР-12"}, Today: "2025-09-19"},
		Day:       schedule.Day{Date: "2025-09-19", Weekday: time.Friday, Workday: true},
		Freshness: api.Freshness{Missing: true},
	}
	if out := FormatDay(r); !strings.Contains(out, "не удалось загрузить") {
		t.Errorf("нет объяснения про незаведённое расписание:\n%s", out)
	}
}

// Подгруппу вуз называет и просто «1». Тогда в шапке не должно остаться
// загадки, чьё это расписание.
func TestGroupLineKeepsGroupName(t *testing.T) {
	tests := []struct {
		name     string
		subgroup string
		want     string
	}{
		{"имя подгруппы содержит группу", "ГР-12/2", "ГР-12/2"},
		{"имя подгруппы само по себе", "1 подгруппа", "ГР-12 · 1 подгруппа"},
		{"без подгруппы", "", "ГР-12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := api.Context{Group: store.Group{Name: "ГР-12"}}
			if tt.subgroup != "" {
				c.Subgroup = &store.Subgroup{ID: 1, Name: tt.subgroup}
			}
			if got := groupLine(c); got != tt.want {
				t.Errorf("groupLine = %q, ожидалось %q", got, tt.want)
			}
		})
	}
}

func TestChangeDetailsIncludeDecisionFields(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*schedule.Item)
	}{
		{"remote", "дистанционно", func(it *schedule.Item) { it.Flags |= schedule.FlagRemote }},
		{"self study", "самоподготовка", func(it *schedule.Item) { it.Flags |= schedule.FlagSelfWork }},
		{"comment", "ссылка &lt;новая&gt;", func(it *schedule.Item) { it.Comments = "ссылка <новая>" }},
		{"end time", "09:30", func(it *schedule.Item) { it.MinuteTo = 570 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := item(1, 510, 605, "08:30 - 10:05", "Химия")
			after := before
			tc.change(&after)
			r := api.ChangeDayResponse{Context: dayResponse().Context, Date: "2025-09-19", Known: true,
				Before: dayResponse(before).Day, After: dayResponse(after).Day}
			out := FormatChangeDay(r)
			if strings.Contains(out, "Тебя правка не задела") || !strings.Contains(out, tc.want) || !strings.Contains(out, "➕") {
				t.Fatalf("важная правка скрыта: %s", out)
			}
		})
	}
}

func TestRemoteVisibleInCompactViews(t *testing.T) {
	it := item(1, 510, 605, "08:30 - 10:05", "Химия")
	it.Flags = schedule.FlagRemote
	d := dayResponse(it)
	week := FormatWeek(api.WeekResponse{Context: d.Context, Week: schedule.Week{Monday: d.Day.Date, Days: []schedule.Day{d.Day}}})
	now := FormatNow(api.NowResponse{Context: d.Context, Day: d.Day, Now: schedule.ComputeNow(d.Day, time.Date(2025, 9, 19, 9, 0, 0, 0, time.UTC))})
	for _, out := range []string{week, now} {
		if !strings.Contains(out, "дистанционно") {
			t.Fatalf("формат пары скрыт: %s", out)
		}
	}
}

func TestIncompleteWeekDoesNotPromiseEmptyDays(t *testing.T) {
	r := api.WeekResponse{Context: dayResponse().Context, Freshness: api.Freshness{Missing: true}, Week: schedule.Week{Days: []schedule.Day{
		dayResponse(item(1, 510, 605, "08:30 - 10:05", "Химия")).Day,
		{Date: "2025-09-20", Workday: true},
	}}}
	out := FormatWeek(r)
	if strings.Contains(out, "занятий нет") || !strings.Contains(out, "нет полных данных") {
		t.Fatal(out)
	}
}

func TestUnknownSlotNumberIsHidden(t *testing.T) {
	for _, flags := range []schedule.Flags{0, schedule.FlagEmpty} {
		it := item(0, 510, 600, "08:30 – 10:00", "Химия")
		it.Flags = flags
		r := dayResponse(it)
		day := FormatDay(r)
		week := FormatWeek(api.WeekResponse{Context: r.Context, Week: schedule.Week{Days: []schedule.Day{r.Day}}})
		if strings.Contains(day, "0 пара") || strings.Contains(week, "<blockquote>0 ·") {
			t.Fatalf("Нулевой номер: %s\n%s", day, week)
		}
		now := FormatNow(api.NowResponse{Context: r.Context, Day: r.Day, Now: schedule.Now{At: time.Date(2025, 9, 19, 9, 0, 0, 0, time.UTC), Current: &it, Next: &it}})
		if strings.Contains(now, "0 пара") {
			t.Fatal(now)
		}
		for _, line := range changeLines(r.Day, 0) {
			if strings.HasPrefix(line, "0 ·") {
				t.Fatal(line)
			}
		}
		if !strings.Contains(day, "08:30") {
			t.Fatal(day)
		}
	}
}

func TestFreshnessUsesProfileTimezone(t *testing.T) {
	old := profile.Current()
	t.Cleanup(func() { profile.Set(old) })
	p := old
	p.Timezone = "Asia/Kolkata"
	profile.Set(p)
	var text strings.Builder
	writeFreshness(&text, api.Freshness{Stale: true, FetchedAt: time.Date(2026, 9, 15, 22, 0, 0, 0, time.UTC)})
	if !strings.Contains(text.String(), "16.09 03:30") {
		t.Fatal(text.String())
	}
}
