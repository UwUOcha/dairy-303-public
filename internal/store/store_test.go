package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("открытие базы: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fixNow фиксирует «сегодня» для решений о дубликатах: без этого тесты про
// каталог начинали бы зависеть от того, какой сейчас месяц на машине.
func fixNow(t *testing.T, iso string) {
	t.Helper()
	ts, err := time.Parse("2006-01-02", iso)
	if err != nil {
		t.Fatalf("разбор даты %q: %v", iso, err)
	}
	prev := nowFunc
	nowFunc = func() time.Time { return ts }
	t.Cleanup(func() { nowFunc = prev })
}

func testTree() importdata.GroupTree {
	return importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Учебный факультет"}},
		Groups: []importdata.Group{
			{ID: 231, Name: "ГР-11", DepartmentID: 10, Course: 1, Active: true},
			{ID: 232, Name: "ГР-12", DepartmentID: 10, Course: 1, Active: true},
			{ID: 859, Name: "М-ИСиТ-11", DepartmentID: 10, GradYear: 2028, Active: false},
		},
	}
}

var testTimes = []importdata.LessonTime{
	{ID: 1, MinuteFrom: 8*60 + 30, MinuteTo: 10*60 + 5, Label: "08:30 - 10:05"},
	{ID: 2, MinuteFrom: 10*60 + 25, MinuteTo: 12 * 60, Label: "10:25 - 12:00"},
}

func TestNormalizeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ГР-12", "гр12"},
		{"г р 12", "гр12"},
		{"Б-Арх-11", "барх11"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeName(tt.in); got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, ожидалось %q", tt.in, got, tt.want)
		}
	}
}

func TestSaveGroupTreeAndSearch(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatalf("сохранение дерева: %v", err)
	}

	// Поиск переживает любой способ набрать название.
	for _, q := range []string{"ГР-12", "гр12", "г р 12"} {
		got, err := db.SearchGroups(ctx, q, 10)
		if err != nil {
			t.Fatalf("поиск %q: %v", q, err)
		}
		if len(got) == 0 || got[0].Name != "ГР-12" {
			t.Errorf("поиск %q дал %+v, ожидалась ГР-12 первой", q, got)
		}
	}

	// Совпадение с начала важнее совпадения в середине.
	got, err := db.SearchGroups(ctx, "гр", 10)
	if err != nil {
		t.Fatalf("поиск: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("найдено %d групп, ожидалось 2", len(got))
	}

	// Архивные группы в поиск не попадают, но остаются в базе.
	if got, _ := db.SearchGroups(ctx, "мисит", 10); len(got) != 0 {
		t.Errorf("архивная группа не должна находиться поиском: %+v", got)
	}
	g, err := db.Group(ctx, 859)
	if err != nil {
		t.Fatalf("архивная группа по id: %v", err)
	}
	if g.Active || g.GradYear != 2028 {
		t.Errorf("архивная группа = %+v", g)
	}

	if _, err := db.Group(ctx, 999999); err != ErrNotFound {
		t.Errorf("несуществующая группа: %v, ожидалось ErrNotFound", err)
	}
}

// Группа, пропавшая из ответа upstream, помечается неактивной, а не удаляется:
// на неё могут ссылаться пользователи и уже загруженные занятия.
func TestSaveGroupTreeDeactivatesMissing(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	shrunk := testTree()
	shrunk.Groups = shrunk.Groups[:1] // осталась только 231
	if err := db.SaveGroupTree(ctx, shrunk); err != nil {
		t.Fatal(err)
	}

	g, err := db.Group(ctx, 232)
	if err != nil {
		t.Fatalf("группа 232 исчезла из базы: %v", err)
	}
	if g.Active {
		t.Error("пропавшая из ответа группа должна стать неактивной")
	}
	ids, err := db.ActiveGroupIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 231 {
		t.Errorf("активные группы = %v, ожидалось [231]", ids)
	}
}

