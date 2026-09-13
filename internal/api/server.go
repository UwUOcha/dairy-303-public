package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Sync — то, что сервер спрашивает у синхронизатора.
//
// Интерфейс, а не *syncer.Syncer, по двум причинам. Он честно перечисляет всё,
// что пользовательский путь позволяет себе делать с фоновым контуром: догнать
// недостающие месяцы и спросить, как тот себя чувствует. И он позволяет
// проверить обработчики без живого синка — в том числе то, ради чего
// healthcheck переписывался: сломанный синк не должен делать сервис мёртвым.
type Sync interface {
	// EnsureRange добирает месяцы, покрывающие диапазон дат.
	EnsureRange(ctx context.Context, groupID int64, from, to string) error
	// SyncMonth загружает один месяц одной группы.
	SyncMonth(ctx context.Context, groupID int64, year, month int) error
	// LastError — чем закончился последний проход фоновых контуров.
	LastError() error
}

// Server отдаёт данные raspd ботам.
type Server struct {
	db   *store.DB
	sync Sync
	log  *slog.Logger
	loc  *time.Location
	// staleAfter — с какого возраста данных пользователя стоит предупредить.
	staleAfter time.Duration
	// diag — источники данных админ-панели; подключаются отдельно, см. stats.go.
	diag diagnostics
}

// NewServer собирает сервер.
func NewServer(db *store.DB, s Sync, loc *time.Location, staleAfter time.Duration, log *slog.Logger) *Server {
	if staleAfter <= 0 {
		staleAfter = 24 * time.Hour
	}
	return &Server{db: db, sync: s, log: log, loc: loc, staleAfter: staleAfter}
}

// Handler собирает маршруты.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /web-auth", s.handleWebAuth)
	mux.HandleFunc("POST /bot-key", s.handleBotKey)
	mux.HandleFunc("/bot/", s.handlePersonalBot)
	mux.HandleFunc(PathHealth, s.handleHealth)
	mux.HandleFunc(PathGroupSearch, s.handleGroupSearch)
	mux.HandleFunc(PathDepartments, s.handleDepartments)
	mux.HandleFunc(PathGroupList, s.handleGroupList)
	mux.HandleFunc(PathGroup, s.handleGroup)
	mux.HandleFunc(PathSubgroups, s.handleSubgroups)
	mux.HandleFunc(PathGroupTwins, s.handleGroupTwins)
	mux.HandleFunc(PathDay, s.handleDay)
	mux.HandleFunc(PathWeek, s.handleWeek)
	mux.HandleFunc(PathExams, s.handleExams)
	mux.HandleFunc(PathDiscipline, s.handleDiscipline)
	mux.HandleFunc(PathNow, s.handleNow)
	mux.HandleFunc(PathNextLesson, s.handleNextLesson)
	mux.HandleFunc(PathUpcoming, s.handleUpcoming)
	mux.HandleFunc(PathUserGet, s.handleUserGet)
	mux.HandleFunc(PathUserSave, s.handleUserSave)
	mux.HandleFunc(PathUserMenu, s.handleUserMenu)
	mux.HandleFunc(PathNotifyList, s.handleNotifyList)
	mux.HandleFunc(PathNotifyMark, s.handleNotifyMark)
	mux.HandleFunc(PathNotifyHint, s.handleNotifyHint)
	mux.HandleFunc(PathNotifyOff, s.handleNotifyOff)
	mux.HandleFunc(PathChangeDates, s.handleChangeDates)
	mux.HandleFunc(PathChangeDay, s.handleChangeDay)
	mux.HandleFunc("/changes/history", s.handleScheduleHistory)
	mux.HandleFunc(PathChangeSummary, s.handleChangeSummary)
	mux.HandleFunc(PathOutboxTake, s.handleOutboxTake)
	mux.HandleFunc(PathOutboxDone, s.handleOutboxDone)
	mux.HandleFunc(PathOutboxFail, s.handleOutboxFail)
	mux.HandleFunc(PathOutboxPut, s.handleOutboxPut)
	mux.HandleFunc(PathFeedbackAdd, s.handleFeedbackAdd)
	mux.HandleFunc(PathFeedbackGet, s.handleFeedbackGet)
	mux.HandleFunc(PathFeedbackAns, s.handleFeedbackAnswered)
	mux.HandleFunc(PathUsersCount, s.handleUsersCount)
	mux.HandleFunc(PathProbeList, s.handleProbeList)
	mux.HandleFunc(PathProbeMark, s.handleProbeMark)
	mux.HandleFunc(PathStats, s.handleStats)
	mux.HandleFunc(PathStatsBot, s.handleStatsBot)
	mux.HandleFunc(PathTeachers, s.handleTeachers)
	mux.HandleFunc(PathTeacherWeek, s.handleTeacherWeek)
	return mux
}

