package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func TestHistoryListsImmutableVersionsAndFiltersSubgroup(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 1, Name: "1"}, {ID: 2, Name: "2"}}); err != nil {
		t.Fatal(err)
	}
	ms := importdata.MonthSchedule{GroupID: 39, Year: 2026, Month: 9, LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 510, MinuteTo: 605}}}
	save := func() {
		t.Helper()
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}
	save()
	ms.Lessons = []importdata.Lesson{{ID: 99, GroupID: 39, SubgroupID: 2, Date: "2026-09-08", LessonTimeID: 1, Discipline: "Химия"}}
	save()
	ms.Lessons = nil
	save()
	var list struct {
		Revisions []store.ScheduleRevision `json:"revisions"`
	}
	if code := get(t, srv, "/changes/history", "group=39&monday=2026-09-07", &list); code != 200 || len(list.Revisions) != 2 {
		t.Fatalf("%d %+v", code, list)
	}
	id := list.Revisions[0].ID
	var rev store.ScheduleRevision
	query := fmt.Sprintf("group=39&monday=2026-09-07&id=%d", id)
	if code := get(t, srv, "/changes/history", query, &rev); code != 200 || len(rev.Before) != 1 || len(rev.After) != 0 {
		t.Fatalf("%d %+v", code, rev)
	}
	rev = store.ScheduleRevision{}
	if code := get(t, srv, "/changes/history", query+"&subgroup=1", &rev); code != 200 || len(rev.Before) != 0 {
		t.Fatalf("subgroup leaked: %d %+v", code, rev)
	}
	for _, q := range []string{"group=39&monday=bad", "group=39&monday=2026-09-08", "group=39&monday=2026-09-07&id=-1"} {
		var out map[string]any
		if code := get(t, srv, "/changes/history", q, &out); code != 400 {
			t.Fatalf("%s: %d", q, code)
		}
	}
	for _, q := range []string{fmt.Sprintf("group=39&monday=2026-09-14&id=%d", id), "group=39&monday=2026-09-07&id=99999"} {
		var out map[string]any
		if code := get(t, srv, "/changes/history", q, &out); code != 404 {
			t.Fatalf("%s: %d", q, code)
		}
	}
}
