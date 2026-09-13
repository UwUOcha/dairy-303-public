package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// upstream — управляемая заглушка адаптера. Пустой месяц — валидный ответ:
// расписание на месяц может быть просто не заведено.
type upstream struct {
	*httptest.Server
	// gate, если он не nil, держит ответ до закрытия канала.
	gate chan struct{}
	hits chan struct{}
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{hits: make(chan struct{}, 16)}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case u.hits <- struct{}{}:
		default:
		}
		if u.gate != nil {
			select {
			case <-u.gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func testSyncer(t *testing.T, base string) (*Syncer, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	client := fixtureSource{base: base}
	return New(client, db, Options{Location: time.UTC, MonthTimeout: 10 * time.Second}, testLog()), db
}

// Перезапуск должен сразу подхватывать изменения каталога, даже когда база не
// пустая и суточный интервал ещё не вышел. Иначе удалённый upstream двойник
// продолжает торчать в настройках почти сутки.
func TestBootstrapRefreshesNonEmptyGroupTree(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Departments":[{"ID":10,"Name":"Учебный факультет"}],"Groups":[{"ID":545,"Name":"ГР-22","DepartmentID":10,"Active":true}]}`)
	}))
	defer srv.Close()

	s, db := testSyncer(t, srv.URL)
	ctx := context.Background()
	if err := db.SaveGroupTree(ctx, importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Учебный факультет"}},
		Groups: []importdata.Group{
			{ID: 39, Name: "ГР-22", DepartmentID: 10, Course: 2, Active: true},
			{ID: 545, Name: "ГР-22", DepartmentID: 10, Course: 1, Active: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("запросов дерева при запуске = %d, ожидался один", hits)
	}
	old, err := db.Group(ctx, 39)
	if err != nil {
		t.Fatal(err)
	}
	if old.Active {
		t.Error("исчезнувшая запись id=39 осталась активной после запуска")
	}
}

// Ожидающий не должен зависеть от того, кто именно оказался лидером
// singleflight. Раньше зависел: Do контекста не знает вовсе, и запрос
// пользователя с потолком в шесть секунд простаивал ровно столько, сколько
// работал фоновый синк.
func TestSyncMonthWaiterRespectsOwnDeadline(t *testing.T) {
	up := newUpstream(t)
	up.gate = make(chan struct{})
	s, _ := testSyncer(t, up.URL)

	leaderDone := make(chan error, 1)
	go func() { leaderDone <- s.SyncMonth(context.Background(), 39, 2026, 9) }()
	<-up.hits // лидер дошёл до upstream и застрял там

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := s.SyncMonth(ctx, 39, 2026, 9)
	waited := time.Since(started)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ожидание = %v, ожидался DeadlineExceeded", err)
	}
	if waited > time.Second {
		t.Errorf("ожидающий провисел %v — значит, ждал лидера, а не свой контекст", waited)
	}

	close(up.gate)
	if err := <-leaderDone; err != nil {
		t.Fatalf("лидер: %v", err)
	}
}

// Отвал того, кто заказал загрузку первым, не повод бросать её для всех
// остальных: работа идёт в контексте, отвязанном от заказчика.
func TestSyncMonthSurvivesCallerCancel(t *testing.T) {
	up := newUpstream(t)
	up.gate = make(chan struct{})
	s, db := testSyncer(t, up.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.SyncMonth(ctx, 39, 2026, 9) }()
	<-up.hits

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("заказчик получил %v, ожидался Canceled", err)
	}
	close(up.gate)

	// Загрузка должна дойти до базы, хотя заказчика уже нет.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := db.MonthState(context.Background(), 39, 2026, 9); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("месяц не записан: работу убило отменой заказчика")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Одновременные запросы одного месяца схлопываются в один поход к вузу.
func TestSyncMonthCollapsesConcurrent(t *testing.T) {
	up := newUpstream(t)
	up.gate = make(chan struct{})
	s, _ := testSyncer(t, up.URL)

	const callers = 5
	done := make(chan error, callers)
	for range callers {
		go func() { done <- s.SyncMonth(context.Background(), 39, 2026, 9) }()
	}
	<-up.hits
	time.Sleep(50 * time.Millisecond)
	close(up.gate)
	for range callers {
		if err := <-done; err != nil {
			t.Fatalf("вызов: %v", err)
		}
	}

	if extra := len(up.hits); extra != 0 {
		t.Errorf("походов к вузу лишних: %d, ожидался ровно один на всех", extra)
	}
}

