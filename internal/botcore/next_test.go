package botcore

import (
	"strings"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

func TestNextStudyDayPresentation(t *testing.T) {
	for _, tc := range []struct {
		name, date, want string
		freshness        api.Freshness
		found, skip      bool
	}{
		{name: "tomorrow", date: "2026-09-06", want: "Химия", found: true},
		{name: "skip empty days", date: "2026-09-08", want: "Химия", found: true, skip: true},
		{name: "missing month", date: "2026-09-08", want: "Данные могут быть", found: true, freshness: api.Freshness{Missing: true}},
		{name: "stale", date: "2026-09-08", want: "Данные могут быть", found: true, freshness: api.Freshness{Stale: true}},
		{name: "empty", want: "14 дней начиная с завтра"},
		{name: "unknown", want: "не могу надёжно", freshness: api.Freshness{Missing: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := api.NextLessonResponse{Context: api.Context{Today: "2026-09-05"}, From: "2026-09-06", To: "2026-09-19", Freshness: tc.freshness}
			if tc.found {
				r.Lesson = &schedule.Item{}
				r.Day = schedule.Day{Date: tc.date, Items: []schedule.Item{{Lesson: schedule.Lesson{Discipline: "Химия"}}, {Lesson: schedule.Lesson{Discipline: "Биология"}}}}
			}
			out := formatNextStudyDay(r, api.UserResponse{})
			if !strings.Contains(out.Text, tc.want) || strings.Contains(out.Text, "Завтра занятий нет") != tc.skip {
				t.Fatal(out.Text)
			}
			if tc.found && !strings.Contains(out.Text, "Биология") {
				t.Fatal("whole day not shown")
			}
			for _, row := range out.Keyboard.Rows {
				for _, btn := range row {
					if btn.Data == cbNow || btn.Data == cbNextLesson {
						t.Fatal("redundant action", btn)
					}
				}
			}
		})
	}
}
