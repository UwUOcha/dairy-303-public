package api

import (
	"net/http"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

const PathNextLesson = "/schedule/next-lesson"
const NextLessonDays = 14

type NextLessonResponse struct {
	Context
	From   string         `json:"from"`
	To     string         `json:"to"`
	Day    schedule.Day   `json:"day"`
	Lesson *schedule.Item `json:"lesson,omitempty"`
	Prev   string         `json:"prev,omitempty"`
	Next   string         `json:"next,omitempty"`
	Freshness
}

func (s *Server) handleNextLesson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, subgroup := intParam(r, "group"), intParam(r, "subgroup")
	cx, err := s.resolveContext(ctx, group, subgroup)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	now := time.Now().In(s.loc)
	if r.URL.Query().Get("from") == "tomorrow" {
		now = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, s.loc)
	}
	from, to := schedule.FormatDate(now), schedule.FormatDate(now.AddDate(0, 0, NextLessonDays-1))
	lessons, grid, err := s.load(ctx, group, from, to)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	out := NextLessonResponse{Context: cx, From: from, To: to}
	out.Day, out.Lesson = schedule.NextLesson(lessons, grid, subgroup, now, NextLessonDays)
	checkedTo := to
	if out.Lesson != nil {
		checkedTo = out.Day.Date
		out.Prev = schedule.ShiftWorkday(out.Day.Date, -1, grid)
		out.Next = schedule.ShiftWorkday(out.Day.Date, 1, grid)
	}
	out.Freshness = s.freshness(ctx, group, from, checkedTo)
	writeJSON(w, http.StatusOK, out)
}
