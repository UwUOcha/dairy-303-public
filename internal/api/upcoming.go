package api

import (
	"net/http"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

const PathUpcoming = "/schedule/upcoming"
const UpcomingDays = 30

type UpcomingDay struct {
	schedule.Day
	// Полнота всего пути от начала поиска до этого дня: пропущенный месяц
	// не позволяет утверждать, что найденное занятие действительно ближайшее.
	Freshness
}

type UpcomingResponse struct {
	Context
	From string        `json:"from"`
	To   string        `json:"to"`
	Days []UpcomingDay `json:"days"`
	Grid schedule.Grid `json:"grid"`
	Freshness
}

// Только локальные данные: сводка не запускает обход месяцев в источнике.
func (s *Server) handleUpcoming(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, subgroup := intParam(r, "group"), intParam(r, "subgroup")
	cx, err := s.resolveContext(ctx, group, subgroup)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	from := r.URL.Query().Get("date")
	if from == "" {
		from = s.today()
	}
	start, err := schedule.ParseDate(from)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	to := schedule.FormatDate(start.AddDate(0, 0, UpcomingDays-1))
	lessons, err := s.db.Lessons(ctx, group, from, to)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	grid, err := s.db.Grid(ctx)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	out := UpcomingResponse{Context: cx, From: from, To: to, Grid: grid}
	coverage := map[string]Freshness{}
	for i := 0; i < UpcomingDays; i++ {
		date := schedule.FormatDate(start.AddDate(0, 0, i))
		month := date[:7]
		f, ok := coverage[month]
		if !ok {
			f = s.freshness(ctx, group, from, date)
			coverage[month] = f
		}
		out.Days = append(out.Days, UpcomingDay{Day: schedule.BuildDay(date, lessons, grid, subgroup), Freshness: f})
		out.Freshness = f
	}
	writeJSON(w, http.StatusOK, out)
}