// Ради этого теста и менялась схема: потоковое занятие приходит с одним и тем
// же id в ленты всех групп потока. При lessons.group_id как в первоначальном
// наброске вторая запись затёрла бы первую.
func TestFlowLessonSharedBetweenGroups(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	// Занятие потока: якорь — группа 231, а приходит оно и в ленту 232.
	flowLesson := importdata.Lesson{
		ID: 38355, Date: "2025-09-16", LessonTimeID: 1,
		Discipline: "Введение в специальность", ClassType: "Лек", Classroom: "к. 1/312",
		Staff:         []importdata.Staff{{ID: 101, Name: "Иванов И.И."}},
		Audience:      importdata.AudienceFlow,
		GroupID:       231,
		FlowNumber:    1,
		AudienceLabel: "Поток 1 (ГР-11, ГР-12)",
	}
	own232 := importdata.Lesson{
		ID: 38783, Date: "2025-09-16", LessonTimeID: 2,
		Discipline: "Русский язык", ClassType: "Пр", Classroom: "к. 1/907",
		Audience: importdata.AudienceGroup, GroupID: 232, AudienceLabel: "ГР-12",
	}

	for _, ms := range []importdata.MonthSchedule{
		{GroupID: 231, Year: 2025, Month: 9, Lessons: []importdata.Lesson{flowLesson}, LessonTimes: testTimes},
		{GroupID: 232, Year: 2025, Month: 9, Lessons: []importdata.Lesson{flowLesson, own232}, LessonTimes: testTimes},
	} {
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatalf("сохранение месяца группы %d: %v", ms.GroupID, err)
		}
	}

	// Обе группы видят потоковое занятие.
	for _, groupID := range []int64{231, 232} {
		got, err := db.Lessons(ctx, groupID, "2025-09-01", "2025-09-30")
		if err != nil {
			t.Fatalf("чтение занятий группы %d: %v", groupID, err)
		}
		var found bool
		for _, l := range got {
			if l.ID == 38355 {
				found = true
				if l.Discipline != "Введение в специальность" || l.Classroom != "к. 1/312" {
					t.Errorf("группа %d: занятие потока приехало битым: %+v", groupID, l)
				}
				if len(l.Staff) != 1 || l.Staff[0] != "Иванов И.И." {
					t.Errorf("группа %d: преподаватели = %v", groupID, l.Staff)
				}
				if l.Audience != schedule.AudienceFlow {
					t.Errorf("группа %d: Audience = %d", groupID, l.Audience)
				}
			}
		}
		if !found {
			t.Errorf("группа %d не видит потокового занятия 38355", groupID)
		}
	}

	// Своя пара группы 232 в ленту 231 попасть не должна.
	got231, _ := db.Lessons(ctx, 231, "2025-09-01", "2025-09-30")
	if len(got231) != 1 {
		t.Errorf("у группы 231 занятий %d, ожидалось 1", len(got231))
	}

	// Занятие хранится в одном экземпляре.
	var count int
	if err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM lessons WHERE id = 38355`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("занятие потока лежит в %d экземплярах, ожидался 1", count)
	}
}

func TestSaveMonthChangeDetection(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	// «Сегодня» решает, считается ли правка достойной новости, поэтому день
	// фиксируется: иначе тест начал бы зависеть от даты на машине.
	fixNow(t, "2025-09-15")
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	base := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{{
			ID: 1, Date: "2025-09-16", LessonTimeID: 1,
			Discipline: "Химия", ClassType: "Лб", Classroom: "к. 2/423",
			Audience: importdata.AudienceGroup, GroupID: 232,
		}},
	}

	// Первая загрузка изменением не считается: сообщать подписчикам нечего.
	changed, err := db.SaveMonth(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("первая загрузка месяца не должна помечаться изменением")
	}

	// Тот же контент — не изменение.
	changed, err = db.SaveMonth(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("повторная загрузка того же контента помечена изменением")
	}

	// Переезд в другую аудиторию — изменение.
	moved := base
	moved.Lessons = []importdata.Lesson{base.Lessons[0]}
	moved.Lessons[0].Classroom = "к. 2/500"
	changed, err = db.SaveMonth(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("смена аудитории должна помечаться изменением")
	}

	got, _ := db.Lessons(ctx, 232, "2025-09-01", "2025-09-30")
	if len(got) != 1 || got[0].Classroom != "к. 2/500" {
		t.Errorf("занятия после обновления = %+v", got)
	}

	// Комментарий показывается в расписании, поэтому его изменение обязано
	// попадать и в базу, и в отпечаток будущих занятий.
	commented := moved
	commented.Lessons = []importdata.Lesson{moved.Lessons[0]}
	commented.Lessons[0].Comments = "Вход со двора"
	changed, err = db.SaveMonth(ctx, commented)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("смена комментария должна помечаться изменением")
	}
	got, _ = db.Lessons(ctx, 232, "2025-09-01", "2025-09-30")
	if len(got) != 1 || got[0].Comments != "Вход со двора" {
		t.Errorf("комментарий после обновления = %+v", got)
	}
}

// Сетка звонков может измениться без изменения id слотов и самих занятий.
// Быстрый путь одинакового отпечатка всё равно обязан её сохранить.
func TestSaveMonthUpdatesLessonTimesWithoutLessonChanges(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	month := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{{
			ID: 1, Date: "2025-09-16", LessonTimeID: 1, Discipline: "Химия", GroupID: 232,
		}},
	}
	if _, err := db.SaveMonth(ctx, month); err != nil {
		t.Fatal(err)
	}

	updated := append([]importdata.LessonTime(nil), testTimes...)
	updated[0] = importdata.LessonTime{ID: 1, MinuteFrom: 9 * 60, MinuteTo: 10*60 + 35, Label: "09:00 - 10:35"}
	month.LessonTimes = updated
	if changed, err := db.SaveMonth(ctx, month); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Error("смена общей сетки не должна выглядеть как правка ленты одной группы")
	}

	grid, err := db.Grid(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(grid.Times) == 0 || grid.Times[0].MinuteFrom != 9*60 || grid.Times[0].Label != "09:00 - 10:35" {
		t.Errorf("сетка звонков не обновилась: %+v", grid.Times)
	}
}

// Правка прошедшего дня — не новость. Вуз постоянно дописывает темы и меняет
// аудитории задним числом, и на пару, которая была две недели назад, человек
// уже не пойдёт: сообщение о ней — чистый шум. В базу правка при этом обязана
// доехать, иначе локальная копия начнёт расходиться с вузом.
func TestPastDayChangeIsNotNews(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	fixNow(t, "2025-09-15")
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	lesson := func(date, classroom string) importdata.MonthSchedule {
		return importdata.MonthSchedule{
			GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
			Lessons: []importdata.Lesson{{
				ID: 1, Date: date, LessonTimeID: 1, Discipline: "Химия",
				Classroom: classroom, Audience: importdata.AudienceGroup, GroupID: 232,
			}},
		}
	}

	if _, err := db.SaveMonth(ctx, lesson("2025-09-01", "к. 2/423")); err != nil {
		t.Fatal(err)
	}
	changed, err := db.SaveMonth(ctx, lesson("2025-09-01", "к. 2/500"))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("правка прошедшего дня не должна поднимать новость")
	}
	got, _ := db.Lessons(ctx, 232, "2025-09-01", "2025-09-30")
	if len(got) != 1 || got[0].Classroom != "к. 2/500" {
		t.Errorf("правка прошедшего дня не доехала до базы: %+v", got)
	}

	// Сегодняшний день уже считается будущим: пара ещё впереди.
	if _, err := db.SaveMonth(ctx, lesson("2025-09-15", "к. 2/423")); err != nil {
		t.Fatal(err)
	}
	changed, err = db.SaveMonth(ctx, lesson("2025-09-15", "к. 2/500"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("правка сегодняшнего дня — новость")
	}
}

// Окно «что считать будущим» едет вперёд каждый день, и само по себе это не
// изменение расписания. Без сравнения по одной и той же границе первый же
// синк после полуночи разослал бы новость всей базе.
func TestMovingWindowIsNotChange(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	fixNow(t, "2025-09-15")
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	month := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			{ID: 1, Date: "2025-09-15", LessonTimeID: 1, Discipline: "Химия", GroupID: 232},
			{ID: 2, Date: "2025-09-16", LessonTimeID: 1, Discipline: "Физика", GroupID: 232},
		},
	}
	if _, err := db.SaveMonth(ctx, month); err != nil {
		t.Fatal(err)
	}

	// Наступило завтра: пара 15-го уехала за границу окна, расписание — нет.
	fixNow(t, "2025-09-16")
	changed, err := db.SaveMonth(ctx, month)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("съехавшее окно принято за правку расписания")
	}

	// И правка после этого по-прежнему ловится.
	moved := month
	moved.Lessons = []importdata.Lesson{month.Lessons[0], month.Lessons[1]}
	moved.Lessons[1].Classroom = "к. 2/500"
	changed, err = db.SaveMonth(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("правка будущего дня после сдвига окна не поймана")
	}
}

// Порядок занятий в ответе upstream не гарантирован, а отпечаток обязан быть
// устойчивым — иначе каждый синк выглядел бы как правка расписания.
func TestContentHashOrderIndependent(t *testing.T) {
	a := importdata.Lesson{ID: 1, Date: "2025-09-16", Discipline: "Химия"}
	b := importdata.Lesson{ID: 2, Date: "2025-09-16", Discipline: "Физика"}
	if ContentHash([]importdata.Lesson{a, b}) != ContentHash([]importdata.Lesson{b, a}) {
		t.Error("отпечаток зависит от порядка занятий")
	}
	if ContentHash([]importdata.Lesson{a}) == ContentHash([]importdata.Lesson{b}) {
		t.Error("разные занятия дали одинаковый отпечаток")
	}
	if ContentHash(nil) != ContentHash([]importdata.Lesson{}) {
		t.Error("пустой месяц должен давать стабильный отпечаток")
	}
}

func TestChangingGroupDropsQueuedChanges(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	u := User{Platform: "tg", ExtID: "42", GroupID: 232, Notify: true, Changes: true}
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if n, err := db.EnqueueChange(ctx, 232, 2025, 9); err != nil || n != 1 {
		t.Fatalf("постановка новости в очередь: n=%d err=%v", n, err)
	}

	// Обычное сохранение настроек той же группы очередь не затрагивает.
	u.Morning = true
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if items, err := db.TakeOutbox(ctx, "tg", 10); err != nil || len(items) != 1 {
		t.Fatalf("очередь исчезла без смены группы: items=%v err=%v", items, err)
	}

	u.GroupID = 231
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if items, err := db.TakeOutbox(ctx, "tg", 10); err != nil || len(items) != 0 {
		t.Errorf("после смены группы остались старые новости: items=%v err=%v", items, err)
	}
}

// Занятие, исчезнувшее из всех лент, не должно оставаться в базе мусором.
func TestSaveMonthCollectsGarbage(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	full := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			{ID: 1, Date: "2025-09-16", LessonTimeID: 1, Discipline: "Химия", GroupID: 232,
				Staff: []importdata.Staff{{ID: 5, Name: "Смирнов С.С."}}},
			{ID: 2, Date: "2025-09-17", LessonTimeID: 1, Discipline: "Физика", GroupID: 232},
		},
	}
	if _, err := db.SaveMonth(ctx, full); err != nil {
		t.Fatal(err)
	}

	// Вторую пару отменили.
	trimmed := full
	trimmed.Lessons = full.Lessons[:1]
	if _, err := db.SaveMonth(ctx, trimmed); err != nil {
		t.Fatal(err)
	}

	var lessons, links, staffLinks int
	db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM lessons`).Scan(&lessons)
	db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_lessons`).Scan(&links)
	db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM lesson_staff`).Scan(&staffLinks)
	if lessons != 1 || links != 1 {
		t.Errorf("после уборки: занятий %d, связей %d, ожидалось 1 и 1", lessons, links)
	}
	if staffLinks != 1 {
		t.Errorf("связей с преподавателями %d, ожидалась 1", staffLinks)
	}
}

