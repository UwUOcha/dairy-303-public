package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// stubSync — синхронизатор, которого нет. Пользовательский путь обязан
// работать и без него: локальная копия ровно для того и держится.
type stubSync struct {
	last     error
	ensured  int
	ensueErr error
}

func (s *stubSync) EnsureRange(context.Context, int64, string, string) error {
	s.ensured++
	return s.ensueErr
}
func (s *stubSync) SyncMonth(context.Context, int64, int, int) error { return nil }
func (s *stubSync) LastError() error                                 { return s.last }

func testServer(t *testing.T) (*httptest.Server, *store.DB, *stubSync) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	tree := importdata.GroupTree{
		Departments: []importdata.Department{{ID: 10, Name: "Учебный факультет"}},
		Groups: []importdata.Group{
			{ID: 39, Name: "ГР-22", DepartmentID: 10, Course: 2, Active: true},
		},
	}
	if err := db.SaveGroupTree(context.Background(), tree); err != nil {
		t.Fatal(err)
	}

	sync := &stubSync{}
	srv := httptest.NewServer(NewServer(db, sync, time.UTC, 24*time.Hour,
		slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	return srv, db, sync
}

func get(t *testing.T, srv *httptest.Server, path, query string, dst any) int {
	t.Helper()
	url := srv.URL + path
	if query != "" {
		url += "?" + query
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			t.Fatalf("%s: разбор ответа: %v", path, err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode
}

// Главная поправка healthcheck: сломанный фоновый синк — это повод для
// алерта, а не для того, чтобы объявлять сервис мёртвым. Раньше объявлял, и
// недоступный на старте адаптер уводил botd в цикл перезапусков при полной базе.
func TestHealthSurvivesBrokenSync(t *testing.T) {
	srv, _, sync := testServer(t)
	sync.last = errors.New("provider: токен отвергнут")

	var h HealthResponse
	if code := get(t, srv, PathHealth, "", &h); code != http.StatusOK {
		t.Errorf("код = %d, ожидался 200: сломанный синк не отменяет живости", code)
	}
	if !h.OK {
		t.Error("ok = false при живой базе")
	}
	if h.SyncError == "" {
		t.Error("состояние синка потерялось — алертить будет не по чему")
	}
	if h.Groups != 1 {
		t.Errorf("групп = %d, ожидалась 1", h.Groups)
	}
}

// «Завтра» — это следующий учебный день, и знает его только сервер: сетка
// учебных дней есть здесь. Бот раньше выяснял это вторым запросом.
func TestDayNextResolvesOnServer(t *testing.T) {
	srv, _, _ := testServer(t)

	var today DayResponse
	if code := get(t, srv, PathDay, "group=39&date="+DateToday, &today); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}
	var next DayResponse
	if code := get(t, srv, PathDay, "group=39&date="+DateNext, &next); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}

	if next.Day.Date != today.Next {
		t.Errorf("date=next дал %s, а сегодняшний ответ обещал %s", next.Day.Date, today.Next)
	}
	if d, err := schedule.ParseDate(next.Day.Date); err == nil && d.Weekday() == time.Sunday {
		t.Error("следующий учебный день не может быть воскресеньем")
	}
}

func TestDayRejectsGarbageDate(t *testing.T) {
	srv, _, _ := testServer(t)
	if code := get(t, srv, PathDay, "group=39&date=позавчера", nil); code != http.StatusBadRequest {
		t.Errorf("код = %d, ожидался 400", code)
	}
}

// Очередь исходящих: взять, подтвердить, отложить.
func TestOutboxRoundTrip(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()

	subscriber := store.User{Platform: "tg", ExtID: "1", GroupID: 39}.WithDefaults(180)
	subscriber.Platform, subscriber.ExtID, subscriber.GroupID = "tg", "1", 39
	must(t, db.SaveUser(ctx, subscriber))
	// Второй адресат выключил уведомления: ему правки не адресованы.
	must(t, db.SaveUser(ctx, store.User{Platform: "tg", ExtID: "2", GroupID: 39, TZOffset: 180}))

	n, err := db.EnqueueChange(ctx, 39, 2026, 9)
	must(t, err)
	if n != 1 {
		t.Fatalf("в очередь попало %d адресатов, ожидался 1 — тот, кто включал рассылку", n)
	}
	// Повтор той же правки не должен множить сообщения.
	again, err := db.EnqueueChange(ctx, 39, 2026, 9)
	must(t, err)
	if again != 0 {
		t.Errorf("повторная правка добавила %d сообщений, ожидалось 0", again)
	}

	var out OutboxResponse
	if code := get(t, srv, PathOutboxTake, "platform=tg&limit=10", &out); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}
	if len(out.Items) != 1 {
		t.Fatalf("выдано %d сообщений, ожидалось 1", len(out.Items))
	}
	it := out.Items[0]
	if it.Kind != store.OutboxChange || it.ExtID != "1" {
		t.Errorf("не то сообщение: %+v", it)
	}

	// Неудача откладывает повтор — сразу забрать его нельзя.
	if code := get(t, srv, PathOutboxFail, "id="+itoa(it.ID)+"&after=600", nil); code != http.StatusOK {
		t.Fatalf("fail: код = %d", code)
	}
	out = OutboxResponse{}
	get(t, srv, PathOutboxTake, "platform=tg", &out)
	if len(out.Items) != 0 {
		t.Errorf("отложенное сообщение выдано снова: %+v", out.Items)
	}

	// Подтверждение убирает его насовсем.
	if code := get(t, srv, PathOutboxDone, "id="+itoa(it.ID), nil); code != http.StatusOK {
		t.Fatalf("done: код = %d", code)
	}
	left, err := db.TakeOutbox(ctx, "tg", 10)
	must(t, err)
	if len(left) != 0 {
		t.Errorf("после подтверждения в очереди осталось %d", len(left))
	}
}