// Listen поднимает слушателя на unix-сокете.
//
// Осиротевший сокет от прошлого запуска надо снять руками: systemd
// перезапускает юнит, а bind по существующему пути падает с EADDRINUSE.
func Listen(path string) (net.Listener, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("api: каталог сокета: %w", err)
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("api: снятие старого сокета: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("api: слушатель на %s: %w", path, err)
	}
	// Доступ к сокету регулируется правами файла: бот ходит сюда от своего
	// пользователя, снаружи машины сокет не виден в принципе.
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("api: права на сокет: %w", err)
	}
	return ln, nil
}

// ── вспомогательное ─────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, code int, err error) {
	if code >= 500 {
		s.log.Error("ошибка обработчика", "код", code, "ошибка", err)
	}
	writeJSON(w, code, Error{Error: err.Error()})
}

func intParam(r *http.Request, name string) int64 {
	v, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return v
}

// today возвращает сегодняшнюю дату в часовом поясе вуза.
func (s *Server) today() string { return schedule.FormatDate(time.Now().In(s.loc)) }

// resolveContext собирает группу и подгруппу пользователя.
func (s *Server) resolveContext(ctx context.Context, groupID, subgroupID int64) (Context, error) {
	g, err := s.db.Group(ctx, groupID)
	if err != nil {
		return Context{}, err
	}
	c := Context{Group: g, Today: s.today()}
	if subgroupID > 0 {
		subs, err := s.db.Subgroups(ctx, groupID)
		if err != nil {
			return c, err
		}
		for _, sub := range subs {
			if sub.ID == subgroupID {
				found := sub
				c.Subgroup = &found
				break
			}
		}
	}
	return c, nil
}

// freshness оценивает свежесть данных за диапазон дат.
func (s *Server) freshness(ctx context.Context, groupID int64, from, to string) Freshness {
	months, err := monthsBetween(from, to)
	if err != nil {
		return Freshness{Missing: true}
	}
	oldest, err := s.db.FreshnessOf(ctx, groupID, months)
	if err != nil || oldest.IsZero() {
		return Freshness{Missing: true}
	}
	return Freshness{FetchedAt: oldest, Stale: time.Since(oldest) > s.staleAfter}
}

func monthsBetween(from, to string) ([][2]int, error) {
	start, err := schedule.ParseDate(from)
	if err != nil {
		return nil, err
	}
	end, err := schedule.ParseDate(to)
	if err != nil {
		return nil, err
	}
	var out [][2]int
	cur := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
	for ; !cur.After(last); cur = cur.AddDate(0, 1, 0) {
		out = append(out, [2]int{cur.Year(), int(cur.Month())})
	}
	return out, nil
}

// catchUpTimeout — сколько пользовательский запрос готов ждать поход к вузу.
//
// Обещание архитектуры — «ни одного сценария, где бот молчит из-за чужого
// сервера» — держится только при жёстком потолке. источник отвечает за 0.8 с;
// если он молчит дольше, ждать дальше бессмысленно: в базе почти наверняка
// уже есть что показать, а телеграм за это время успеет погасить спиннер на
// кнопке.
const catchUpTimeout = 6 * time.Second