// Потоковое занятие, выпавшее из ленты одной группы, обязано остаться у
// остальных участников потока.
func TestGarbageCollectionKeepsSharedLesson(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	shared := importdata.Lesson{ID: 100, Date: "2025-09-16", LessonTimeID: 1,
		Discipline: "Лекция потока", Audience: importdata.AudienceFlow, GroupID: 231, FlowNumber: 1}

	for _, gid := range []int64{231, 232} {
		if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
			GroupID: gid, Year: 2025, Month: 9,
			Lessons: []importdata.Lesson{shared}, LessonTimes: testTimes,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Группу 232 вывели из потока — её лента опустела.
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
	}); err != nil {
		t.Fatal(err)
	}

	got231, err := db.Lessons(ctx, 231, "2025-09-01", "2025-09-30")
	if err != nil {
		t.Fatal(err)
	}
	if len(got231) != 1 {
		t.Fatalf("группа 231 потеряла общее занятие: %+v", got231)
	}
	got232, _ := db.Lessons(ctx, 232, "2025-09-01", "2025-09-30")
	if len(got232) != 0 {
		t.Errorf("у группы 232 осталось %d занятий, ожидалось 0", len(got232))
	}
}

func TestGridAndWorkdays(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveWorkdays(ctx, map[int][]int64{1: {1}, 2: {1}, 3: {1}, 4: {1}, 5: {1}, 6: {1}}); err != nil {
		t.Fatal(err)
	}

	g, err := db.Grid(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Times) != 2 {
		t.Fatalf("строк сетки %d, ожидалось 2", len(g.Times))
	}
	if g.Times[0].Number != 1 || g.Times[1].Number != 2 {
		t.Errorf("нумерация пар: %+v", g.Times)
	}
	if g.NumberOf(2) != 2 || g.NumberOf(999) != 0 {
		t.Error("NumberOf работает неверно")
	}
	if len(g.Workdays) != 6 || g.Workdays[7] {
		t.Errorf("учебные дни = %v", g.Workdays)
	}
}