// Выключил уведомления — накопленные новости больше не адресованы. Иначе
// человек снимает галочку и тут же получает сообщение из очереди.
func TestDisablingNotifyDropsQueued(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()

	u := store.User{}.WithDefaults(180)
	u.Platform, u.ExtID, u.GroupID = "tg", "1", 39
	must(t, db.SaveUser(ctx, u))
	if _, err := db.EnqueueChange(ctx, 39, 2026, 9); err != nil {
		t.Fatal(err)
	}

	u.Notify = false
	body, _ := json.Marshal(u)
	resp, err := http.Post(srv.URL+PathUserSave, "application/json", bytesReader(body))
	must(t, err)
	resp.Body.Close()

	left, err := db.TakeOutbox(ctx, "tg", 10)
	must(t, err)
	if len(left) != 0 {
		t.Errorf("в очереди осталось %d сообщений для отписавшегося", len(left))
	}
}

// Отметка о показе меню не должна затирать остальные настройки: её ставит
// обработчик, который в ту же секунду мог поменять группу.
func TestMarkMenuSentKeepsSettings(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()

	must(t, db.SaveUser(ctx, store.User{
		Platform: "tg", ExtID: "1", GroupID: 39, SubgroupID: 61, TZOffset: 180,
	}))
	if code := get(t, srv, PathUserMenu, "platform=tg&ext_id=1&version=v1:test", nil); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}

	u, err := db.User(ctx, "tg", "1")
	must(t, err)
	if !u.MenuSent || u.MenuVersion != "v1:test" {
		t.Error("отметка не сохранилась")
	}
	if u.GroupID != 39 || u.SubgroupID != 61 {
		t.Errorf("настройки затёрты: группа=%d подгруппа=%d", u.GroupID, u.SubgroupID)
	}
}

func TestUnknownUserIsNotAnError(t *testing.T) {
	srv, _, _ := testServer(t)
	var resp UserResponse
	if code := get(t, srv, PathUserGet, "platform=tg&ext_id=нет", &resp); code != http.StatusOK {
		t.Errorf("код = %d, ожидался 200", code)
	}
	if resp.Known {
		t.Error("незнакомый пользователь объявлен известным")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// «Что изменилось»: сервер отдаёт список задетых дней и сравнение одного дня.
//
// Даты нарочно далеко в будущем: границу «правка прошлого не новость» ставит
// системное время, и тест не должен зависеть от того, какой сегодня месяц.
func TestChangeEndpoints(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()

	times := []importdata.LessonTime{
		{ID: 1, MinuteFrom: 8*60 + 30, MinuteTo: 10*60 + 5, Label: "08:30 - 10:05"},
		{ID: 2, MinuteFrom: 10*60 + 25, MinuteTo: 12 * 60, Label: "10:25 - 12:00"},
	}
	base := importdata.MonthSchedule{
		GroupID: 39, Year: 2099, Month: 9, LessonTimes: times,
		Lessons: []importdata.Lesson{
			{ID: 1, Date: "2099-09-18", LessonTimeID: 1, Discipline: "Химия", Classroom: "305", GroupID: 39},
			{ID: 2, Date: "2099-09-18", LessonTimeID: 2, Discipline: "Физика", Classroom: "210", GroupID: 39},
		},
	}
	if _, err := db.SaveMonth(ctx, base); err != nil {
		t.Fatal(err)
	}
	moved := base
	moved.Lessons = []importdata.Lesson{
		{ID: 1, Date: "2099-09-18", LessonTimeID: 1, Discipline: "Химия", Classroom: "401", GroupID: 39},
	}
	if changed, err := db.SaveMonth(ctx, moved); err != nil || !changed {
		t.Fatalf("правка не признана изменением: changed=%v err=%v", changed, err)
	}

	var dates ChangeDatesResponse
	if code := get(t, srv, PathChangeDates, "group=39", &dates); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}
	if len(dates.Dates) != 1 || dates.Dates[0] != "2099-09-18" {
		t.Fatalf("изменённые дни = %v", dates.Dates)
	}

	var day ChangeDayResponse
	if code := get(t, srv, PathChangeDay, "group=39&date=2099-09-18", &day); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}
	if !day.Known {
		t.Fatal("снимок «до» должен быть на месте")
	}
	if len(day.Before.Items) != 2 || day.Before.Items[0].Classroom != "305" {
		t.Errorf("«до» = %+v, ожидались две пары и аудитория 305", day.Before.Items)
	}
	if len(day.After.Items) != 1 || day.After.Items[0].Classroom != "401" {
		t.Errorf("«после» = %+v, ожидалась одна пара в 401", day.After.Items)
	}

	// День, о котором снимка нет, — не ошибка: кнопка могла пережить снимок.
	day = ChangeDayResponse{}
	if code := get(t, srv, PathChangeDay, "group=39&date=2099-09-19", &day); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}
	if day.Known {
		t.Error("для дня без снимка known должен быть false")
	}

	// Кривая дата — честный 400, а не пустой ответ.
	if code := get(t, srv, PathChangeDay, "group=39&date=завтра", nil); code != http.StatusBadRequest {
		t.Errorf("код = %d, ожидался 400", code)
	}
}