// load догоняет недостающие месяцы и отдаёт занятия с сеткой звонков.
func (s *Server) load(ctx context.Context, groupID int64, from, to string) ([]schedule.Lesson, schedule.Grid, error) {
	// Ошибку догоняющего запроса намеренно проглатываем: если upstream лёг,
	// правильный ответ — показать то, что уже лежит в базе, с пометкой о
	// свежести, а не отказать пользователю.
	fetchCtx, cancel := context.WithTimeout(ctx, catchUpTimeout)
	defer cancel()
	if err := s.sync.EnsureRange(fetchCtx, groupID, from, to); err != nil {
		s.log.Warn("догоняющий фетч не удался, отдаю из базы",
			"группа", groupID, "с", from, "по", to, "ошибка", err)
	}
	lessons, err := s.db.Lessons(ctx, groupID, from, to)
	if err != nil {
		return nil, schedule.Grid{}, err
	}
	grid, err := s.db.GridForGroup(ctx, groupID)
	return lessons, grid, err
}

// ── обработчики ─────────────────────────────────────────────────────────────

// handleHealth отвечает на вопрос «могу ли я обслуживать запросы», и только
// на него.
//
// Состояние фоновых контуров сюда не подмешивается. Раньше подмешивалось — и
// недоступный на старте источник красил /health в 503, botd не проходил ожидание
// готовности и падал в цикл перезапусков, хотя база была полна и отвечать он
// мог. Сломанный синк — повод для алерта (поле sync_error), а не повод гасить
// бота: локальная копия ровно для того и держится, чтобы переживать чужие
// падения.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := HealthResponse{OK: true}
	if err := s.db.Ping(ctx); err != nil {
		resp.OK, resp.Error = false, err.Error()
	}
	if err := s.sync.LastError(); err != nil {
		resp.SyncError = err.Error()
	}
	resp.Groups, resp.Lessons, _ = s.db.Counts(ctx)
	if ts, _ := s.db.Meta(ctx, store.MetaFullSyncAt); ts != "" {
		if unix, err := strconv.ParseInt(ts, 10, 64); err == nil {
			resp.FullSync = time.Unix(unix, 0).In(s.loc).Format(time.RFC3339)
		}
	}
	code := http.StatusOK
	if !resp.OK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, resp)
}

func (s *Server) handleGroupSearch(w http.ResponseWriter, r *http.Request) {
	limit := int(intParam(r, "limit"))
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	query := r.URL.Query().Get("q")
	groups, err := s.db.SearchGroups(r.Context(), query, limit)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	resp := GroupsResponse{Groups: groups}
	// Ни одной группы — значит человек мог написать название подгруппы. Это
	// не ошибка ввода, а самый частый ответ на вопрос «выбери свою».
	if len(groups) == 0 {
		if sub, err := s.db.FindSubgroup(r.Context(), query); err == nil {
			resp.Subgroup = &sub
		} else if !errors.Is(err, store.ErrNotFound) {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDepartments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.db.Departments(r.Context())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, DepartmentsResponse{Departments: deps})
}

// handleGroupList отдаёт курсы подразделения либо группы конкретного курса —
// два шага обзора дерева для тех, кто не помнит точного названия группы.
func (s *Server) handleGroupList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dep := intParam(r, "department")
	if dep == 0 {
		s.fail(w, http.StatusBadRequest, errors.New("не указано подразделение"))
		return
	}
	course := int(intParam(r, "course"))
	if course == 0 {
		courses, err := s.db.Courses(ctx, dep)
		if err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, CoursesResponse{Courses: courses})
		return
	}
	groups, err := s.db.GroupsOf(ctx, dep, course)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, GroupsResponse{Groups: groups})
}

func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.db.Group(r.Context(), intParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleGroupTwins отдаёт одноимённые записи каталога — материал для ручного
// переключения копии, когда автоматика выбрала не ту.
func (s *Server) handleGroupTwins(w http.ResponseWriter, r *http.Request) {
	twins, err := s.db.GroupTwins(r.Context(), intParam(r, "group"))
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, TwinsResponse{Twins: twins})
}