func TestUsers(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	if _, err := db.User(ctx, "tg", "1"); err != ErrNotFound {
		t.Errorf("новый пользователь: %v, ожидалось ErrNotFound", err)
	}

	u := User{Platform: "tg", ExtID: "1", GroupID: 232, SubgroupID: 335}.WithDefaults(180)
	u.Platform, u.ExtID, u.GroupID, u.SubgroupID = "tg", "1", 232, 335
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	got, err := db.User(ctx, "tg", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupID != 232 || got.SubgroupID != 335 {
		t.Errorf("пользователь = %+v", got)
	}
	// Новичок получает утреннее расписание, ничего не настраивая, а вечернее и
	// пустые дни — только по своей воле.
	if !got.Notify || !got.Morning || got.MorningAt != DefaultMorningAt {
		t.Errorf("утро по умолчанию не включено: %+v", got)
	}
	if got.Evening || got.EmptyDays {
		t.Errorf("вечер и пустые дни по умолчанию должны быть выключены: %+v", got)
	}
	if !got.Changes {
		t.Error("новости о правках по умолчанию должны приходить")
	}
	if !got.Configured() {
		t.Error("пользователь с группой должен считаться настроенным")
	}

	// «Горячие» группы — это ровно те, к которым привязаны пользователи.
	hot, err := db.HotGroupIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hot) != 1 || hot[0] != 232 {
		t.Errorf("горячие группы = %v, ожидалось [232]", hot)
	}

	// Время рассылки хранится в локальных минутах, а тик идёт по UTC:
	// 07:00 при смещении +180 наступает в 04:00 UTC.
	toNotify, err := db.UsersToNotify(ctx, 4*60)
	if err != nil {
		t.Fatal(err)
	}
	if len(toNotify) != 1 || toNotify[0].Kind != NotifyMorning {
		t.Errorf("к рассылке %+v, ожидалось одно утреннее", toNotify)
	}
	if none, _ := db.UsersToNotify(ctx, 7*60); len(none) != 0 {
		t.Error("локальная минута не должна совпадать с минутой UTC")
	}
	if late, _ := db.UsersToNotify(ctx, 4*60+1); len(late) != 1 {
		t.Error("после пропущенного тика рассылка должна быть доступна для повтора")
	}

	// Вечернее сообщение — отдельная рассылка со своим временем и своей
	// отметкой: включённое утро не должно её ни включать, ни блокировать.
	u.Evening, u.EveningAt = true, 18*60+30
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	eve, err := db.UsersToNotify(ctx, 15*60+30)
	if err != nil {
		t.Fatal(err)
	}
	if len(eve) != 1 || eve[0].Kind != NotifyEvening {
		t.Fatalf("вечерняя рассылка = %+v", eve)
	}

	// Отметка об отправке защищает от повторного сообщения — и делает это
	// порознь для утра и вечера.
	if err := db.MarkNotified(ctx, "tg", "1", NotifyMorning, "2025-09-19"); err != nil {
		t.Fatal(err)
	}
	again, _ := db.UsersToNotify(ctx, 4*60)
	if len(again) != 1 || !again[0].Done("2025-09-19") {
		t.Errorf("отметка об отправке не вернулась: %+v", again)
	}
	eve, _ = db.UsersToNotify(ctx, 15*60+30)
	if len(eve) != 1 || eve[0].Done("2025-09-19") {
		t.Errorf("утренняя отметка не должна закрывать вечернюю рассылку: %+v", eve)
	}

	// Разовая подсказка про пустые дни отмечается отдельно и не сбрасывается
	// сохранением настроек: иначе человек получал бы её каждую субботу.
	if err := db.MarkEmptyHinted(ctx, "tg", "1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.User(ctx, "tg", "1"); !got.EmptyHinted {
		t.Error("сохранение настроек стёрло отметку о показанной подсказке")
	}

	if err := db.DisableNotify(ctx, "tg", "1"); err != nil {
		t.Fatal(err)
	}
	if off, _ := db.UsersToNotify(ctx, 4*60); len(off) != 0 {
		t.Error("выключенные уведомления не должны попадать в выборку")
	}

	// Выключение одной только утренней рассылки: главный тумблер остаётся
	// включённым, и новости о правках продолжают приходить.
	u.Morning = false
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.User(ctx, "tg", "1"); got.Morning || !got.Notify {
		t.Errorf("утро не выключилось по отдельности: %+v", got)
	}
	if morning, _ := db.UsersToNotify(ctx, 4*60); len(morning) != 0 {
		t.Error("выключенное утро не должно попадать в выборку")
	}
}

// Ожидание ввода времени живёт в базе, а не в памяти botd: перезапуск не
// должен ронять человека на середине настройки.
func TestAwaitSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)

	u := User{Platform: "tg", ExtID: "1", GroupID: 232}.WithDefaults(180)
	u.Platform, u.ExtID, u.GroupID = "tg", "1", 232
	u.Await = AwaitEveningTime
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := db.User(ctx, "tg", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Await != AwaitEveningTime {
		t.Errorf("ожидание ввода = %q", got.Await)
	}
}

func TestSubgroups(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSubgroups(ctx, 232, []importdata.Subgroup{
		{ID: 334, Name: "ГР-12/1"},
		{ID: 335, Name: "ГР-12/2"},
	}); err != nil {
		t.Fatal(err)
	}

	subs, err := db.Subgroups(ctx, 232)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 || subs[0].ID != 334 || subs[1].Name != "ГР-12/2" {
		t.Errorf("подгруппы = %+v", subs)
	}
	if none, _ := db.Subgroups(ctx, 231); len(none) != 0 {
		t.Errorf("у группы без подгрупп нашлось %d", len(none))
	}
}

func TestMonthBounds(t *testing.T) {
	tests := []struct {
		year, month      int
		wantFrom, wantTo string
	}{
		{2025, 9, "2025-09-01", "2025-09-30"},
		{2026, 2, "2026-02-01", "2026-02-28"},
		{2024, 2, "2024-02-01", "2024-02-29"}, // високосный
		{2025, 12, "2025-12-01", "2025-12-31"},
	}
	for _, tt := range tests {
		from, to := MonthBounds(tt.year, tt.month)
		if from != tt.wantFrom || to != tt.wantTo {
			t.Errorf("MonthBounds(%d, %d) = %s..%s, ожидалось %s..%s",
				tt.year, tt.month, from, to, tt.wantFrom, tt.wantTo)
		}
	}
}

// Вуз держит в каталоге ту же группу людей ещё и под названием следующего
// года: одноимённая запись на курс младше и без единого занятия. Показывать
// её человеку нельзя — он выберет пустышку и увидит вечно пустое расписание.

// Свидетельство важнее соглашения об именах: если занятия появились у той
// записи, которую мы прятали, прятать надо другую.

// Свежая установка: каталог уже загружен, расписания ещё нет ни у кого.
// Прятать двойников в этот момент не по чему — фаза каталога считается по
// занятиям, а их ноль. Ровно на этом бот после первого деплоя показывал
// половину групп: угаданное «смещение ноль» прятало настоящую запись каждого
// из 209 задвоенных имён.

// Прошлогодние занятия не делают запись настоящей. В августе вуз
// перенумеровывает группы, и та, что весь год была живой, остаётся с
// расписанием только за прошлый год: показывать надо не её, иначе человек
// привяжется к записи, в которой сентябрь пуст навсегда.

// Пара, о которой не известно ничего: расписание не заведено ни у одной из
// двух записей. Тогда решает курс — но не сам по себе, а с поправкой на фазу
// каталога, которую видно по остальным группам вуза.

// Список копий — материал для ручного переключения в боте: человек должен
// увидеть, у какой записи год уже идёт, а у какой расписание кончилось.

// Уникальные имена прятать не за что.

