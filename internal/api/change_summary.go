package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

const PathChangeSummary = "/changes/summary"

type ChangeSummaryRequest struct {
	GroupID    int64              `json:"group_id"`
	SubgroupID int64              `json:"subgroup_id"`
	Before     []store.ChangedDay `json:"before"`
}

type ChangeSummaryResponse struct {
	Context
	Days []ChangeDayResponse `json:"days"`
}

func (s *Server) handleChangeSummary(w http.ResponseWriter, r *http.Request) {
	var req ChangeSummaryRequest
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(req.Before) > 31 {
		s.fail(w, 400, errors.New("слишком много дней"))
		return
	}
	ctx := r.Context()
	cx, err := s.resolveContext(ctx, req.GroupID, req.SubgroupID)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	grid, err := s.db.Grid(ctx)
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	out := ChangeSummaryResponse{Context: cx}
	sort.Slice(req.Before, func(i, j int) bool { return req.Before[i].Date < req.Before[j].Date })
	for _, d := range req.Before {
		if _, err := schedule.ParseDate(d.Date); err != nil {
			s.fail(w, 400, err)
			return
		}
		if d.Date < cx.Today {
			continue
		}
		after, err := s.db.Lessons(ctx, req.GroupID, d.Date, d.Date)
		if err != nil {
			s.fail(w, 500, err)
			return
		}
		out.Days = append(out.Days, ChangeDayResponse{Context: cx, Date: d.Date, Known: true,
			Before:    schedule.BuildDay(d.Date, d.Before, grid, req.SubgroupID),
			After:     schedule.BuildDay(d.Date, after, grid, req.SubgroupID),
			Freshness: s.freshness(ctx, req.GroupID, d.Date, d.Date),
		})
	}
	writeJSON(w, 200, out)
}