func (s *Server) handleSubgroups(w http.ResponseWriter, r *http.Request) {
	groupID := intParam(r, "group")
	ctx := r.Context()
	// This endpoint is also exposed by the public site. Unknown group IDs
	// must not cause arbitrary upstream requests through EnsureRange below.
	if _, err := s.db.Group(ctx, groupID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, http.StatusNotFound, err)
		} else {
			s.fail(w, http.StatusInternalServerError, err)
		}
		return
	}

	subs, err := s.db.Subgroups(ctx, groupID)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if len(subs) == 0 {
		// Подгруппы приезжают только вместе с расписанием: отдельного
		// эндпоинта у источника нет. Для только что выбранной группы месяц может
		// быть ещё не загружен — тянем его и пробуем снова, но не дольше, чем
		// готов ждать пользователь.
		fetchCtx, cancel := context.WithTimeout(ctx, catchUpTimeout)
		defer cancel()
		now := time.Now().In(s.loc)
		from, to := store.MonthBounds(now.Year(), int(now.Month()))
		// Groups without subgroups are valid. Repeated visits to the website
		// must respect the month's freshness instead of forcing a new fetch.
		if err := s.sync.EnsureRange(fetchCtx, groupID, from, to); err != nil {
			s.log.Warn("не удалось подтянуть подгруппы", "группа", groupID, "ошибка", err)
		} else if subs, err = s.db.Subgroups(ctx, groupID); err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, SubgroupsResponse{Subgroups: subs})
}

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := intParam(r, "group")
	subgroupID := intParam(r, "subgroup")

	// Кроме ISO-даты понимаем два слова. Они экономят боту круг: «завтра» —
	// это следующий учебный день, и вычислить его можно только здесь, зная
	// сетку. Раньше бот ради этого запрашивал сегодняшний день только для
	// того, чтобы взять из ответа поле next, и делал два запроса вместо одного.
	date := r.URL.Query().Get("date")
	switch date {
	case "", DateToday:
		date = s.today()
	case DateNext:
		grid, err := s.db.GridForGroup(ctx, groupID)
		if err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
		date = schedule.ShiftWorkday(s.today(), 1, grid)
	}
	if _, err := schedule.ParseDate(date); err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("дата %q не в формате ГГГГ-ММ-ДД", date))
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

	lessons, grid, err := s.load(ctx, groupID, date, date)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, DayResponse{
		Context:   cx,
		Day:       schedule.BuildDay(date, lessons, grid, subgroupID),
		Prev:      schedule.ShiftWorkday(date, -1, grid),
		Next:      schedule.ShiftWorkday(date, 1, grid),
		Grid:      grid,
		Freshness: s.freshness(ctx, groupID, date, date),
	})
}

func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := intParam(r, "group")
	subgroupID := intParam(r, "subgroup")

	monday := r.URL.Query().Get("monday")
	if monday == "" {
		monday = schedule.FormatDate(schedule.MondayOf(time.Now().In(s.loc)))
	}
	start, err := schedule.ParseDate(monday)
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("дата %q не в формате ГГГГ-ММ-ДД", monday))
		return
	}
	// Нормализуем к понедельнику: бот может прислать любую дату недели.
	start = schedule.MondayOf(start)
	monday = schedule.FormatDate(start)
	sunday := schedule.FormatDate(start.AddDate(0, 0, 6))

	cx, err := s.resolveContext(ctx, groupID, subgroupID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	lessons, grid, err := s.load(ctx, groupID, monday, sunday)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, WeekResponse{
		Context:    cx,
		Week:       schedule.BuildWeek(monday, lessons, grid, subgroupID),
		PrevMonday: schedule.FormatDate(start.AddDate(0, 0, -7)),
		NextMonday: schedule.FormatDate(start.AddDate(0, 0, 7)),
		ThisMonday: schedule.FormatDate(schedule.MondayOf(time.Now().In(s.loc))),
		Grid:       grid,
		Freshness:  s.freshness(ctx, groupID, monday, sunday),
	})
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := intParam(r, "group")
	subgroupID := intParam(r, "subgroup")

	// Часовой пояс пользователя может отличаться от вузовского: студент на
	// каникулах в другом регионе всё ещё хочет знать, что идёт «сейчас».
	tz := int(intParam(r, "tz"))
	loc := s.loc
	if tz != 0 {
		loc = time.FixedZone("user", tz*60)
	}
	at := time.Now().In(loc)
	date := schedule.FormatDate(at)

	cx, err := s.resolveContext(ctx, groupID, subgroupID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	lessons, grid, err := s.load(ctx, groupID, date, date)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	day := schedule.BuildDay(date, lessons, grid, subgroupID)

	writeJSON(w, http.StatusOK, NowResponse{
		Context:   cx,
		Now:       schedule.ComputeNow(day, at),
		Day:       day,
		Freshness: s.freshness(ctx, groupID, date, date),
	})
}

