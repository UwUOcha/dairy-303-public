package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func TestTeacherWeekDeduplicatesFlowsAndReportsCoverage(t *testing.T) {
	srv, db, sync := testServer(t)
	ctx := context.Background()
	err := db.SaveGroupTree(ctx, importdata.GroupTree{Departments: []importdata.Department{{ID: 10, Name: "Институт"}},
		Groups: []importdata.Group{{ID: 39, Name: "ГР-22", DepartmentID: 10, Active: true}, {ID: 40, Name: "ГР-23", DepartmentID: 10, Active: true}, {ID: 41, Name: "ГР-24", DepartmentID: 10, Active: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []int64{39, 40} {
		_, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: group, Year: 2026, Month: 9,
			LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 510, MinuteTo: 605, Label: "08:30 – 10:05"}},
			Lessons: []importdata.Lesson{{ID: 10, Date: "2026-09-07", LessonTimeID: 1, Discipline: "Анатомия", Audience: importdata.AudienceFlow,
				AudienceLabel: "Поток (ГР-22, ГР-23)", Staff: []importdata.Staff{{Name: "Соколова Елена"}, {Name: "Морозов Дмитрий"}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var search struct {
		Teachers []store.Teacher `json:"teachers"`
	}
	if code := get(t, srv, PathTeachers, "q=СОКОЛОВА&date=2026-09-07", &search); code != 200 || len(search.Teachers) != 1 {
		t.Fatalf("search: %d %+v", code, search)
	}
	var week TeacherWeekResponse
	if code := get(t, srv, PathTeacherWeek, "teacher=1&monday=2026-09-09", &week); code != 200 {
		t.Fatalf("week: %d", code)
	}
	if week.Week.Monday != "2026-09-07" {
		t.Fatalf("week not normalized: %+v", week.Week)
	}
	var count int
	for _, d := range week.Week.Days {
		for _, l := range d.Items {
			count++
			if len(l.Staff) != 2 || l.Staff[0] != "Соколова Елена" {
				t.Fatalf("staff order: %+v", l.Staff)
			}
		}
	}
	if count != 1 {
		t.Fatalf("flow duplicated: %d", count)
	}
	if len(week.Profile.Subjects) != 1 || week.Profile.Subjects[0].Lessons != 1 || len(week.Profile.Subjects[0].Groups) != 2 {
		t.Fatalf("profile duplicates flow or drops its groups: %+v", week.Profile)
	}
	if !week.Profile.Missing || week.Profile.Loaded != 2 || week.Profile.Expected != 15 || week.Profile.From != "2026-09-01" || week.Profile.To != "2027-01-31" {
		t.Fatalf("half-year coverage must be independent of week coverage: %+v", week.Profile)
	}
	if len(week.Week.Days[0].Items[0].Groups) != 2 {
		t.Fatalf("flow groups missing: %+v", week.Week.Days[0].Items)
	}
	if !week.Missing || week.Loaded != 2 || week.Expected != 3 || week.FetchedAt.IsZero() {
		t.Fatalf("coverage: %+v", week)
	}
	if sync.ensured != 0 {
		t.Fatal("teacher browsing must not fetch every group")
	}
	if code := get(t, srv, PathTeacherWeek, "teacher=999&monday=2026-09-07", nil); code != http.StatusNotFound {
		t.Fatalf("missing teacher: %d", code)
	}
	if code := get(t, srv, PathTeacherWeek, "teacher=1&monday=wrong", nil); code != http.StatusBadRequest {
		t.Fatalf("bad date: %d", code)
	}
}

func TestPublicSubgroupsRejectsUnknownGroupBeforeCatchUp(t *testing.T) {
	srv, _, sync := testServer(t)
	if code := get(t, srv, PathSubgroups, "group=999999", nil); code != http.StatusNotFound {
		t.Fatalf("unknown group: %d", code)
	}
	if sync.ensured != 0 {
		t.Fatal("unknown group reached upstream catch-up")
	}
	if code := get(t, srv, PathSubgroups, "group=39", nil); code != http.StatusOK {
		t.Fatalf("known group: %d", code)
	}
	if sync.ensured != 1 {
		t.Fatal("subgroups must use freshness-aware EnsureRange")
	}
}

func TestTeacherWeekUsesSubgroupCatalogName(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 334, Name: "ГР-22/1"}}); err != nil {
		t.Fatal(err)
	}
	_, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: 39, Year: 2026, Month: 9,
		Lessons: []importdata.Lesson{{ID: 200, Date: "2026-09-07", Discipline: "Анатомия",
			Audience: importdata.AudienceSubgroup, SubgroupID: 334, AudienceLabel: "ГР-22",
			Staff: []importdata.Staff{{ID: 701, Name: "Соколова Елена"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var week TeacherWeekResponse
	if code := get(t, srv, PathTeacherWeek, "teacher=1&monday=2026-09-07", &week); code != 200 {
		t.Fatalf("week: %d", code)
	}
	var count int
	for _, day := range week.Week.Days {
		for _, lesson := range day.Items {
			count++
			if lesson.SubgroupID != 334 || lesson.AudienceLabel != "ГР-22/1" {
				t.Fatalf("wrong subgroup: %+v", lesson)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected one lesson, got %d", count)
	}
}

func TestTeachersSearchAndProfileStayWithinPeriodAndActiveGroups(t *testing.T) {
	srv, db, sync := testServer(t)
	ctx := context.Background()
	if err := db.SaveGroupTree(ctx, importdata.GroupTree{Groups: []importdata.Group{
		{ID: 39, Name: "Группа А", Active: true}, {ID: 40, Name: "Группа Б", Active: true}, {ID: 41, Name: "Архив", Active: false},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		group   int64
		month   int
		lessons []importdata.Lesson
	}{
		{39, 9, []importdata.Lesson{
			{ID: 1, Date: "2026-09-07", Discipline: "Анатомия", ClassType: "Лек", Staff: []importdata.Staff{{ID: 701, Name: "Соколова Елена"}}},
			{ID: 2, Date: "2026-09-08", Discipline: "Окно", IsEmpty: true, Staff: []importdata.Staff{{ID: 701, Name: "Соколова Елена"}}},
		}},
		{40, 10, []importdata.Lesson{{ID: 3, Date: "2026-10-05", Discipline: "Биохимия", ClassType: "Пр", Staff: []importdata.Staff{{ID: 701, Name: "Соколова Елена"}}}}},
		{39, 2, []importdata.Lesson{{ID: 4, Date: "2026-02-02", Discipline: "Прошлый предмет", Staff: []importdata.Staff{{ID: 701, Name: "Соколова Елена"}}}}},
		{41, 9, []importdata.Lesson{{ID: 5, Date: "2026-09-07", Discipline: "Архивный предмет", Staff: []importdata.Staff{{Name: "Архивный преподаватель"}}}}},
	} {
		if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: fixture.group, Year: 2026, Month: fixture.month, Lessons: fixture.lessons}); err != nil {
			t.Fatal(err)
		}
	}
	var result struct {
		Teachers []store.Teacher `json:"teachers"`
	}
	for _, tc := range []struct {
		q, group string
		want     int
	}{
		{"БИОХИМИЯ", "", 1}, {"биохимия", "39", 0}, {"анатомия", "39", 1}, {"прошлый", "", 0}, {"архив", "", 0}, {"окно", "", 0},
	} {
		params := url.Values{"date": {"2026-09-07"}, "q": {tc.q}}
		if tc.group != "" {
			params.Set("group", tc.group)
		}
		if code := get(t, srv, PathTeachers, params.Encode(), &result); code != 200 || len(result.Teachers) != tc.want {
			t.Fatalf("search %+v: %d %+v", tc, code, result)
		}
	}
	if code := get(t, srv, PathTeachers, "date=2026-09-07&q="+url.QueryEscape("соколова"), &result); code != 200 || len(result.Teachers) != 1 {
		t.Fatalf("name search: %d %+v", code, result)
	}
	if !reflect.DeepEqual(result.Teachers[0].Subjects, []string{"Анатомия", "Биохимия"}) {
		t.Fatalf("subjects: %+v", result)
	}
	var profile TeacherWeekResponse
	if code := get(t, srv, PathTeacherWeek, "teacher=1&monday=2026-09-14", &profile); code != 200 {
		t.Fatal(code)
	}
	if !profile.Week.Empty() || len(profile.Profile.Subjects) != 2 {
		t.Fatalf("empty week must retain half-year subjects: %+v", profile)
	}
	for _, path := range []string{PathTeachers, PathTeacherWeek} {
		params := "date=wrong"
		if path == PathTeacherWeek {
			params = "teacher=1&monday=wrong"
		}
		if code := get(t, srv, path, params, nil); code != 400 {
			t.Fatalf("invalid date: %d", code)
		}
	}
	if code := get(t, srv, PathTeachers, "group=oops", nil); code != 400 {
		t.Fatalf("invalid group: %d", code)
	}
	if sync.ensured != 0 {
		t.Fatal("teacher catalog must not fetch all groups upstream")
	}
}

func TestTeacherSearchRanksExactSurnameAndKeepsGroupContext(t *testing.T) {
	srv, db, _ := testServer(t)
	_, err := db.SaveMonth(context.Background(), importdata.MonthSchedule{GroupID: 39, Year: 2026, Month: 9,
		Lessons: []importdata.Lesson{{ID: 81, Date: "2026-09-07", Discipline: "Физиология", Staff: []importdata.Staff{{Name: "Романов А.В."}, {Name: "Романова А.Н."}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Teachers []store.Teacher `json:"teachers"`
	}
	if code := get(t, srv, PathTeachers, "date=2026-09-07&q="+url.QueryEscape("Романова"), &out); code != 200 {
		t.Fatal(code)
	}
	if len(out.Teachers) != 2 || out.Teachers[0].Name != "Романова А.Н." {
		t.Fatalf("wrong order: %+v", out)
	}
	if len(out.Teachers[0].Groups) != 1 || out.Teachers[0].Groups[0] != "ГР-22" {
		t.Fatalf("missing group context: %+v", out)
	}
}

func TestTeacherDirectoryMetadataAndHomonyms(t *testing.T) {
	srv, db, sync := testServer(t)
	ctx := context.Background()
	directory := importdata.StaffDirectory{Staff: []importdata.StaffDetails{
		{ID: 65, Name: "Смирнов В.В.", FullName: "Смирнов Владимир Владимирович", Degree: "к.п.н.", Departments: []string{"Кафедра информатики"}},
		{ID: 709, Name: "Смирнов В.В.", FullName: "Смирнов Виктор Валерьевич"},
	}}
	if err := db.SaveStaffDirectory(ctx, directory, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := importdata.MonthSchedule{GroupID: 39, Year: 2026, Month: 9}
	for i, p := range directory.Staff {
		m.Lessons = append(m.Lessons, importdata.Lesson{ID: int64(i + 10), Date: "2026-09-07", Discipline: []string{"Большие данные", "Медицина"}[i], Staff: []importdata.Staff{{ID: p.ID, Name: p.Name}}})
	}
	if _, err := db.SaveMonth(ctx, m); err != nil {
		t.Fatal(err)
	}
	var search struct {
		Teachers []store.Teacher `json:"teachers"`
	}
	if code := get(t, srv, PathTeachers, "q=Смирнов&date=2026-09-07", &search); code != 200 || len(search.Teachers) != 2 {
		t.Fatalf("homonyms: %d %+v", code, search)
	}
	if code := get(t, srv, PathTeachers, "q=Владимир&date=2026-09-07", &search); code != 200 || len(search.Teachers) != 1 {
		t.Fatalf("full name search: %d %+v", code, search)
	}
	var week TeacherWeekResponse
	if code := get(t, srv, PathTeacherWeek, fmt.Sprintf("teacher=%d&monday=2026-09-07", search.Teachers[0].ID), &week); code != 200 {
		t.Fatalf("profile: %d", code)
	}
	if week.Teacher.FullName != directory.Staff[0].FullName || week.Teacher.Degree != "к.п.н." || len(week.Teacher.Departments) != 1 || len(week.Profile.Subjects) != 1 || week.Profile.Subjects[0].Name != "Большие данные" {
		t.Fatalf("profile contaminated or missing metadata: %+v", week)
	}
	if sync.ensured != 0 {
		t.Fatal("teacher profile downloaded schedule")
	}
}
