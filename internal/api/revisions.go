package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func (s *Server) handleScheduleHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, subgroup := intParam(r, "group"), intParam(r, "subgroup")
	mon := r.URL.Query().Get("monday")
	d, err := schedule.ParseDate(mon)
	if err != nil || (int(d.Weekday())+6)%7 != 0 || group <= 0 || subgroup < 0 {
		s.fail(w, 400, fmt.Errorf("нужны группа и дата понедельника"))
		return
	}
	if _, err := s.resolveContext(ctx, group, subgroup); err != nil {
		code := 500
		if errors.Is(err, store.ErrNotFound) {
			code = 404
		}
		s.fail(w, code, err)
		return
	}
	if r.URL.Query().Has("id") {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			s.fail(w, 400, fmt.Errorf("некорректный снимок"))
			return
		}
		rev, err := s.db.ScheduleRevision(ctx, group, mon, id)
		if err != nil {
			code := 500
			if errors.Is(err, store.ErrNotFound) {
				code = 404
			}
			s.fail(w, code, err)
			return
		}
		filter := func(ls []schedule.Lesson) []schedule.Lesson {
			out := []schedule.Lesson{}
			for _, l := range ls {
				if subgroup == 0 || l.SubgroupID == 0 || l.SubgroupID == subgroup {
					out = append(out, l)
				}
			}
			return out
		}
		writeJSON(w, 200, map[string]any{"id": rev.ID, "created_at": rev.CreatedAt, "before_at": rev.BeforeAt, "before": filter(rev.Before), "after": filter(rev.After)})
		return
	}
	revisions, err := s.db.ScheduleRevisions(ctx, group, mon)
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revisions": revisions, "retention_days": 14})
}
