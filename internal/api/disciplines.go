package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// PathDiscipline — один предмет группы за учебное полугодие.
const PathDiscipline = "/disciplines/get"

// DisciplineResponse — всё, что известно про предмет одной группы за семестр.
//
// Раскладку по видам занятий, преподавателям и аудиториям считает клиент: это
// десяток строк поверх того же списка, который всё равно едет ради ленты
// занятий, и второй формат ответа ради них был бы лишним звеном, который
// пришлось бы держать в согласии с первым.
type DisciplineResponse struct {
	Context
	Discipline store.Discipline `json:"discipline"`
	// Items — занятия предмета по возрастанию даты, уже отобранные по подгруппе.
	Items []schedule.Item `json:"items,omitempty"`
	From  string          `json:"from"`
	To    string          `json:"to"`
	// Months и MonthsLoaded — из скольких месяцев полугодия локальная копия
	// действительно собрана. Без этой пары «осталось 12 занятий» звучит как
	// обещание: в сентябре январь ещё не загружен, и цифра вырастет сама.
	Months       int           `json:"months"`
	MonthsLoaded int           `json:"months_loaded"`
	LoadedMonths []string      `json:"loaded_months,omitempty"`
	Grid         schedule.Grid `json:"grid,omitzero"`
	Freshness
}

// handleDiscipline отдаёт занятия предмета за полугодие, в которое попадает
// date (по умолчанию — сегодня).
//
// Предмет адресуется идентификатором справочника (discipline) или точным
// названием (q): сайт приходит сюда из карточки пары, где есть только имя.
//
// Догоняющего запроса к вузу здесь нет по той же причине, что и у сессии:
// полугодие — это пять месяцев каталога, и тянуть их ради экрана, который
// открывают посмотреть уже загруженное, значило бы уронить ответ на минуту.
func (s *Server) handleDiscipline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID, subgroupID := intParam(r, "group"), intParam(r, "subgroup")

	anchor := r.URL.Query().Get("date")
	if anchor == "" || anchor == DateToday {
		anchor = s.today()
	}
	from, to, err := schedule.Semester(anchor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, Error{Error: "Некорректная дата"})
		return
	}

	cx, err := s.resolveContext(ctx, groupID, subgroupID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	var d store.Discipline
	if id := intParam(r, "discipline"); id > 0 {
		d, err = s.db.Discipline(ctx, id)
	} else if name := r.URL.Query().Get("q"); name != "" {
		d, err = s.db.DisciplineByName(ctx, name)
	} else {
		writeJSON(w, http.StatusBadRequest, Error{Error: "Не указан предмет"})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, Error{Error: "Предмет не найден"})
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	lessons, err := s.db.DisciplineLessons(ctx, groupID, d.ID, from, to)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	grid, err := s.db.Grid(ctx)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	out := DisciplineResponse{Context: cx, Discipline: d, From: from, To: to, Grid: grid}
	for _, l := range schedule.VisibleTo(lessons, subgroupID) {
		out.Items = append(out.Items, schedule.Item{Lesson: l, Number: grid.NumberOf(l.TimeID)})
	}
	out.Months, out.MonthsLoaded, out.Freshness, out.LoadedMonths = s.coverage(ctx, groupID, from, to)
	writeJSON(w, http.StatusOK, out)
}

// coverage считает свежесть по тем месяцам диапазона, которые в копии есть.
//
// Общий s.freshness для полугодия не подходит: он объявляет Missing, как
// только не загружен хотя бы один месяц, а незагруженный январь в сентябре —
// это норма, а не поломка. Предупреждать надо, когда нет ничего; остальное
// честнее сказать числом загруженных месяцев.
func (s *Server) coverage(ctx context.Context, groupID int64, from, to string) (months, loaded int, f Freshness, loadedMonths []string) {
	list, err := monthsBetween(from, to)
	if err != nil {
		return 0, 0, Freshness{Missing: true, Stale: true}, nil
	}
	var oldest time.Time
	for _, ym := range list {
		st, err := s.db.MonthState(ctx, groupID, ym[0], ym[1])
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return len(list), loaded, Freshness{Missing: true, Stale: true}, loadedMonths
		}
		loaded++
		loadedMonths = append(loadedMonths, fmt.Sprintf("%04d-%02d", ym[0], ym[1]))
		if oldest.IsZero() || st.FetchedAt.Before(oldest) {
			oldest = st.FetchedAt
		}
	}
	return len(list), loaded, Freshness{FetchedAt: oldest, Missing: loaded == 0,
		Stale: oldest.IsZero() || time.Since(oldest) > s.staleAfter}, loadedMonths
}