// handleChangeDates отдаёт дни, в которых у группы недавно правили расписание.
//
// Список живёт ровно столько, сколько снимки: кнопка «подробнее» из
// позавчерашнего сообщения честно ответит, что показывать уже нечего.
func (s *Server) handleChangeDates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	today := s.today()
	dates, err := s.db.ChangedDates(ctx, intParam(r, "group"), today)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ChangeDatesResponse{Dates: dates, Today: today})
}

// handleChangeDay отдаёт один день до правки и после неё.
//
// Догоняющего запроса к вузу здесь нет намеренно: день, о котором спрашивают,
// синхронизировали минуту назад — правка ровно оттуда и взялась. Лишний поход
// к upstream на каждое нажатие «подробнее» стоил бы дороже всей этой кнопки.
func (s *Server) handleChangeDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := intParam(r, "group")
	subgroupID := intParam(r, "subgroup")

	date := r.URL.Query().Get("date")
	if _, err := schedule.ParseDate(date); err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("дата %q не в формате ГГГГ-ММ-ДД", date))
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

	before, err := s.db.ChangedBefore(ctx, groupID, date)
	known := true
	if errors.Is(err, store.ErrNotFound) {
		known = false
	} else if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	after, err := s.db.Lessons(ctx, groupID, date, date)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	grid, err := s.db.Grid(ctx)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, ChangeDayResponse{
		Context:   cx,
		Date:      date,
		Before:    schedule.BuildDay(date, before, grid, subgroupID),
		After:     schedule.BuildDay(date, after, grid, subgroupID),
		Known:     known,
		Freshness: s.freshness(ctx, groupID, date, date),
	})
}

