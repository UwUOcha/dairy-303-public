package api

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

const PathTeachers = "/teachers/search"
const PathTeacherWeek = "/teachers/week"

type TeacherWeekResponse struct {
	Teacher store.Teacher  `json:"teacher"`
	Week    schedule.Week  `json:"week"`
	Today   string         `json:"today"`
	Profile TeacherProfile `json:"profile"`
	// Grid — сетка звонков вуза, см. DayResponse.Grid.
	Grid     schedule.Grid `json:"grid,omitzero"`
	Loaded   int           `json:"loaded_group_months"`
	Expected int           `json:"expected_group_months"`
	Freshness
}

// TeacherProfile describes observed teaching, never curriculum or employment data.
type TeacherProfile struct {
	From     string           `json:"from"`
	To       string           `json:"to"`
	Subjects []TeacherSubject `json:"subjects"`
	Loaded   int              `json:"loaded_group_months"`
	Expected int              `json:"expected_group_months"`
	Freshness
}
type TeacherSubject struct {
	Name    string                 `json:"name"`
	Lessons int                    `json:"lessons"`
	Kinds   []string               `json:"kinds"`
	Groups  []schedule.LessonGroup `json:"groups"`
}

func teacherSubjects(ls []schedule.Lesson) []TeacherSubject {
	byName := map[string]*TeacherSubject{}
	for _, l := range ls {
		if l.Flags.Has(schedule.FlagEmpty|schedule.FlagNonStudy) || l.Discipline == "" {
			continue
		}
		t := byName[l.Discipline]
		if t == nil {
			t = &TeacherSubject{Name: l.Discipline, Kinds: []string{}, Groups: []schedule.LessonGroup{}}
			byName[l.Discipline] = t
		}
		t.Lessons++
		if l.ClassType != "" {
			found := false
			for _, kind := range t.Kinds {
				found = found || kind == l.ClassType
			}
			if !found {
				t.Kinds = append(t.Kinds, l.ClassType)
			}
		}
		for _, group := range l.Groups {
			found := false
			for _, known := range t.Groups {
				found = found || known.ID == group.ID
			}
			if !found {
				t.Groups = append(t.Groups, group)
			}
		}
	}
	out := []TeacherSubject{}
	for _, t := range byName {
		sort.Strings(t.Kinds)
		sort.Slice(t.Groups, func(i, j int) bool { return t.Groups[i].Name < t.Groups[j].Name })
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lessons != out[j].Lessons {
			return out[i].Lessons > out[j].Lessons
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *Server) handleTeachers(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query().Get("q")) > 200 {
		writeJSON(w, 400, Error{Error: "Слишком длинный запрос"})
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = s.today()
	}
	from, to, err := schedule.Semester(date)
	if err != nil {
		writeJSON(w, 400, Error{Error: "Некорректная дата"})
		return
	}
	group := intParam(r, "group")
	if r.URL.Query().Get("group") != "" && group <= 0 {
		writeJSON(w, 400, Error{Error: "Некорректная группа"})
		return
	}
	ts, err := s.db.SearchTeachers(r.Context(), r.URL.Query().Get("q"), from, to, group)
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	writeJSON(w, 200, struct {
		Teachers []store.Teacher `json:"teachers"`
		From     string          `json:"from"`
		To       string          `json:"to"`
	}{ts, from, to})
}

func (s *Server) handleTeacherWeek(w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("monday")
	if d == "" {
		d = s.today()
	}
	start, err := schedule.ParseDate(d)
	if err != nil {
		writeJSON(w, 400, Error{Error: "Некорректная дата"})
		return
	}
	start = schedule.MondayOf(start)
	from, to := schedule.FormatDate(start), schedule.FormatDate(start.AddDate(0, 0, 6))
	t, err := s.db.Teacher(r.Context(), intParam(r, "teacher"))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 404, Error{Error: "Преподаватель не найден"})
		return
	}
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	ls, err := s.db.TeacherLessons(r.Context(), t.ID, from, to)
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	grid, err := s.db.Grid(r.Context())
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	months, _ := monthsBetween(from, to)
	loaded, expected, oldest, err := s.db.CatalogCoverage(r.Context(), months)
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	profileFrom, profileTo, _ := schedule.Semester(from)
	profileLessons, err := s.db.TeacherLessons(r.Context(), t.ID, profileFrom, profileTo)
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	profileMonths, _ := monthsBetween(profileFrom, profileTo)
	pl, pe, po, err := s.db.CatalogCoverage(r.Context(), profileMonths)
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	profile := TeacherProfile{From: profileFrom, To: profileTo, Subjects: teacherSubjects(profileLessons), Loaded: pl, Expected: pe,
		Freshness: Freshness{FetchedAt: po, Missing: pe == 0 || pl < pe, Stale: po.IsZero() || time.Since(po) > s.staleAfter}}
	writeJSON(w, 200, TeacherWeekResponse{Teacher: t, Week: schedule.BuildWeek(from, ls, grid, 0), Today: s.today(),
		Profile: profile, Grid: grid, Loaded: loaded, Expected: expected, Freshness: Freshness{FetchedAt: oldest,
			Missing: expected == 0 || loaded < expected, Stale: oldest.IsZero() || time.Since(oldest) > s.staleAfter}})
}