// Потоковую пару перенесли, а вторая группа потока ещё не синхронизировалась.
// Дата занятия одна на все ленты — иначе у неё пара пропала бы со старого дня
// и не появилась на новом: выборка идёт по дате ленты, а BuildDay сверяет её с
// датой самого занятия и расхождение отбрасывает.
func TestFlowLessonMoveUpdatesAllFeeds(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	flow := importdata.Lesson{
		ID: 38355, Date: "2025-09-16", LessonTimeID: 1,
		Discipline: "Введение в специальность", ClassType: "Лек",
		Audience: importdata.AudienceFlow, GroupID: 231, FlowNumber: 1,
	}
	for _, gid := range []int64{231, 232} {
		if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
			GroupID: gid, Year: 2025, Month: 9,
			Lessons: []importdata.Lesson{flow}, LessonTimes: testTimes,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Вуз перенёс пару на 18-е. Синхронизировалась пока только группа 231.
	moved := flow
	moved.Date = "2025-09-18"
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
		GroupID: 231, Year: 2025, Month: 9,
		Lessons: []importdata.Lesson{moved}, LessonTimes: testTimes,
	}); err != nil {
		t.Fatal(err)
	}

	for _, gid := range []int64{231, 232} {
		old, err := db.Lessons(ctx, gid, "2025-09-16", "2025-09-16")
		if err != nil {
			t.Fatal(err)
		}
		if len(old) != 0 {
			t.Errorf("группа %d: пара осталась на 16-м, хотя её перенесли", gid)
		}

		got, err := db.Lessons(ctx, gid, "2025-09-18", "2025-09-18")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("группа %d: на 18-м занятий %d, ожидалось 1", gid, len(got))
		}
		if got[0].Date != "2025-09-18" {
			t.Errorf("группа %d: дата ленты разошлась с датой занятия (%s)", gid, got[0].Date)
		}
	}
}

// Очередь исходящих переживает перезапуск обоих демонов, поэтому её поведение
// проверяется на уровне хранилища.
func TestOutboxLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	// Первые двое подписаны, третий выключил уведомления целиком, четвёртый —
	// из другой группы. Пятый оставил уведомления, но отказался от новостей о
	// правках: их и проверяет отдельный флаг.
	on := func(platform, extID string, group int64) User {
		u := User{}.WithDefaults(180)
		u.Platform, u.ExtID, u.GroupID = platform, extID, group
		return u
	}
	off := on("tg", "3", 232)
	off.Notify = false
	noChanges := on("tg", "5", 232)
	noChanges.Changes = false
	for _, u := range []User{
		on("tg", "1", 232),
		on("vk", "2", 232),
		off,
		on("tg", "4", 231),
		noChanges,
	} {
		if err := db.SaveUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	n, err := db.EnqueueChange(ctx, 232, 2025, 9)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("адресатов = %d, ожидалось 2: подписчики именно этой группы", n)
	}

	// Платформы разгребают очередь порознь.
	tg, err := db.TakeOutbox(ctx, "tg", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tg) != 1 || tg[0].ExtID != "1" {
		t.Fatalf("телеграму выдано %+v", tg)
	}

	// Неудачи копятся, и на исчерпании попыток сообщение выбрасывается: новость
	// о правке расписания недельной давности никому не нужна.
	id := tg[0].ID
	for i := 1; i <= OutboxMaxAttempts; i++ {
		kept, err := db.OutboxFail(ctx, id, -time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if want := i < OutboxMaxAttempts; kept != want {
			t.Errorf("после %d-й неудачи в очереди = %v, ожидалось %v", i, kept, want)
		}
	}
	left, err := db.TakeOutbox(ctx, "tg", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("в очереди осталось %d безнадёжных сообщений", len(left))
	}
}

// Миграция существующей базы должна давать ту же схему, что и установка с нуля.
func TestMigrationFromV2(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	// Собираем базу версии 2: актуальная схема без всего, что добавили третья,
	// четвёртая и пятая. Уведомления там — одна колонка notify_at, где NULL
	// значит «не слать», а число — минуты утреннего сообщения.
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.tx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			`DROP TABLE outbox`,
			`DROP TABLE change_days`,
			`DROP TABLE feedback`,
			`ALTER TABLE users DROP COLUMN menu_sent`,
			`ALTER TABLE users DROP COLUMN menu_version`,
			`ALTER TABLE month_state DROP COLUMN future_hash`,
			`ALTER TABLE month_state DROP COLUMN future_from`,
			`DROP INDEX idx_users_notify`,
			`DROP INDEX idx_users_probe`,
		}
		for _, col := range []string{
			"notify_on", "morning_on", "morning_at", "evening_on", "evening_at",
			"empty_on", "empty_hinted", "changes_on", "notified_eve_on", "await",
			"blocked_at", "probed_at",
		} {
			stmts = append(stmts, `ALTER TABLE users DROP COLUMN `+col)
		}
		stmts = append(stmts,
			`ALTER TABLE users ADD COLUMN notify_at INTEGER`,
			`CREATE INDEX idx_users_notify ON users(notify_at) WHERE notify_at IS NOT NULL`,
			// Двое: один с включённой рассылкой в 07:30, другой без неё.
			`INSERT INTO users(platform, ext_id, group_id, subgroup_id, tz_offset, notify_at,
			                   notified_on, created_at, last_seen)
			 VALUES('tg', '1', 232, 0, 180, 450, '', 0, 0),
			       ('tg', '2', 232, 0, 180, NULL, '', 0, 0)`,
			legacyStaffSchema,
			`ALTER TABLE users DROP COLUMN tz_name; PRAGMA user_version = 2`,
		)
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: "3", GroupID: 232, TZOffset: 180}); err == nil {
		t.Fatal("запись со свежим кодом в старую схему должна была не пройти")
	}
	db.Close()

	fixNow(t, "2025-09-15")

	// Повторное открытие догоняет схему до текущей версии.
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("миграция 2→8: %v", err)
	}
	defer db.Close()

	if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: "3", GroupID: 232, TZOffset: 180}); err != nil {
		t.Fatalf("после миграции пользователь не сохраняется: %v", err)
	}
	if err := db.MarkMenuSent(ctx, "tg", "1"); err != nil {
		t.Fatalf("после миграции нет колонки menu_sent: %v", err)
	}
	if _, err := db.TakeOutbox(ctx, "tg", 1); err != nil {
		t.Fatalf("после миграции нет таблицы outbox: %v", err)
	}
	if _, err := db.ChangedDates(ctx, 232, "2025-09-15"); err != nil {
		t.Fatalf("после миграции нет таблицы change_days: %v", err)
	}
	if _, _, err := db.AddFeedback(ctx, Feedback{Platform: "tg", ExtID: "1", Text: "проверка"}); err != nil {
		t.Fatalf("после миграции нет таблицы feedback: %v", err)
	}
	// Никого ещё не проверяли, поэтому в очередь на проверку попадают оба
	// настроенных пользователя старой базы.
	if targets, err := db.UsersToProbe(ctx, "tg", 10, time.Now()); err != nil {
		t.Fatalf("после миграции нет учёта блокировок: %v", err)
	} else if len(targets) == 0 {
		t.Error("после миграции никто не попал в очередь на проверку")
	}

	// Выбранное время переезжает в morning_at, а не сбрасывается на семь утра:
	// человек его настраивал, и менять его молча нельзя.
	subscriber, err := db.User(ctx, "tg", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !subscriber.Notify || !subscriber.Morning || subscriber.MorningAt != 450 {
		t.Errorf("подписчик после миграции = %+v", subscriber)
	}
	// Пустые дни выключаются всем — это и есть смена поведения.
	if subscriber.EmptyDays {
		t.Error("уведомления о пустых днях должны быть выключены после миграции")
	}
	if !subscriber.Changes {
		t.Error("подписчик должен и дальше получать новости о правках")
	}

	// Кто рассылку не включал — тому бот и дальше молчит.
	silent, err := db.User(ctx, "tg", "2")
	if err != nil {
		t.Fatal(err)
	}
	if silent.Notify {
		t.Errorf("выключенная рассылка после миграции включилась: %+v", silent)
	}

	// Отпечаток будущего появился у месяцев, записанных до миграции: первое же
	// сравнение после обновления обязано пройти, а не поднять ложную новость.
	month := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			{ID: 1, Date: "2025-09-16", LessonTimeID: 1, Discipline: "Химия", GroupID: 232},
		},
	}
	if _, err := db.SaveMonth(ctx, month); err != nil {
		t.Fatal(err)
	}
	changed, err := db.SaveMonth(ctx, month)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("после миграции тот же месяц принят за изменённый")
	}
}