// Ночной обход не должен пропадать оттого, что демон перезапускается позже
// назначенного часа. И не должен откладываться на ночь, когда расписания нет
// вовсе: без занятий каталог не отличает настоящую запись от двойника.
func TestFullSyncOverdue(t *testing.T) {
	s, db := testSyncer(t, "http://127.0.0.1:1")
	ctx := context.Background()

	if !s.fullSyncOverdue(ctx) {
		t.Error("свежая установка: занятий нет, обход нужен сразу, а не в 03:00")
	}

	// Появились занятия, а отметки по-прежнему нет — так выглядит база,
	// поднятая из бэкапа. Гнать вузу лишний обход из-за пропавшей строчки
	// в meta незачем.
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{
		GroupID: 39, Year: 2026, Month: 9,
		LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 8*60 + 30, MinuteTo: 10*60 + 5, Label: "08:30 - 10:05"}},
		Lessons: []importdata.Lesson{{
			ID: 1, Date: "2026-09-01", LessonTimeID: 1,
			Discipline: "Анатомия", GroupID: 39,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if s.fullSyncOverdue(ctx) {
		t.Error("занятия есть, отметки нет — это бэкап, а не свежая установка")
	}

	stamp := func(ago time.Duration) {
		if err := db.SetMeta(ctx, store.MetaFullSyncAt,
			fmt.Sprint(time.Now().Add(-ago).Unix())); err != nil {
			t.Fatal(err)
		}
	}

	stamp(2 * time.Hour)
	if s.fullSyncOverdue(ctx) {
		t.Error("обход два часа назад — не просрочен")
	}

	stamp(3 * 24 * time.Hour)
	if !s.fullSyncOverdue(ctx) {
		t.Error("обхода не было трое суток — надо нагонять, а не ждать 03:00")
	}
}

// Отметка о проходе по горячим группам ставится и тогда, когда обходить
// некого: иначе каждый перезапуск демона считал бы контур не отработавшим.
func TestHotSyncStampsMeta(t *testing.T) {
	s, db := testSyncer(t, "http://127.0.0.1:1")
	ctx := context.Background()

	if err := s.HotSync(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Meta(ctx, store.MetaHotSyncAt); v == "" {
		t.Error("отметка hot_sync_at не поставлена")
	}
}

// Проход, на котором часть месяцев не далась, всё равно считается
// состоявшимся: иначе несколько неудачных запросов заставляли бы контур
// обходить вуз заново раз в час.
func TestFullSyncStampsAfterPartialFailure(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n%2 == 0 {
			http.Error(w, "боль", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	s, db := testSyncer(t, srv.URL)
	ctx := context.Background()
	must(t, db.SaveGroupTree(ctx, importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Институт"}},
		Groups: []importdata.Group{
			{ID: 39, Name: "ГР-22", DepartmentID: 10, Course: 2, Active: true},
			{ID: 40, Name: "ГР-21", DepartmentID: 10, Course: 2, Active: true},
		},
	}))

	if err := s.FullSync(ctx); err == nil {
		t.Fatal("ожидалась ошибка: часть месяцев не загрузилась")
	}
	if v, _ := db.Meta(ctx, store.MetaFullSyncAt); v == "" {
		t.Error("отметка о проходе не поставлена — контур пойдёт обходить вуз заново")
	}
	if s.fullSyncOverdue(ctx) {
		t.Error("обход, только что состоявшийся, объявлен просроченным")
	}
}

// Обход, потерявший часть месяцев, отличим от сломанного контура: один
// таймаут на девятьсот запросов не должен поднимать алерт и уводить полный
// контур на часовой повтор.
func TestFullSyncPartialFailureIsDistinguishable(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 2 {
			http.Error(w, "боль", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	s, db := testSyncer(t, srv.URL)
	ctx := context.Background()
	must(t, db.SaveGroupTree(ctx, importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Институт"}},
		Groups: []importdata.Group{
			{ID: 39, Name: "ГР-22", DepartmentID: 10, Course: 2, Active: true},
			{ID: 40, Name: "ГР-21", DepartmentID: 10, Course: 2, Active: true},
		},
	}))

	var partial *PartialError
	err := s.FullSync(ctx)
	if !errors.As(err, &partial) {
		t.Fatalf("ожидался PartialError, получено %v", err)
	}
	if partial.Failed != 1 || partial.Requests != 2 {
		t.Errorf("потери описаны как %d из %d, ожидалось 1 из 2", partial.Failed, partial.Requests)
	}
}

// Upstream, не ответивший ни разу, — это уже поломка, а не рябь: такой обход
// должен доходить до алерта обычной ошибкой.
func TestFullSyncTotalFailureIsNotPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "боль", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s, db := testSyncer(t, srv.URL)
	ctx := context.Background()
	must(t, db.SaveGroupTree(ctx, importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Институт"}},
		Groups:      []importdata.Group{{ID: 39, Name: "ГР-22", DepartmentID: 10, Course: 2, Active: true}},
	}))

	var partial *PartialError
	err := s.FullSync(ctx)
	if err == nil {
		t.Fatal("ожидалась ошибка: upstream не ответил ни разу")
	}
	if errors.As(err, &partial) {
		t.Error("обход без единого удачного запроса выдан за частичную потерю")
	}
}

// Отозванный токен обрывает обход целиком, и отметку ставить не за что:
// вуза мы так и не обошли.
func TestFullSyncDoesNotStampOnAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s, db := testSyncer(t, srv.URL)
	ctx := context.Background()
	must(t, db.SaveGroupTree(ctx, importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Институт"}},
		Groups:      []importdata.Group{{ID: 39, Name: "ГР-22", DepartmentID: 10, Course: 2, Active: true}},
	}))

	if err := s.FullSync(ctx); !errors.Is(err, importdata.ErrAuth) {
		t.Fatalf("ошибка = %v, ожидался ErrAuth", err)
	}
	if v, _ := db.Meta(ctx, store.MetaFullSyncAt); v != "" {
		t.Error("отметка поставлена, хотя обход не состоялся")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestFullSyncWithoutCatalogDoesNotPostponeInitialImport(t *testing.T) {
	up := newUpstream(t)
	s, db := testSyncer(t, up.URL)
	ctx := context.Background()
	if err := s.FullSync(ctx); err == nil {
		t.Fatal("empty catalog reported as a completed full import")
	}
	if stamp, err := db.Meta(ctx, store.MetaFullSyncAt); err != nil || stamp != "" {
		t.Fatalf("empty import stamped: %q %v", stamp, err)
	}
}
