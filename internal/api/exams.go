package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// PathExams — ближайшая сессия группы.
const PathExams = "/schedule/exams"

// examHorizon — насколько далеко вперёд смотрим.
//
// Полгода: сессия следующего семестра появляется в расписании задолго до неё
// самой, и человеку важно увидеть её, как только вуз завёл даты.
const examHorizon = 180 * 24 * time.Hour

// ExamsResponse — испытания группы от сегодняшнего дня и вперёд.
type ExamsResponse struct {
	Context
	// Items — экзамены, зачёты и консультации по возрастанию даты.
	Items []schedule.Item `json:"items,omitempty"`
	From  string          `json:"from"`
	To    string          `json:"to"`
}

// handleExams отдаёт сессию: только то, что предстоит сдавать.
//
// Догоняющего запроса к вузу здесь нет намеренно. Полгода вперёд — это шесть
// месяцев каталога, и тянуть их ради экрана, который человек открывает раз в
// семестр, значило бы уронить ответ на минуту. Ночная синхронизация уже держит
// каталог полным.
func (s *Server) handleExams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := intParam(r, "group")
	subgroupID := intParam(r, "subgroup")

	cx, err := s.resolveContext(ctx, groupID, subgroupID)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	from := s.today()
	start, err := schedule.ParseDate(from)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	to := schedule.FormatDate(start.Add(examHorizon))

	lessons, err := s.db.Lessons(ctx, groupID, from, to)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	grid, err := s.db.Grid(ctx)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	out := ExamsResponse{Context: cx, From: from, To: to}
	for _, l := range schedule.VisibleTo(lessons, subgroupID) {
		if !l.IsSession() {
			continue
		}
		out.Items = append(out.Items, schedule.Item{Lesson: l, Number: grid.NumberOf(l.TimeID)})
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		if out.Items[i].Date != out.Items[j].Date {
			return out.Items[i].Date < out.Items[j].Date
		}
		if out.Items[i].MinuteFrom != out.Items[j].MinuteFrom {
			return out.Items[i].MinuteFrom < out.Items[j].MinuteFrom
		}
		return out.Items[i].ID < out.Items[j].ID
	})
	writeJSON(w, http.StatusOK, out)
}