// Правка расписания должна не только поднимать флаг «изменилось», но и
// оставлять после себя, что именно изменилось: список дней и снимок «до».
func TestSaveMonthRecordsChangedDays(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	fixNow(t, "2025-09-15")
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	base := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			// Прошедший день: его правку человеку показывать незачем.
			{ID: 1, Date: "2025-09-10", LessonTimeID: 1, Discipline: "Анатомия", Classroom: "101", GroupID: 232},
			{ID: 2, Date: "2025-09-18", LessonTimeID: 1, Discipline: "Химия", Classroom: "305", GroupID: 232},
			{ID: 3, Date: "2025-09-19", LessonTimeID: 1, Discipline: "Физика", Classroom: "210", GroupID: 232},
		},
	}
	if changed, err := db.SaveMonth(ctx, base); err != nil || changed {
		t.Fatalf("первая загрузка месяца: changed=%v err=%v — новостью она не является", changed, err)
	}

	// Одну пару переносят в другую аудиторию, вторую отменяют, а в прошедшем
	// дне вуз задним числом правит аудиторию.
	next := base
	next.Lessons = []importdata.Lesson{
		{ID: 1, Date: "2025-09-10", LessonTimeID: 1, Discipline: "Анатомия", Classroom: "102", GroupID: 232},
		{ID: 2, Date: "2025-09-18", LessonTimeID: 1, Discipline: "Химия", Classroom: "401", GroupID: 232},
	}
	changed, err := db.SaveMonth(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("перенос пары в будущем не признан изменением")
	}

	dates, err := db.ChangedDates(ctx, 232, "2025-09-15")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2025-09-18", "2025-09-19"}
	if len(dates) != len(want) {
		t.Fatalf("изменённые дни = %v, ожидались %v: прошедший день в список не попадает", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Fatalf("изменённые дни = %v, ожидались %v", dates, want)
		}
	}

	// Снимок «до» помнит именно ту аудиторию, которую человек видел.
	before, err := db.ChangedBefore(ctx, 232, "2025-09-18")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Classroom != "305" {
		t.Errorf("снимок дня до правки = %+v, ожидалась Химия в 305", before)
	}
	// Отменённая пара тоже должна найтись в «до»: иначе показать, что она
	// пропала, будет нечем.
	if before, err := db.ChangedBefore(ctx, 232, "2025-09-19"); err != nil || len(before) != 1 {
		t.Errorf("снимок отменённого дня = %+v, err=%v", before, err)
	}

	// Повторная правка того же дня снимок не перетирает: «до» — это то, что
	// человек видел последним, а не предыдущая правка.
	again := base
	again.Lessons = []importdata.Lesson{
		{ID: 2, Date: "2025-09-18", LessonTimeID: 1, Discipline: "Химия", Classroom: "500", GroupID: 232},
	}
	if _, err := db.SaveMonth(ctx, again); err != nil {
		t.Fatal(err)
	}
	if before, err := db.ChangedBefore(ctx, 232, "2025-09-18"); err != nil || before[0].Classroom != "305" {
		t.Errorf("после второй правки снимок = %+v, ожидалась исходная 305", before)
	}
}

// Сообщение о правке должно нести дни, а повтор — обновлять их у сообщения,
// которое ещё лежит в очереди.
func TestEnqueueChangeCarriesDays(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	fixNow(t, "2025-09-15")
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveUser(ctx, User{
		Platform: "tg", ExtID: "1", GroupID: 232, Notify: true, Changes: true,
	}); err != nil {
		t.Fatal(err)
	}

	base := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			{ID: 2, Date: "2025-09-18", LessonTimeID: 1, Discipline: "Химия", GroupID: 232},
			{ID: 3, Date: "2025-09-19", LessonTimeID: 1, Discipline: "Физика", GroupID: 232},
		},
	}
	if _, err := db.SaveMonth(ctx, base); err != nil {
		t.Fatal(err)
	}

	moved := base
	moved.Lessons = []importdata.Lesson{
		{ID: 2, Date: "2025-09-18", LessonTimeID: 2, Discipline: "Химия", GroupID: 232},
		{ID: 3, Date: "2025-09-19", LessonTimeID: 1, Discipline: "Физика", GroupID: 232},
	}
	if _, err := db.SaveMonth(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if n, err := db.EnqueueChange(ctx, 232, 2025, 9); err != nil || n != 1 {
		t.Fatalf("постановка в очередь: n=%d err=%v", n, err)
	}

	payload := func() ChangePayload {
		t.Helper()
		items, err := db.TakeOutbox(ctx, "tg", 10)
		if err != nil || len(items) != 1 {
			t.Fatalf("очередь = %+v, err=%v", items, err)
		}
		var p ChangePayload
		if err := json.Unmarshal([]byte(items[0].Payload), &p); err != nil {
			t.Fatalf("разбор payload: %v", err)
		}
		return p
	}

	if p := payload(); len(p.Days) != 1 || p.Days[0] != 18 {
		t.Fatalf("в сообщении дни %v, ожидался один — 18", p.Days)
	}

	// Вторая правка, пока первое сообщение не доставлено: адресат должен
	// увидеть оба дня, а не только те, что были известны в первый раз.
	second := base
	second.Lessons = []importdata.Lesson{
		{ID: 2, Date: "2025-09-18", LessonTimeID: 2, Discipline: "Химия", GroupID: 232},
	}
	if _, err := db.SaveMonth(ctx, second); err != nil {
		t.Fatal(err)
	}
	if n, err := db.EnqueueChange(ctx, 232, 2025, 9); err != nil || n != 0 {
		t.Fatalf("повтор добавил %d сообщений (err=%v), ожидалось 0", n, err)
	}
	p := payload()
	if len(p.Days) != 2 || p.Days[0] != 18 || p.Days[1] != 19 {
		t.Errorf("после второй правки дни = %v, ожидались 18 и 19", p.Days)
	}
	if p.Date(18) != "2025-09-18" {
		t.Errorf("дата дня 18 = %q", p.Date(18))
	}
}

