package schedule

import (
	"testing"
	"time"
)

func TestNextLessonCalendarAndSubgroup(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) // Saturday
	lessons := []Lesson{
		{ID: 1, Date: "2026-08-29", MinuteFrom: 600, MinuteTo: 800}, // started
		{ID: 2, Date: "2026-08-29", MinuteFrom: 800, SubgroupID: 2},
		{ID: 3, Date: "2026-08-29", MinuteFrom: 810, Flags: FlagEmpty},
		{ID: 4, Date: "2026-08-30", MinuteFrom: 600, Flags: FlagNonStudy},
		{ID: 5, Date: "2026-08-30", MinuteFrom: 700, SubgroupID: 1, Flags: FlagRemote},
		{ID: 6, Date: "2026-09-01", MinuteFrom: 510, SubgroupID: 1},
	}
	d, it := NextLesson(lessons, Grid{}, 1, now, 14)
	if it == nil || it.ID != 5 || d.Date != "2026-08-30" {
		t.Fatalf("Sunday/subgroup result: %+v %+v", d, it)
	}
	d, it = NextLesson(lessons, Grid{}, 1, now.AddDate(0, 0, 2), 14)
	if it == nil || it.ID != 6 || d.Date != "2026-09-01" {
		t.Fatalf("month boundary: %+v %+v", d, it)
	}
	_, it = NextLesson([]Lesson{{Date: "2026-09-12", MinuteFrom: 510}}, Grid{}, 1, now, 14)
	if it != nil {
		t.Fatal("search exceeded 14 days")
	}
}
