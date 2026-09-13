package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func TestNextLessonEndpointIncludesSubgroupAndHorizon(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tomorrow := now.AddDate(0, 0, 1)
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 1, Name: "1"}, {ID: 2, Name: "2"}}); err != nil {
		t.Fatal(err)
	}
	month := importdata.MonthSchedule{GroupID: 39, Year: tomorrow.Year(), Month: int(tomorrow.Month()), LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 600, MinuteTo: 690, Label: "10:00 - 11:30"}}, Lessons: []importdata.Lesson{
		{ID: 1, GroupID: 39, SubgroupID: 2, Date: schedule.FormatDate(tomorrow), LessonTimeID: 1, Discipline: "Другая подгруппа"},
		{ID: 2, GroupID: 39, SubgroupID: 1, Date: schedule.FormatDate(tomorrow), LessonTimeID: 1, Discipline: "Химия"},
	}}
	if _, err := db.SaveMonth(ctx, month); err != nil {
		t.Fatal(err)
	}
	var out NextLessonResponse
	if code := get(t, srv, PathNextLesson, "group=39&subgroup=1", &out); code != 200 {
		t.Fatal(code)
	}
	if out.Lesson == nil || out.Lesson.ID != 2 {
		t.Fatalf("wrong next: %+v", out)
	}
	if out.From != schedule.FormatDate(now) || out.To != schedule.FormatDate(now.AddDate(0, 0, 13)) {
		t.Fatalf("range: %+v", out)
	}
	// Only preceding months matter for confidence; later unloaded months must
	// not make tomorrow's known lesson uncertain.
	if tomorrow.Month() == now.Month() && out.Missing {
		t.Fatal("unloaded later month marked known next lesson missing")
	}
}

func TestNextLessonDistinguishesUnloadedAndEmptySchedule(t *testing.T) {
	srv, db, _ := testServer(t)
	var out NextLessonResponse
	if code := get(t, srv, PathNextLesson, "group=39", &out); code != 200 || !out.Missing || out.Lesson != nil {
		t.Fatalf("unloaded schedule: status=%d result=%+v", code, out)
	}
	now := time.Now().UTC()
	end := now.AddDate(0, 0, NextLessonDays-1)
	for month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC); !month.After(end); month = month.AddDate(0, 1, 0) {
		if _, err := db.SaveMonth(context.Background(), importdata.MonthSchedule{GroupID: 39, Year: month.Year(), Month: int(month.Month())}); err != nil {
			t.Fatal(err)
		}
	}
	out = NextLessonResponse{}
	if code := get(t, srv, PathNextLesson, "group=39", &out); code != 200 || out.Missing || out.Stale || out.Lesson != nil {
		t.Fatalf("known empty schedule: status=%d result=%+v", code, out)
	}
}

func TestChangeSummaryEndpointFiltersBothSnapshots(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	date := schedule.FormatDate(now)
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 1, Name: "1"}, {ID: 2, Name: "2"}}); err != nil {
		t.Fatal(err)
	}
	month := importdata.MonthSchedule{GroupID: 39, Year: now.Year(), Month: int(now.Month()), LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 600, MinuteTo: 690}}, Lessons: []importdata.Lesson{{ID: 2, GroupID: 39, SubgroupID: 2, Date: date, LessonTimeID: 1, Discipline: "Химия"}}}
	if _, err := db.SaveMonth(ctx, month); err != nil {
		t.Fatal(err)
	}
	var out ChangeSummaryResponse
	req := ChangeSummaryRequest{GroupID: 39, SubgroupID: 1, Before: []store.ChangedDay{{Date: date, Before: []schedule.Lesson{{ID: 1, Date: date, SubgroupID: 2, Discipline: "Физика"}}}}}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+PathChangeSummary, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Days) != 1 || !out.Days[0].Before.Empty() || !out.Days[0].After.Empty() {
		t.Fatalf("other subgroup leaked: %+v", out)
	}
}

func TestNextStudyDayStartsTomorrow(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tomorrow := now.AddDate(0, 0, 1)
	found := now.AddDate(0, 0, 3)
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 1, Name: "1"}, {ID: 2, Name: "2"}}); err != nil {
		t.Fatal(err)
	}
	lessons := []importdata.Lesson{
		{ID: 1, GroupID: 39, SubgroupID: 1, Date: schedule.FormatDate(now), LessonTimeID: 1, Discipline: "Сегодня"},
		{ID: 2, GroupID: 39, SubgroupID: 2, Date: schedule.FormatDate(tomorrow), LessonTimeID: 1, Discipline: "Другая подгруппа"},
		{ID: 3, GroupID: 39, SubgroupID: 1, Date: schedule.FormatDate(found), LessonTimeID: 1, Discipline: "Химия"},
		{ID: 4, GroupID: 39, SubgroupID: 1, Date: schedule.FormatDate(found), LessonTimeID: 2, Discipline: "Биология"},
	}
	for month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC); !month.After(found); month = month.AddDate(0, 1, 0) {
		ms := importdata.MonthSchedule{GroupID: 39, Year: month.Year(), Month: int(month.Month()), LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 1439, MinuteTo: 1440}, {ID: 2, MinuteFrom: 600, MinuteTo: 690}}}
		for _, l := range lessons {
			if strings.HasPrefix(l.Date, month.Format("2006-01")) {
				ms.Lessons = append(ms.Lessons, l)
			}
		}
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}
	var out NextLessonResponse
	if code := get(t, srv, PathNextLesson, "group=39&subgroup=1&from=tomorrow", &out); code != 200 {
		t.Fatal(code)
	}
	if out.Day.Date != schedule.FormatDate(found) || len(out.Day.Items) != 2 || out.From != schedule.FormatDate(tomorrow) || out.To != schedule.FormatDate(now.AddDate(0, 0, 14)) || out.Missing || out.Prev == "" || out.Next == "" {
		t.Fatalf("unexpected day: %+v", out)
	}
}