// Снимки не должны копиться: своё они отживают за трое суток, а прошедший
// день бесполезен и раньше.
func TestPurgeChangedDays(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	fixNow(t, "2025-09-15")
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	base := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			{ID: 2, Date: "2025-09-18", LessonTimeID: 1, Discipline: "Химия", GroupID: 232},
		},
	}
	if _, err := db.SaveMonth(ctx, base); err != nil {
		t.Fatal(err)
	}
	empty := base
	empty.Lessons = nil
	if _, err := db.SaveMonth(ctx, empty); err != nil {
		t.Fatal(err)
	}
	if dates, err := db.ChangedDates(ctx, 232, "2025-09-15"); err != nil || len(dates) != 1 {
		t.Fatalf("снимок не сохранился: %v %v", dates, err)
	}

	// Уборка «на следующей неделе»: день уже прошёл.
	if err := db.PurgeChangedDays(ctx, 72*time.Hour, "2025-09-22"); err != nil {
		t.Fatal(err)
	}
	if dates, err := db.ChangedDates(ctx, 232, "2025-09-01"); err != nil || len(dates) != 0 {
		t.Errorf("после уборки осталось %v (err=%v)", dates, err)
	}
}

// Сентябрь первый раз загружают в августе, и граница прошлого синка ложится
// раньше самого месяца. Выборка «до» не должна выходить за месяц: иначе весь
// конец августа объявится отменённым — в сентябрьском ответе вуза его нет по
// определению.
func TestChangedDaysStayInsideMonth(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}

	fixNow(t, "2025-08-20")
	august := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 8, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			{ID: 1, Date: "2025-08-25", LessonTimeID: 1, Discipline: "Анатомия", GroupID: 232},
		},
	}
	september := importdata.MonthSchedule{
		GroupID: 232, Year: 2025, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{
			{ID: 2, Date: "2025-09-02", LessonTimeID: 1, Discipline: "Химия", GroupID: 232},
		},
	}
	for _, ms := range []importdata.MonthSchedule{august, september} {
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}

	// Назавтра сентябрь правят: границей сравнения остаётся 20 августа.
	fixNow(t, "2025-08-21")
	moved := september
	moved.Lessons = []importdata.Lesson{
		{ID: 2, Date: "2025-09-02", LessonTimeID: 2, Discipline: "Химия", GroupID: 232},
	}
	if changed, err := db.SaveMonth(ctx, moved); err != nil || !changed {
		t.Fatalf("правка сентября: changed=%v err=%v", changed, err)
	}

	dates, err := db.ChangedDates(ctx, 232, "2025-08-21")
	if err != nil {
		t.Fatal(err)
	}
	if len(dates) != 1 || dates[0] != "2025-09-02" {
		t.Errorf("изменённые дни = %v, ожидался только 2025-09-02", dates)
	}
}

// Отписка от новостей о правках не должна забирать с собой личный ответ
// автора: он лежит в той же очереди и адресован тому же человеку.
func TestDropOutboxKeepsAnswer(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)

	if err := db.Enqueue(ctx, "tg", "1", OutboxChange, "c1", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := db.Enqueue(ctx, "tg", "1", OutboxAnswer, "7", `{"id":7}`); err != nil {
		t.Fatal(err)
	}
	if err := db.DropOutboxFor(ctx, "tg", "1", OutboxChange); err != nil {
		t.Fatal(err)
	}

	items, err := db.TakeOutbox(ctx, "tg", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != OutboxAnswer {
		t.Fatalf("в очереди осталось %+v, ожидался только ответ автора", items)
	}

	// А блокировка бота выносит всё: писать туда больше некому.
	if err := db.DropOutboxFor(ctx, "tg", "1"); err != nil {
		t.Fatal(err)
	}
	if items, err = db.TakeOutbox(ctx, "tg", 10); err != nil || len(items) != 0 {
		t.Fatalf("после блокировки очередь = %+v (%v)", items, err)
	}
}

// Пауза между обращениями считается по последнему принятому: отклонённое
// обращение её не продлевает.
func TestFeedbackCooldown(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	fb := Feedback{Platform: "tg", ExtID: "1", GroupName: "ГР-12", Text: "первое"}

	id, wait, err := db.AddFeedback(ctx, fb)
	if err != nil || wait != 0 || id == 0 {
		t.Fatalf("первое обращение: id=%d wait=%v err=%v", id, wait, err)
	}

	fb.Text = "второе"
	id2, wait, err := db.AddFeedback(ctx, fb)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != 0 || wait <= 0 || wait > FeedbackCooldown {
		t.Fatalf("второе обращение подряд: id=%d wait=%v", id2, wait)
	}

	// Сосед по площадке пишет своё, и пауза первого его не касается.
	other := Feedback{Platform: "tg", ExtID: "2", Text: "чужое"}
	if _, wait, err = db.AddFeedback(ctx, other); err != nil || wait != 0 {
		t.Fatalf("чужая пауза задела соседа: wait=%v err=%v", wait, err)
	}

	got, err := db.Feedback(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "первое" || got.GroupName != "ГР-12" || got.Answered() {
		t.Fatalf("обращение сохранено как %+v", got)
	}
	if err := db.MarkAnswered(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, err = db.Feedback(ctx, id); err != nil || !got.Answered() {
		t.Fatalf("обращение не отмечено отвеченным: %+v (%v)", got, err)
	}
}

func TestFreshnessRequiresEveryRequestedMonth(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 9}); err != nil {
		t.Fatal(err)
	}
	for _, months := range [][][2]int{
		{{2026, 9}, {2026, 10}},
		{{2026, 10}, {2026, 9}},
	} {
		at, err := db.FreshnessOf(ctx, 232, months)
		if err != nil {
			t.Fatal(err)
		}
		if !at.IsZero() {
			t.Fatalf("частичная загрузка названа полной: %v", at)
		}
	}
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 10}); err != nil {
		t.Fatal(err)
	}
	at, err := db.FreshnessOf(ctx, 232, [][2]int{{2026, 9}, {2026, 10}})
	if err != nil || at.IsZero() {
		t.Fatalf("полный диапазон: %v, %v", at, err)
	}
}

