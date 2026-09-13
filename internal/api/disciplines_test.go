package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

func TestDisciplineCollectsSemesterWithoutReachingUpstream(t *testing.T) {
	srv, db, sync := testServer(t)
	ctx := context.Background()
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 334, Name: "ГР-22/1"}, {ID: 335, Name: "ГР-22/2"}}); err != nil {
		t.Fatal(err)
	}
	times := []importdata.LessonTime{{ID: 1, MinuteFrom: 510, MinuteTo: 605, Label: "08:30 – 10:05"}}
	_, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: 39, Year: 2026, Month: 9, LessonTimes: times,
		Lessons: []importdata.Lesson{
			{ID: 10, Date: "2026-09-07", LessonTimeID: 1, Discipline: "Анатомия", ClassType: "Лек",
				Classroom: "к. 1/210", Staff: []importdata.Staff{{Name: "Соколова Елена"}}},
			{ID: 11, Date: "2026-09-14", LessonTimeID: 1, Discipline: "Анатомия", ClassType: "Лб",
				Classroom: "к. 1/211", Audience: importdata.AudienceSubgroup, SubgroupID: 334,
				Staff: []importdata.Staff{{Name: "Морозов Дмитрий"}}},
			{ID: 12, Date: "2026-09-14", LessonTimeID: 1, Discipline: "Анатомия", ClassType: "Лб",
				Audience: importdata.AudienceSubgroup, SubgroupID: 335},
			{ID: 13, Date: "2026-09-21", LessonTimeID: 1, Discipline: "Биохимия", ClassType: "Лек"},
		}})
	if err != nil {
		t.Fatal(err)
	}

	// Сайт приходит из карточки пары, где есть только название предмета.
	var byName DisciplineResponse
	if code := get(t, srv, PathDiscipline, "group=39&q=Анатомия&date=2026-09-07", &byName); code != 200 {
		t.Fatalf("по названию: %d", code)
	}
	if len(byName.Items) != 3 || byName.Discipline.Name != "Анатомия" {
		t.Fatalf("предмет собран неверно: %+v", byName)
	}
	if byName.From != "2026-09-01" || byName.To != "2027-01-31" {
		t.Fatalf("границы полугодия: %s..%s", byName.From, byName.To)
	}
	// Пять месяцев полугодия, загружен один: без этой пары «осталось N пар»
	// звучало бы как обещание.
	if byName.Months != 5 || byName.MonthsLoaded != 1 || byName.Missing || len(byName.LoadedMonths) != 1 || byName.LoadedMonths[0] != "2026-09" {
		t.Fatalf("покрытие: %+v", byName)
	}
	if byName.Items[0].Staff[0] != "Соколова Елена" || byName.Items[0].ClassType != "Лек" {
		t.Fatalf("занятие без подробностей: %+v", byName.Items[0])
	}
	if byName.Items[0].Number != 1 {
		t.Fatalf("номер пары не проставлен: %+v", byName.Items[0])
	}

	// Ссылка, которой делятся, несёт найденный идентификатор.
	var byID DisciplineResponse
	if code := get(t, srv, PathDiscipline,
		"group=39&discipline="+itoa(byName.Discipline.ID)+"&subgroup=334&date=2026-09-07", &byID); code != 200 {
		t.Fatalf("по id: %d", code)
	}
	if len(byID.Items) != 2 {
		t.Fatalf("подгруппа видит чужую лабораторную: %+v", byID.Items)
	}
	if byID.Subgroup == nil || byID.Subgroup.ID != 334 {
		t.Fatalf("подгруппа не разрешена: %+v", byID.Subgroup)
	}

	if code := get(t, srv, PathDiscipline, "group=39&q=Философия&date=2026-09-07", nil); code != http.StatusNotFound {
		t.Fatalf("неизвестный предмет: %d", code)
	}
	if code := get(t, srv, PathDiscipline, "group=39&date=2026-09-07", nil); code != http.StatusBadRequest {
		t.Fatalf("предмет не указан: %d", code)
	}
	if code := get(t, srv, PathDiscipline, "group=39&q=Анатомия&date=вчера", nil); code != http.StatusBadRequest {
		t.Fatalf("некорректная дата: %d", code)
	}
	if code := get(t, srv, PathDiscipline, "group=999999&q=Анатомия&date=2026-09-07", nil); code != http.StatusNotFound {
		t.Fatalf("неизвестная группа: %d", code)
	}
	// Полугодие — это пять месяцев каталога: ходить за ними в вуз по открытию
	// экрана нельзя.
	if sync.ensured != 0 {
		t.Fatalf("экран предмета сходил к вузу: %d", sync.ensured)
	}
}