// Ручки проверки живости: raspd отдаёт очередь и принимает ответ площадки —
// сам он спросить не может, ключей площадок у него нет.
func TestProbeRoundTrip(t *testing.T) {
	srv, db, _ := testServer(t)
	ctx := context.Background()

	u := store.User{Platform: "tg", ExtID: "77", GroupID: 39}.WithDefaults(180)
	u.Platform, u.ExtID, u.GroupID = "tg", "77", 39
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	var list ProbeResponse
	if code := get(t, srv, PathProbeList, "platform=tg&limit=10", &list); code != 200 {
		t.Fatalf("очередь проверки ответила %d", code)
	}
	if len(list.Targets) != 1 || list.Targets[0].ExtID != "77" {
		t.Fatalf("очередь проверки = %+v", list.Targets)
	}

	if code := get(t, srv, PathProbeMark, "platform=tg&ext_id=77&reachable=0", nil); code != 200 {
		t.Fatalf("отметка проверки ответила %d", code)
	}
	got, err := db.User(ctx, "tg", "77")
	if err != nil {
		t.Fatal(err)
	}
	if got.Notify {
		t.Error("заблокировавшему бота оставили рассылку")
	}

	// Проверенный уходит из очереди до следующего срока.
	list = ProbeResponse{}
	if code := get(t, srv, PathProbeList, "platform=tg&limit=10", &list); code != 200 {
		t.Fatalf("повторный запрос очереди ответил %d", code)
	}
	if len(list.Targets) != 0 {
		t.Errorf("только что проверенный остался в очереди: %+v", list.Targets)
	}
}

// Поиск группы отвечает и на название подгруппы.
//
// Отдельная проверка на стыке, а не только в хранилище: без подгруппы в ответе
// бот не отличает «такого нет» от «человек назвал подгруппу», и обе развилки
// сходятся в отказ.
func TestGroupSearchFindsSubgroup(t *testing.T) {
	srv, db, _ := testServer(t)
	if err := db.SaveSubgroups(context.Background(), 39, []importdata.Subgroup{
		{ID: 61, Name: "ГР-22/1"}, {ID: 62, Name: "ГР-22/2"},
	}); err != nil {
		t.Fatal(err)
	}

	var byGroup GroupsResponse
	if code := get(t, srv, PathGroupSearch, "q=гр22", &byGroup); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}
	if len(byGroup.Groups) != 1 || byGroup.Subgroup != nil {
		t.Errorf("название группы обязано разбираться группой: %+v", byGroup)
	}

	var bySub GroupsResponse
	if code := get(t, srv, PathGroupSearch, "q="+url.QueryEscape("ГР-22/2"), &bySub); code != http.StatusOK {
		t.Fatalf("код = %d", code)
	}
	if len(bySub.Groups) != 0 {
		t.Errorf("групп с таким названием нет: %+v", bySub.Groups)
	}
	if bySub.Subgroup == nil || bySub.Subgroup.ID != 62 {
		t.Fatalf("подгруппа не найдена: %+v", bySub.Subgroup)
	}
	if bySub.Subgroup.GroupID != 39 {
		t.Errorf("подгруппа приехала без своей группы: %+v", bySub.Subgroup)
	}

	var none GroupsResponse
	get(t, srv, PathGroupSearch, "q="+url.QueryEscape("ГР-22/9"), &none)
	if len(none.Groups) != 0 || none.Subgroup != nil {
		t.Errorf("несуществующая подгруппа не должна находиться: %+v", none)
	}
}