func TestNotifyCatchUpWindow(t *testing.T) {
	for _, tc := range []struct {
		name            string
		offset, at, now int
		want            bool
	}{
		{"one minute late", 180, 420, 241, true},
		{"window end", 180, 420, 250, true},
		{"too late", 180, 420, 251, false},
		{"not yet due", 180, 420, 239, false},
		{"UTC midnight crossing", -180, 1260, 5, true},
		{"previous local day", 180, 1439, 1260, false},
		{"local midnight", 180, 0, 1261, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTest(t)
			ctx := context.Background()
			u := User{Platform: "tg", ExtID: "1", GroupID: 232}.WithDefaults(tc.offset)
			u.MorningAt = tc.at
			if err := db.SaveUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			got, err := db.UsersToNotify(ctx, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			if (len(got) == 1) != tc.want {
				t.Fatalf("targets=%d, want due=%t", len(got), tc.want)
			}
		})
	}
}

func TestMenuVersionMigrationAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "menu.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	stale := User{Platform: "tg", ExtID: "1", GroupID: 232, MenuSent: true}.WithDefaults(180)
	if err := db.SaveUser(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE users DROP COLUMN menu_version`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, legacyStaffSchema+`ALTER TABLE users DROP COLUMN tz_name; PRAGMA user_version = 8`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.User(ctx, "tg", "1")
	if err != nil || !u.MenuSent || u.MenuVersion != "" {
		t.Fatalf("миграция: %+v %v", u, err)
	}
	if err := db.MarkMenuSent(ctx, "tg", "1", "v1:new"); err != nil {
		t.Fatal(err)
	}
	// Старый снимок настроек и старый клиент не стирают подтверждение доставки.
	if err := db.SaveUser(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMenuSent(ctx, "tg", "1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err = db.User(ctx, "tg", "1")
	if err != nil || u.MenuVersion != "v1:new" || u.GroupID != 232 {
		t.Fatalf("после перезапуска: %+v %v", u, err)
	}
}

func TestFindSubgroup(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatalf("сохранение дерева: %v", err)
	}
	// ГР-12 вуз называет подгруппы полностью, ГР-11 — одними номерами.
	// Обе формы встречаются в каталоге, и обе человек пишет одинаково.
	if err := db.SaveSubgroups(ctx, 232, []importdata.Subgroup{
		{ID: 707, Name: "ГР-12/1"}, {ID: 708, Name: "ГР-12/2"},
	}); err != nil {
		t.Fatalf("сохранение подгрупп: %v", err)
	}
	if err := db.SaveSubgroups(ctx, 231, []importdata.Subgroup{
		{ID: 801, Name: "1"}, {ID: 802, Name: "2"},
	}); err != nil {
		t.Fatalf("сохранение подгрупп: %v", err)
	}

	found := []struct {
		query string
		want  int64
	}{
		{"ГР-12/2", 708},    // как написано в расписании вуза
		{"гр12/2", 708},     // без дефисов и регистра
		{"г р 12 / 1", 707}, // с пробелами
		{"ГР-11/2", 802},    // подгруппа названа одним номером
	}
	for _, tt := range found {
		got, err := db.FindSubgroup(ctx, tt.query)
		if err != nil {
			t.Errorf("поиск подгруппы %q: %v", tt.query, err)
			continue
		}
		if got.ID != tt.want {
			t.Errorf("поиск подгруппы %q дал %+v, ожидалась подгруппа %d", tt.query, got, tt.want)
		}
	}

	// Название группы без подгруппы разбирается поиском по группам, и подсовывать
	// сюда первую попавшуюся подгруппу нельзя: человек не выбирал её.
	for _, q := range []string{"ГР-12", "ГР-12/9", "барх11/1", ""} {
		if got, err := db.FindSubgroup(ctx, q); err == nil {
			t.Errorf("поиск подгруппы %q дал %+v, ожидался отказ", q, got)
		} else if !errors.Is(err, ErrNotFound) {
			t.Errorf("поиск подгруппы %q: %v, ожидалась ErrNotFound", q, err)
		}
	}
}

func TestGroupTwins(t *testing.T) {
	ctx := context.Background()
	fixNow(t, "2026-09-15")
	db := openTest(t)

	tree := importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Медицинский институт"}},
		Groups: []importdata.Group{
			{ID: 39, Name: "ГР-22", DepartmentID: 10, Course: 2, Active: true},
			{ID: 545, Name: "ГР-22", DepartmentID: 10, Course: 1, Active: true},
			{ID: 232, Name: "ГР-12", DepartmentID: 10, Course: 1, Active: true},
		},
	}
	tree.Groups[0].Hidden = true
	if err := db.SaveGroupTree(ctx, tree); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
		GroupID: 39, Year: 2026, Month: 6, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{{
			ID: 1, Date: "2026-06-10", LessonTimeID: 1, Discipline: "Биохимия", GroupID: 39,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
		GroupID: 545, Year: 2026, Month: 9, LessonTimes: testTimes,
		Lessons: []importdata.Lesson{{
			ID: 2, Date: "2026-09-01", LessonTimeID: 1, Discipline: "Анатомия", GroupID: 545,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// Копии ищутся по любой из них — человек приходит сюда из своей группы,
	// какой бы она ни была.
	for _, from := range []int64{39, 545} {
		twins, err := db.GroupTwins(ctx, from)
		if err != nil {
			t.Fatal(err)
		}
		if len(twins) != 2 {
			t.Fatalf("из группы %d получено %d копий, ожидалось 2: %+v", from, len(twins), twins)
		}
		if twins[0].ID != 545 || twins[0].Shadowed {
			t.Errorf("первой должна идти видимая копия 545, получено %+v", twins[0])
		}
		if !twins[0].HasCurrent || twins[0].LastDate != "2026-09-01" {
			t.Errorf("у 545 ожидались занятия текущего года: %+v", twins[0])
		}
		if twins[1].HasCurrent || twins[1].LastDate != "2026-06-10" {
			t.Errorf("у 39 расписание должно значиться прошлогодним: %+v", twins[1])
		}
	}

	n, err := db.CountGroupTwins(ctx, 39)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("копий у ГР-22 насчитано %d, ожидалось 2", n)
	}
	// У группы без двойника переключать нечего, и бот об этом узнаёт отсюда.
	if n, err := db.CountGroupTwins(ctx, 232); err != nil || n != 1 {
		t.Errorf("копий у ГР-12 насчитано %d (ошибка %v), ожидалась 1", n, err)
	}
}