func (s *Server) handleUserGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	platform := r.URL.Query().Get("platform")
	extID := r.URL.Query().Get("ext_id")

	u, err := s.db.User(ctx, platform, extID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, UserResponse{Known: false})
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	resp := UserResponse{User: u, Known: true}
	if u.GroupID > 0 {
		if g, err := s.db.Group(ctx, u.GroupID); err == nil {
			resp.Group = &g
		}
		resp.GroupTwins, _ = s.db.CountGroupTwins(ctx, u.GroupID)
		if u.SubgroupID > 0 {
			subs, _ := s.db.Subgroups(ctx, u.GroupID)
			for _, sub := range subs {
				if sub.ID == u.SubgroupID {
					found := sub
					resp.Subgroup = &found
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUserSave(w http.ResponseWriter, r *http.Request) {
	var u store.User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if u.Platform == "" || u.ExtID == "" {
		s.fail(w, http.StatusBadRequest, errors.New("не указаны platform и ext_id"))
		return
	}
	if err := s.db.SaveUser(r.Context(), u); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	// Выключил уведомления — значит, и накопленные новости о правках ему
	// больше не адресованы. Иначе человек выключает уведомления и тут же
	// получает сообщение из очереди.
	//
	// Только новости: в той же очереди может ждать ответ автора на обращение
	// этого же человека, и отписка от правок — не повод его проглотить.
	if !u.Notify || !u.Changes {
		if err := s.db.DropOutboxFor(r.Context(), u.Platform, u.ExtID, store.OutboxChange); err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUserMenu(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.db.MarkMenuSent(r.Context(), q.Get("platform"), q.Get("ext_id"), q.Get("version")); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleNotifyList(w http.ResponseWriter, r *http.Request) {
	targets, err := s.db.UsersToNotify(r.Context(), int(intParam(r, "minute")))
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, NotifyResponse{Targets: targets})
}

func (s *Server) handleNotifyMark(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := store.NotifyKind(q.Get("kind"))
	if kind != store.NotifyEvening {
		kind = store.NotifyMorning
	}
	if err := s.db.MarkNotified(r.Context(), q.Get("platform"), q.Get("ext_id"), kind, q.Get("date")); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleNotifyHint(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.db.MarkEmptyHinted(r.Context(), q.Get("platform"), q.Get("ext_id")); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleNotifyOff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	platform, extID := q.Get("platform"), q.Get("ext_id")
	if err := s.db.DisableNotify(r.Context(), platform, extID); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	// Сюда приходят и после блокировки бота. Копить в очереди сообщения,
	// которые заведомо не дойдут, незачем — их всё равно некому читать.
	if err := s.db.DropOutboxFor(r.Context(), platform, extID); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── очередь исходящих ───────────────────────────────────────────────────────
//
// Событие рождается в raspd (расписание месяца изменилось), а доставляет его
// botd — и у каждой платформы свой доставщик. Отсюда три ручки: взять готовое,
// подтвердить отправку, отложить неудачу.

func (s *Server) handleOutboxTake(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	if platform == "" {
		s.fail(w, http.StatusBadRequest, errors.New("не указана платформа"))
		return
	}
	items, err := s.db.TakeOutbox(r.Context(), platform, int(intParam(r, "limit")))
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, OutboxResponse{Items: items})
}

func (s *Server) handleOutboxDone(w http.ResponseWriter, r *http.Request) {
	var expected []string
	if r.Method == http.MethodPost {
		var req struct {
			Payload *string `json:"payload"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
			s.fail(w, 400, err)
			return
		}
		if req.Payload == nil {
			s.fail(w, 400, errors.New("нет payload"))
			return
		}
		expected = append(expected, *req.Payload)
	}
	if err := s.db.OutboxDone(r.Context(), intParam(r, "id"), expected...); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleOutboxFail(w http.ResponseWriter, r *http.Request) {
	after := time.Duration(intParam(r, "after")) * time.Second
	kept, err := s.db.OutboxFail(r.Context(), intParam(r, "id"), after)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"kept": kept})
}

func (s *Server) handleOutboxPut(w http.ResponseWriter, r *http.Request) {
	var req OutboxPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if req.Platform == "" || req.ExtID == "" || req.Kind == "" {
		s.fail(w, http.StatusBadRequest, errors.New("не указаны platform, ext_id или kind"))
		return
	}
	if err := s.db.Enqueue(r.Context(), req.Platform, req.ExtID, req.Kind, req.DedupKey, req.Payload); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── обратная связь ──────────────────────────────────────────────────────────
//
// Обращения хранит raspd, потому что база — его. Кому их доставлять, он не
// знает и знать не должен: автора задаёт конфигурация botd, а сюда приезжает
// только готовая строка очереди.

func (s *Server) handleFeedbackAdd(w http.ResponseWriter, r *http.Request) {
	var f store.Feedback
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if f.Platform == "" || f.ExtID == "" || strings.TrimSpace(f.Text) == "" {
		s.fail(w, http.StatusBadRequest, errors.New("пустое обращение или нет адресата"))
		return
	}
	id, wait, err := s.db.AddFeedback(r.Context(), f)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	// Отказ по частоте — обычный ответ, а не ошибка: боту надо объяснить
	// человеку, сколько ждать, а не показать «сервис недоступен».
	writeJSON(w, http.StatusOK, FeedbackResponse{ID: id, WaitSec: int(wait.Seconds() + 0.5)})
}

func (s *Server) handleFeedbackGet(w http.ResponseWriter, r *http.Request) {
	f, err := s.db.Feedback(r.Context(), intParam(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		s.fail(w, http.StatusNotFound, errors.New("обращение не найдено"))
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, FeedbackResponse{Feedback: &f})
}

func (s *Server) handleFeedbackAnswered(w http.ResponseWriter, r *http.Request) {
	if err := s.db.MarkAnswered(r.Context(), intParam(r, "id")); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── проверка живости адресатов ──────────────────────────────────────────────
//
// Ходят сюда боты: у raspd нет ни токенов площадок, ни права спрашивать их о
// людях. Он умеет только сказать, кого пора проверить, и записать ответ.

func (s *Server) handleProbeList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	targets, err := s.db.UsersToProbe(r.Context(), q.Get("platform"), int(intParam(r, "limit")), time.Now())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ProbeResponse{Targets: targets})
}

func (s *Server) handleProbeMark(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.db.MarkProbed(r.Context(), q.Get("platform"), q.Get("ext_id"), q.Get("reachable") == "1"); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUsersCount(w http.ResponseWriter, r *http.Request) {
	n, err := s.db.CountUsers(r.Context())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, CountResponse{Count: n})
}
