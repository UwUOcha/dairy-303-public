package api

import (
	"context"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

func TestUpcomingCoverageSubgroupsAndCalendarDays(t *testing.T) {
	srv, db, sync := testServer(t)
	ctx := context.Background()
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 1, Name: "1"}, {ID: 2, Name: "2"}}); err != nil {
		t.Fatal(err)
	}
	// The range crosses a week and month boundary; Sunday is a valid study day.
	ms := importdata.MonthSchedule{GroupID: 39, Year: 2026, Month: 10, LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 600, MinuteTo: 690}}, Lessons: []importdata.Lesson{
		{ID: 1, Date: "2026-10-04", SubgroupID: 1, LessonTimeID: 1, Discipline: "Химия"},
		{ID: 2, Date: "2026-10-03", SubgroupID: 2, LessonTimeID: 1, Discipline: "Чужая пара"},
	}}
	if _, err := db.SaveMonth(ctx, ms); err != nil {
		t.Fatal(err)
	}
	var out UpcomingResponse
	if code := get(t, srv, PathUpcoming, "group=39&subgroup=1&date=2026-09-28", &out); code != 200 {
		t.Fatal(code)
	}
	if len(out.Days) != 30 || out.From != "2026-09-28" || out.To != "2026-10-27" {
		t.Fatalf("wrong horizon: %+v", out)
	}
	if len(out.Days[5].Items) != 0 || len(out.Days[6].Items) != 1 || out.Days[6].Items[0].ID != 1 {
		t.Fatal("wrong subgroup/day")
	}
	if !out.Days[6].Missing {
		t.Fatal("missing preceding month must make the next day uncertain")
	}
	if sync.ensured != 0 {
		t.Fatal("summary must not fetch from upstream")
	}
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: 39, Year: 2026, Month: 9}); err != nil {
		t.Fatal(err)
	}
	if code := get(t, srv, PathUpcoming, "group=39&subgroup=1&date=2026-09-28", &out); code != 200 || out.Missing || out.Days[6].Missing {
		t.Fatal("loaded empty September is valid coverage")
	}
	if code := get(t, srv, PathUpcoming, "group=39&subgroup=1&date=2026-10-04", &out); code != 200 || out.Days[0].Missing || !out.Missing {
		t.Fatal("missing later November must not taint the known first day")
	}
	if code := get(t, srv, PathUpcoming, "group=39&date=bad", &out); code != 400 {
		t.Fatal(code)
	}
}
