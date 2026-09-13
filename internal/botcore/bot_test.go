package botcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// fakeRasp — заглушка демона расписания на unix-сокете.
//
// Она намеренно повторяет то поведение настоящего raspd, на котором ломался
// онбординг: незнакомый пользователь отдаётся пустой записью, а сохранение без
// platform и ext_id отвергается с 400. Заглушка, которая молча принимает любой
// запрос, этот баг бы не поймала.
type fakeRasp struct {
	users     map[string]store.User
	groups    map[int64]store.Group
	subgroups map[int64][]store.Subgroup
	// dayCalls — сколько раз спрашивали расписание дня и с какой датой.
	dayDates   []string
	nextStarts []string
	// changed — дни, о правке которых заглушка знает, по группам.
	changed map[int64][]string
	// feedback — принятые обращения по номеру, next — следующий номер.
	feedback map[int64]store.Feedback
	next     int64
	// tooOften — заглушка отвечает отказом по частоте: столько ждать.
	tooOften time.Duration
	// outbox — что бот попросил доставить, в порядке постановки.
	outbox []api.OutboxPutRequest
	// users_count — что отвечать на счётчик для экрана «О проекте».
	count             int
	outboxUnavailable bool
	dayFreshness      api.Freshness
}

func key(platform, extID string) string { return platform + "|" + extID }

// twinsOf повторяет порядок настоящего raspd: первой идёт копия, которую он
// считает настоящей, — та, у которой расписание нового года.
func (f *fakeRasp) twinsOf(name string) []store.Twin {
	var out []store.Twin
	for _, g := range f.groups {
		if g.Name != name {
			continue
		}
		t := store.Twin{Group: g}
		if g.ID == 545 {
			t.HasCurrent, t.LastDate = true, "2026-09-30"
		} else if g.ID == 39 {
			t.Shadowed, t.LastDate = true, "2026-06-10"
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Shadowed != out[j].Shadowed {
			return !out[i].Shadowed
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// findSubgroup повторяет store.FindSubgroup: имя подгруппы вуз пишет и
// целиком («ГР-22/2»), и одним номером.
func (f *fakeRasp) findSubgroup(norm string) *store.Subgroup {
	if norm == "" {
		return nil
	}
	ids := slices.Sorted(maps.Keys(f.subgroups))
	for _, id := range ids {
		g, ok := f.groups[id]
		if !ok || f.twinsOf(g.Name)[0].ID != id && len(f.twinsOf(g.Name)) > 1 {
			// Спрятанная копия каталога: настоящей считается первая.
			continue
		}
		for _, s := range f.subgroups[id] {
			if store.NormalizeName(s.Name) == norm ||
				store.NormalizeName(g.Name)+store.NormalizeName(s.Name) == norm {
				return &s
			}
		}
	}
	return nil
}

func newFakeRasp(t *testing.T) *api.Client {
	c, _ := newFakeRaspWithState(t)
	return c
}

func newFakeRaspWithState(t *testing.T) (*api.Client, *fakeRasp) {
	t.Helper()
	f := &fakeRasp{
		users: map[string]store.User{},
		groups: map[int64]store.Group{
			232: {ID: 232, Name: "ГР-12", DepartmentID: 10, Department: "Учебный факультет", Course: 1, Active: true},
			39:  {ID: 39, Name: "ГР-22", DepartmentID: 10, Department: "Учебный факультет", Course: 2, Active: true},
			// Копия той же группы под названием нового учебного года: имя
			// совпадает до буквы, курс в каталоге на единицу меньше, и
			// расписание нового года заведено именно у неё.
			545: {ID: 545, Name: "ГР-22", DepartmentID: 10, Department: "Учебный факультет", Course: 1, Active: true},
		},
		changed:  map[int64][]string{},
		feedback: map[int64]store.Feedback{},
		subgroups: map[int64][]store.Subgroup{
			232: {{ID: 334, GroupID: 232, Name: "ГР-12/1"}, {ID: 335, GroupID: 232, Name: "ГР-12/2"}},
			39:  {{ID: 61, GroupID: 39, Name: "ГР-22/1"}, {ID: 62, GroupID: 39, Name: "ГР-22/2"}},
			545: {{ID: 707, GroupID: 545, Name: "ГР-22/1"}, {ID: 708, GroupID: 545, Name: "ГР-22/2"}},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathUserGet, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		u, ok := f.users[key(q.Get("platform"), q.Get("ext_id"))]
		if !ok {
			// Как настоящий raspd: пустая запись без идентификатора.
			writeJSON(w, api.UserResponse{Known: false})
			return
		}
		resp := api.UserResponse{User: u, Known: true}
		if g, ok := f.groups[u.GroupID]; ok {
			resp.Group = &g
			resp.GroupTwins = len(f.twinsOf(g.Name))
		}
		for _, s := range f.subgroups[u.GroupID] {
			if s.ID == u.SubgroupID {
				found := s
				resp.Subgroup = &found
			}
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc(api.PathUserSave, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var u store.User
		if err := json.Unmarshal(body, &u); err != nil {
			http.Error(w, `{"error":"кривой JSON"}`, http.StatusBadRequest)
			return
		}
		if u.Platform == "" || u.ExtID == "" {
			http.Error(w, `{"error":"не указаны platform и ext_id"}`, http.StatusBadRequest)
			return
		}
		f.users[key(u.Platform, u.ExtID)] = u
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc(api.PathGroupSearch, func(w http.ResponseWriter, r *http.Request) {
		q := store.NormalizeName(r.URL.Query().Get("q"))
		var out []store.Group
		for _, g := range f.groups {
			if strings.Contains(store.NormalizeName(g.Name), q) {
				out = append(out, g)
			}
		}
		// Как настоящий raspd: устойчивый порядок и уважение к limit — без
		// него не проверить, что бот замечает обрезанный список.
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		// Как настоящий raspd: ни одной группы — повод проверить, не написал ли
		// человек название подгруппы. Двойники каталога сюда не попадают, иначе
		// «ГР-22/2» уводило бы в копию, из которой бот сам людей выселяет.
		if len(out) == 0 {
			if sub := f.findSubgroup(q); sub != nil {
				writeJSON(w, api.GroupsResponse{Subgroup: sub})
				return
			}
		}
		writeJSON(w, api.GroupsResponse{Groups: out})
	})
	mux.HandleFunc(api.PathGroupTwins, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
		g, ok := f.groups[id]
		if !ok {
			writeJSON(w, api.TwinsResponse{})
			return
		}
		writeJSON(w, api.TwinsResponse{Twins: f.twinsOf(g.Name)})
	})
	mux.HandleFunc(api.PathSubgroups, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
		writeJSON(w, api.SubgroupsResponse{Subgroups: f.subgroups[id]})
	})
	mux.HandleFunc(api.PathDepartments, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, api.DepartmentsResponse{
			Departments: []store.Department{{ID: 10, Name: "Учебный факультет"}},
		})
	})
	mux.HandleFunc(api.PathGroupList, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("course") == "" {
			writeJSON(w, api.CoursesResponse{Courses: []int{1, 2}})
			return
		}
		course, _ := strconv.Atoi(r.URL.Query().Get("course"))
		var out []store.Group
		for _, g := range f.groups {
			if g.Course == course {
				out = append(out, g)
			}
		}
		// Как настоящий raspd: ORDER BY name. Без устойчивого порядка не
		// проверить листание — страницы разъезжались бы от запуска к запуску.
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		writeJSON(w, api.GroupsResponse{Groups: out})
	})
	mux.HandleFunc(api.PathUserMenu, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		k := key(q.Get("platform"), q.Get("ext_id"))
		if u, ok := f.users[k]; ok {
			// Как настоящий raspd: точечный UPDATE, остальные поля не трогаем.
			u.MenuSent = true
			u.MenuVersion = q.Get("version")
			f.users[k] = u
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc(api.PathNotifyList, func(w http.ResponseWriter, _ *http.Request) {
		// Минуту заглушка не считает: её арифметика проверяется в store, а
		// здесь важно поведение рассыльщика, а не выборка.
		var out []store.NotifyTarget
		for _, u := range f.users {
			if !u.Notify || u.GroupID == 0 {
				continue
			}
			if u.Morning {
				out = append(out, store.NotifyTarget{User: u, Kind: store.NotifyMorning})
			}
			if u.Evening {
				out = append(out, store.NotifyTarget{User: u, Kind: store.NotifyEvening})
			}
		}
		writeJSON(w, api.NotifyResponse{Targets: out})
	})
	mux.HandleFunc(api.PathNotifyMark, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		k := key(q.Get("platform"), q.Get("ext_id"))
		if u, ok := f.users[k]; ok {
			if store.NotifyKind(q.Get("kind")) == store.NotifyEvening {
				u.NotifiedEveOn = q.Get("date")
			} else {
				u.NotifiedOn = q.Get("date")
			}
			f.users[k] = u
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc(api.PathNotifyHint, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		k := key(q.Get("platform"), q.Get("ext_id"))
		if u, ok := f.users[k]; ok {
			u.EmptyHinted = true
			f.users[k] = u
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc(api.PathNextLesson, func(w http.ResponseWriter, r *http.Request) {
		f.nextStarts = append(f.nextStarts, r.URL.Query().Get("from"))
		writeJSON(w, api.NextLessonResponse{Context: api.Context{Today: "2025-09-19"}, From: "2025-09-20", To: "2025-10-03"})
	})
	mux.HandleFunc(api.PathDay, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
		date := r.URL.Query().Get("date")
		f.dayDates = append(f.dayDates, date)
		// Сетка учебных дней есть только у сервера, поэтому слово «следующий»
		// разворачивает он.
		day := "2025-09-19"
		if date == api.DateNext {
			day = "2025-09-20"
		} else if date != "" && date != api.DateToday {
			day = date
		}
		writeJSON(w, api.DayResponse{
			Freshness: f.dayFreshness,
			Context:   api.Context{Group: f.groups[id], Today: "2025-09-19"},
			Day:       schedule.Day{Date: day, Workday: true},
			Prev:      "2025-09-18", Next: "2025-09-20",
		})
	})

	mux.HandleFunc(api.PathChangeDates, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
		writeJSON(w, api.ChangeDatesResponse{Dates: f.changed[id], Today: "2025-09-19"})
	})
	mux.HandleFunc(api.PathChangeDay, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
		date := r.URL.Query().Get("date")
		resp := api.ChangeDayResponse{
			Context: api.Context{Group: f.groups[id], Today: "2025-09-19"},
			Date:    date,
			Known:   slices.Contains(f.changed[id], date),
		}
		if resp.Known {
			resp.Before = schedule.Day{Date: date, Workday: true, Items: []schedule.Item{
				{Number: 1, Lesson: schedule.Lesson{ID: 1, Date: date, TimeLabel: "08:30 - 10:05", Discipline: "Химия", Classroom: "305"}},
				{Number: 2, Lesson: schedule.Lesson{ID: 2, Date: date, TimeLabel: "10:25 - 12:00", Discipline: "Физика", Classroom: "210"}},
			}}
			resp.After = schedule.Day{Date: date, Workday: true, Items: []schedule.Item{
				{Number: 2, Lesson: schedule.Lesson{ID: 2, Date: date, TimeLabel: "10:25 - 12:00", Discipline: "Физика", Classroom: "210"}},
			}}
		}
		writeJSON(w, resp)
	})

	mux.HandleFunc(api.PathFeedbackAdd, func(w http.ResponseWriter, r *http.Request) {
		var fb store.Feedback
		if err := json.NewDecoder(r.Body).Decode(&fb); err != nil {
			http.Error(w, `{"error":"кривой JSON"}`, http.StatusBadRequest)
			return
		}
		if f.tooOften > 0 {
			writeJSON(w, api.FeedbackResponse{WaitSec: int(f.tooOften.Seconds())})
			return
		}
		f.next++
		fb.ID = f.next
		fb.CreatedAt = time.Now()
		f.feedback[fb.ID] = fb
		writeJSON(w, api.FeedbackResponse{ID: fb.ID})
	})
	mux.HandleFunc(api.PathFeedbackGet, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		fb, ok := f.feedback[id]
		if !ok {
			http.Error(w, `{"error":"нет такого"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, api.FeedbackResponse{Feedback: &fb})
	})
	mux.HandleFunc(api.PathFeedbackAns, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if fb, ok := f.feedback[id]; ok {
			fb.AnsweredAt = time.Now()
			f.feedback[id] = fb
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc(api.PathOutboxPut, func(w http.ResponseWriter, r *http.Request) {
		if f.outboxUnavailable {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var req api.OutboxPutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"кривой JSON"}`, http.StatusBadRequest)
			return
		}
		f.outbox = append(f.outbox, req)
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc(api.PathUsersCount, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, api.CountResponse{Count: f.count})
	})

	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("сокет заглушки: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	return api.NewClient(sock), f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func testBot(t *testing.T) *Bot {
	t.Helper()
	return New(newFakeRasp(t), 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func handle(t *testing.T, b *Bot, u Update) []Reply {
	t.Helper()
	rs, err := b.Handle(context.Background(), u)
	if err != nil {
		t.Fatalf("Handle(%+v): %v", u, err)
	}
	return rs
}

func firstText(rs []Reply) string {
	if len(rs) == 0 {
		return ""
	}
	return rs[0].Text
}

// Регрессия: незнакомого человека нет в базе, и raspd отдаёт его пустой
// записью — без platform и ext_id. Раньше эти поля не подставлялись, и первое
// же сохранение получало 400, из-за чего онбординг обрывался на выборе группы
// с сообщением «сервис недоступен» — и поиском, и через обзор институтов.
func TestOnboardingNewUserPicksGroup(t *testing.T) {
	tests := []struct {
		name string
		pick Update
	}{
		{"поиск по названию", Update{Platform: "tg", UserID: "1", Text: "гр12"}},
		{"обзор по институтам", Update{Platform: "tg", UserID: "2", Callback: "g:232"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := testBot(t)
			got := firstText(handle(t, b, tt.pick))
			if strings.Contains(got, "недоступен") {
				t.Fatalf("онбординг оборвался: %q", got)
			}
			// У группы есть подгруппы — значит, следующий шаг их выбор.
			if !strings.Contains(got, "подгруппы") {
				t.Fatalf("ожидался выбор подгруппы, получено: %q", got)
			}

			// Группа обязана сохраниться, иначе следующее сообщение снова
			// начнёт настройку с нуля.
			day := firstText(handle(t, b, Update{Platform: tt.pick.Platform, UserID: tt.pick.UserID, Text: MenuToday}))
			if strings.Contains(day, "Напиши название") {
				t.Errorf("группа не сохранилась, бот снова просит настроиться: %q", day)
			}
			if !strings.Contains(day, "ГР-12") {
				t.Errorf("расписание не для выбранной группы: %q", day)
			}
		})
	}
}

// Полный путь новичка: обзор институтов → курс → группа → подгруппа.
func TestOnboardingBrowseFlow(t *testing.T) {
	b := testBot(t)
	u := func(cb, text string) Update {
		return Update{Platform: "tg", UserID: "42", Callback: cb, Text: text}
	}

	if got := firstText(handle(t, b, u("", "/start"))); !strings.Contains(got, "Напиши название") {
		t.Fatalf("приветствие: %q", got)
	}
	if got := firstText(handle(t, b, u("br", ""))); !strings.Contains(got, "институт") {
		t.Fatalf("список институтов: %q", got)
	}
	if got := firstText(handle(t, b, u("br:10", ""))); !strings.Contains(got, "курс") {
		t.Fatalf("список курсов: %q", got)
	}
	if got := firstText(handle(t, b, u("br:10:1", ""))); !strings.Contains(got, "группу") {
		t.Fatalf("список групп: %q", got)
	}
	if got := firstText(handle(t, b, u("g:232", ""))); !strings.Contains(got, "подгруппы") {
		t.Fatalf("выбор подгруппы: %q", got)
	}
	got := firstText(handle(t, b, u("s:335", "")))
	if strings.Contains(got, "недоступен") {
		t.Fatalf("выбор подгруппы сорвался: %q", got)
	}
	if !strings.Contains(got, "ГР-12/2") {
		t.Errorf("подгруппа не подтверждена: %q", got)
	}
}

// Кнопка «выбрать из списка институтов» должна вести к списку с первого раза.
func TestBrowseButtonRespondsForUnknownUser(t *testing.T) {
	b := testBot(t)
	rs := handle(t, b, Update{Platform: "tg", UserID: "777", Callback: "br"})
	if len(rs) == 0 {
		t.Fatal("на кнопку не пришло ответа")
	}
	if rs[0].Keyboard == nil || len(rs[0].Keyboard.Rows) == 0 {
		t.Fatalf("список институтов пуст: %+v", rs[0])
	}
	if !strings.HasPrefix(rs[0].Keyboard.Rows[0][0].Data, "br:") {
		t.Errorf("кнопка института ведёт в %q", rs[0].Keyboard.Rows[0][0].Data)
	}
}

// Смена группы должна сбрасывать подгруппу: её id принадлежит прежней группе,
// и старый фильтр прятал бы у новой группы всё подряд.
func TestChangingGroupResetsSubgroup(t *testing.T) {
	b := testBot(t)
	u := Update{Platform: "tg", UserID: "5"}
	press := func(data string) []Reply {
		return handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Callback: data})
	}

	press("g:232")
	press("s:335")

	// Переезжаем в группу без подгрупп — теперь это осознанное действие
	// кнопкой «сделать моей», а не побочный эффект просмотра.
	press("ad:39")

	user, err := b.loadUser(context.Background(), u.Platform, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.User.GroupID != 39 {
		t.Errorf("группа = %d, ожидалась 39", user.User.GroupID)
	}
	if user.User.SubgroupID != 0 {
		t.Errorf("подгруппа = %d, ожидался сброс в 0", user.User.SubgroupID)
	}
}

// Просмотр чужой группы не должен трогать привязку. Раньше трогал молча —
// при том что /help прямо приглашал «напиши название другой группы».
func TestViewingOtherGroupKeepsOwn(t *testing.T) {
	b := testBot(t)
	u := Update{Platform: "tg", UserID: "8"}
	press := func(data string) []Reply {
		return handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Callback: data})
	}

	press("g:39")
	press("s:61")

	// Заходим в чужую группу и листаем её стрелками.
	view := press("g:232")
	if len(view) == 0 {
		t.Fatal("чужая группа не открылась")
	}
	if !strings.Contains(view[0].Text, "Чужая группа") {
		t.Errorf("нет пометки о чужом расписании:\n%s", view[0].Text)
	}
	if !strings.Contains(view[0].Text, "ГР-12") {
		t.Errorf("показано не то расписание:\n%s", view[0].Text)
	}
	if !hasButton(view, cb(cbAdopt, "232")) {
		t.Errorf("нет кнопки «сделать моей»: %+v", view[0].Keyboard)
	}

	// Навигация внутри гостевого режима из него не выбрасывает.
	next := press(cb(cbDay, "2025-09-22", "232"))
	if !strings.Contains(firstText(next), "Чужая группа") {
		t.Errorf("стрелка вернула в свою группу:\n%s", firstText(next))
	}

	user, err := b.loadUser(context.Background(), u.Platform, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.User.GroupID != 39 || user.User.SubgroupID != 61 {
		t.Errorf("привязка изменилась просмотром: группа=%d подгруппа=%d",
			user.User.GroupID, user.User.SubgroupID)
	}

	// А вот кнопка «сделать моей» переносит привязку.
	press(cb(cbAdopt, "232"))
	user, err = b.loadUser(context.Background(), u.Platform, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.User.GroupID != 232 {
		t.Errorf("группа = %d, кнопка «сделать моей» не сработала", user.User.GroupID)
	}
}

// Новичку выбор группы и есть настройка: гостевой режим ему только мешал бы.
func TestNewUserPickBindsImmediately(t *testing.T) {
	b := testBot(t)
	u := Update{Platform: "tg", UserID: "9"}

	got := firstText(handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Callback: "g:39"}))
	if strings.Contains(got, "Чужая группа") {
		t.Errorf("новичка отправили в гостевой режим:\n%s", got)
	}

	user, err := b.loadUser(context.Background(), u.Platform, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.User.GroupID != 39 {
		t.Errorf("группа = %d, онбординг не сохранил выбор", user.User.GroupID)
	}
}

// Незнакомый пользователь получает часовой пояс по умолчанию, а не нулевой:
// иначе «сейчас» считалось бы по UTC.
func TestNewUserGetsDefaultTimezone(t *testing.T) {
	b := testBot(t)
	handle(t, b, Update{Platform: "tg", UserID: "9", Callback: "g:39"})

	user, err := b.loadUser(context.Background(), "tg", "9")
	if err != nil {
		t.Fatal(err)
	}
	if user.User.TZOffset != 180 {
		t.Errorf("часовой пояс = %d, ожидалось 180", user.User.TZOffset)
	}
}

// Пока каталог групп качается, обзор не должен показывать экран без единой
// кнопки: снаружи это неотличимо от зависшего бота.
func TestBrowseWithEmptyCatalogue(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "api.sock")
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathUserGet, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, api.UserResponse{Known: false})
	})
	mux.HandleFunc(api.PathDepartments, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, api.DepartmentsResponse{})
	})
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	b := New(api.NewClient(sock), 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rs := handle(t, b, Update{Platform: "tg", UserID: "1", Callback: "br"})

	if len(rs) == 0 || rs[0].Text == "" {
		t.Fatal("на пустой каталог не пришло ответа")
	}
	if !strings.Contains(rs[0].Text, "загружается") {
		t.Errorf("ожидалось объяснение вместо пустого списка: %q", rs[0].Text)
	}
	if rs[0].Keyboard == nil || len(rs[0].Keyboard.Rows) == 0 {
		t.Error("нужна кнопка «попробовать снова», иначе из экрана некуда деться")
	}
}

// Автоматика выбирает копию по занятиям и на стыке учебных годов может
// промахнуться — тогда человек чинит это сам, не теряя подгруппу: у копии
// свои id подгрупп, но те же названия.
func TestSwitchGroupTwinKeepsSubgroup(t *testing.T) {
	b := testBot(t)
	u := Update{Platform: "tg", UserID: "77"}
	pressed := func(data string) []Reply {
		return handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Callback: data})
	}

	pressed("g:39")
	pressed("s:61")

	// В настройках должна быть строка про копию — иначе до переключения не
	// добраться.
	settings := handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Text: MenuSettings})
	if !hasButton(settings, cbTwinMen) {
		t.Fatalf("в настройках нет кнопки копии: %+v", settings[0].Keyboard)
	}

	list := pressed(cbTwinMen)
	if !hasButton(list, "tp:545") || !hasButton(list, "tp:39") {
		t.Fatalf("в списке копий не обе записи: %+v", list[0].Keyboard)
	}

	got := firstText(pressed("tp:545"))
	if strings.Contains(got, "недоступен") {
		t.Fatalf("переключение сорвалось: %q", got)
	}
	user, err := b.loadUser(context.Background(), u.Platform, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.User.GroupID != 545 {
		t.Errorf("группа = %d, ожидалась копия 545", user.User.GroupID)
	}
	if user.User.SubgroupID != 707 {
		t.Errorf("подгруппа = %d, ожидалась 707 — «ГР-22/1» у новой копии", user.User.SubgroupID)
	}
}

// Кнопка копии не должна мозолить глаза тем, у кого копии нет: у большинства
// групп каталога двойника не бывает.
func TestSettingsHideTwinButtonWithoutTwins(t *testing.T) {
	b := testBot(t)
	handle(t, b, Update{Platform: "tg", UserID: "78", Callback: "g:232"})

	settings := handle(t, b, Update{Platform: "tg", UserID: "78", Text: MenuSettings})
	if hasButton(settings, cbTwinMen) {
		t.Errorf("у группы без копий предложено переключение: %+v", settings[0].Keyboard)
	}
}

// Кнопка приезжает из сообщения, которое могло пролежать в чате неделю.
// Привязку разрешено переставлять только на одноимённую запись.
func TestSwitchTwinRejectsForeignGroup(t *testing.T) {
	b := testBot(t)
	handle(t, b, Update{Platform: "tg", UserID: "79", Callback: "g:39"})
	handle(t, b, Update{Platform: "tg", UserID: "79", Callback: "tp:232"})

	user, err := b.loadUser(context.Background(), "tg", "79")
	if err != nil {
		t.Fatal(err)
	}
	if user.User.GroupID != 39 {
		t.Errorf("группа = %d, ожидалось, что чужой id не примут", user.User.GroupID)
	}
}

// hasButton ищет кнопку с такими callback-данными.
// buttonLabels собирает подписи всех кнопок ответа: настройки показывают
// текущие группу и подгруппу именно на них, а не в тексте.
func buttonLabels(rs []Reply) string {
	var b strings.Builder
	for _, r := range rs {
		if r.Keyboard == nil {
			continue
		}
		for _, row := range r.Keyboard.Rows {
			for _, btn := range row {
				b.WriteString(btn.Label + "\n")
			}
		}
	}
	return b.String()
}

func hasButton(rs []Reply, data string) bool {
	for _, r := range rs {
		if r.Keyboard == nil {
			continue
		}
		for _, row := range r.Keyboard.Rows {
			for _, btn := range row {
				if btn.Data == data {
					return true
				}
			}
		}
	}
	return false
}

// The menu and old reply keyboards use one schedule search, starting tomorrow.
func TestTomorrowCostsOneRequest(t *testing.T) {
	for _, label := range []string{MenuTomorrow, "⏭ Завтра", "/tomorrow"} {
		t.Run(label, func(t *testing.T) {
			client, f := newFakeRaspWithState(t)
			b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
			handle(t, b, Update{Platform: "tg", UserID: "5", Callback: "g:39"})
			handle(t, b, Update{Platform: "tg", UserID: "5", Callback: "s:61"})
			f.dayDates = nil
			got := firstText(handle(t, b, Update{Platform: "tg", UserID: "5", Text: label}))
			if !strings.Contains(got, "14 дней начиная с завтра") {
				t.Fatal(got)
			}
			if len(f.dayDates) != 0 || len(f.nextStarts) != 1 || f.nextStarts[0] != "tomorrow" {
				t.Fatalf("day=%v search=%v", f.dayDates, f.nextStarts)
			}
		})
	}
}

// Кнопка подгруппы могла приехать из старого сообщения — например, человек с
// тех пор сменил группу. Записать чужой id нельзя: он не совпадёт ни с одним
// занятием, и фильтр молча спрячет всё расписание.
func TestPickSubgroupRejectsForeign(t *testing.T) {
	b := testBot(t)
	u := Update{Platform: "tg", UserID: "6"}
	press := func(data string) []Reply {
		return handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Callback: data})
	}

	press("g:39")
	press("s:61")
	// 334 — подгруппа совсем другой группы (ГР-12).
	got := firstText(press("s:334"))
	if !strings.Contains(got, "нет") {
		t.Errorf("на чужую подгруппу ожидалось объяснение, получено: %q", got)
	}

	user, err := b.loadUser(context.Background(), u.Platform, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if user.User.SubgroupID != 61 {
		t.Errorf("подгруппа = %d, должна была остаться 61", user.User.SubgroupID)
	}
}

// Отметку о показе меню бот ставит один раз и не ценой чужих настроек:
// раньше она уезжала полной перезаписью пользователя снимком, сделанным до
// работы обработчика, и откатывала только что выбранную группу.
func TestMenuShownOnceAndKeepsSettings(t *testing.T) {
	client, f := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	u := Update{Platform: "tg", UserID: "7"}
	press := func(data string) []Reply {
		return handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Callback: data})
	}

	press("g:39")

	// Первый ответ с меню — тот, что показывает расписание после настройки.
	first := press("s:61")
	if len(first) == 0 || first[0].Menu == nil {
		t.Fatal("настроенному человеку не предложили меню")
	}
	if first[0].MenuSeen {
		t.Error("меню помечено виденным до первого показа")
	}

	retry := handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Text: MenuToday})
	if retry[0].MenuSeen {
		t.Fatal("доставка отмечена до отправки")
	}
	if err := b.MenuDelivered(context.Background(), u.Platform, u.UserID, first[0].Menu); err != nil {
		t.Fatal(err)
	}

	second := handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Text: MenuToday})
	if len(second) == 0 || !second[0].MenuSeen {
		t.Error("повторный показ не помечен виденным — адаптер пришлёт подсказку снова")
	}

	// Перезапуск сохраняет знание о доставке; старая версия требует обновления.
	b = New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	afterRestart := handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Text: MenuToday})
	if !afterRestart[0].MenuSeen {
		t.Fatal("перезапуск сбросил версию")
	}
	old := f.users[key(u.Platform, u.UserID)]
	old.MenuSent = true
	old.MenuVersion = "old-version"
	f.users[key(u.Platform, u.UserID)] = old
	outdated := handle(t, b, Update{Platform: u.Platform, UserID: u.UserID, Text: MenuToday})
	if outdated[0].MenuSeen {
		t.Fatal("старое меню сочли актуальным")
	}

	saved := f.users[key(u.Platform, u.UserID)]
	if saved.GroupID != 39 || saved.SubgroupID != 61 {
		t.Errorf("настройки затёрты отметкой меню: группа=%d подгруппа=%d",
			saved.GroupID, saved.SubgroupID)
	}
}

// Сообщение о правке расписания собирается без единого похода в raspd:
// рассылка на всю группу не должна упираться в запрос на каждого адресата.
func TestChangeMessage(t *testing.T) {
	b := testBot(t)
	text, kb := b.ChangeMessage(
		store.User{Platform: "tg", ExtID: "1", GroupID: 39, TZOffset: 180},
		store.ChangePayload{GroupID: 39, Year: 2025, Month: 9},
	)
	if !strings.Contains(text, "сентябрь") {
		t.Errorf("в тексте нет месяца: %q", text)
	}
	if kb == nil || len(kb.Rows) == 0 || len(kb.Rows[0]) != 2 {
		t.Fatalf("ожидались кнопки «сегодня» и «неделя»: %+v", kb)
	}
	if !strings.HasPrefix(kb.Rows[0][0].Data, cbDay+":") {
		t.Errorf("кнопка дня ведёт не туда: %q", kb.Rows[0][0].Data)
	}
}

// Обрезанный список надо назвать обрезанным. Молча показанные восемь из
// двадцати читаются как «моей группы бот не знает».
func TestSearchSaysWhenTruncated(t *testing.T) {
	client, f := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for i := range 12 {
		id := int64(900 + i)
		f.groups[id] = store.Group{
			ID: id, Name: fmt.Sprintf("Т-ЕСТ-1%d", i), DepartmentID: 10, Course: 1, Active: true,
		}
	}

	rs := handle(t, b, Update{Platform: "tg", UserID: "10", Text: "тест"})
	if len(rs) == 0 {
		t.Fatal("поиск не ответил")
	}
	if !strings.Contains(rs[0].Text, "первые") {
		t.Errorf("об обрезке списка не сказано:\n%s", rs[0].Text)
	}
	// В клавиатуре ровно searchLimit групп плюс строка «выбрать из списка».
	if got := len(rs[0].Keyboard.Rows); got != searchLimit+1 {
		t.Errorf("строк в клавиатуре %d, ожидалось %d", got, searchLimit+1)
	}
}

// Эхо пользовательского ввода не должно превращать сообщение об ошибке в
// стену текста: в поиск прилетает и вставленный абзац.
func TestShortenEchoesQuerySafely(t *testing.T) {
	b := testBot(t)
	long := strings.Repeat("абвгд ", 40)

	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "11", Text: long}))
	if !strings.Contains(got, "…") {
		t.Errorf("длинный запрос не обрезан:\n%s", got)
	}
	if len([]rune(got)) > 400 {
		t.Errorf("сообщение раздулось до %d символов", len([]rune(got)))
	}
}

// Курс, который не помещается на один экран, должен листаться, а не терять
// хвост: во ВКонтакте нижняя панель держит сорок кнопок, и сорок вторая группа
// первого курса раньше просто пропадала.
func TestBrowseGroupsPaginates(t *testing.T) {
	client, f := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Ровно тот случай из базы вуза, ради которого всё и затевалось.
	const total = 42
	for i := range total {
		id := int64(1000 + i)
		f.groups[id] = store.Group{
			ID: id, Name: fmt.Sprintf("П-АГ-%02d", i), DepartmentID: 10, Course: 3, Active: true,
		}
	}

	press := func(data string) Reply {
		rs := handle(t, b, Update{Platform: "tg", UserID: "70", Callback: data})
		if len(rs) == 0 {
			t.Fatalf("на %q не пришло ответа", data)
		}
		return rs[0]
	}

	first := press("br:10:3")
	if !strings.Contains(first.Text, "страница 1 из 3") {
		t.Errorf("на первой странице не сказано, сколько всего:\n%s", first.Text)
	}

	// Обходим страницы кнопкой «вперёд» и собираем всё, что удалось выбрать.
	seen := map[string]bool{}
	r := first
	for range 3 {
		var forward string
		for _, row := range r.Keyboard.Rows {
			for _, btn := range row {
				if strings.HasPrefix(btn.Data, cbGroupPick+":") {
					seen[btn.Data] = true
				}
				if strings.Contains(btn.Label, "▶️") {
					forward = btn.Data
				}
			}
		}
		if forward == "" {
			t.Fatal("на странице нет кнопки «вперёд»")
		}
		r = press(forward)
	}

	if len(seen) != total {
		t.Errorf("по страницам доступно %d групп из %d", len(seen), total)
	}
	// Круг замкнулся: с последней страницы «вперёд» вернуло на первую.
	if !strings.Contains(r.Text, "страница 1 из 3") {
		t.Errorf("листание не закольцевалось:\n%s", r.Text)
	}
}

// Короткому списку страницы не нужны — ни строки листания, ни номера в тексте.
func TestBrowseGroupsWithoutPages(t *testing.T) {
	b := testBot(t)
	r := handle(t, b, Update{Platform: "tg", UserID: "71", Callback: "br:10:1"})[0]

	if strings.Contains(r.Text, "страница") {
		t.Errorf("номер страницы у списка из двух групп:\n%s", r.Text)
	}
	for _, row := range r.Keyboard.Rows {
		for _, btn := range row {
			if strings.Contains(btn.Label, "▶️") {
				t.Errorf("кнопка листания у списка, который влезает целиком: %+v", btn)
			}
		}
	}
}

// Кнопка со страницей живёт в переписке сколько угодно, а каталог за это время
// пересобирается. Уехавшая за конец страница должна вернуть к началу списка, а
// не показать пустой экран.
func TestBrowsePageBeyondEnd(t *testing.T) {
	b := testBot(t)
	r := handle(t, b, Update{Platform: "tg", UserID: "72", Callback: "br:10:1:p9"})[0]

	if r.Keyboard == nil || len(r.Keyboard.Rows) == 0 {
		t.Fatalf("страница за концом списка пуста: %+v", r)
	}
	if !strings.HasPrefix(r.Keyboard.Rows[0][0].Data, cbGroupPick+":") {
		t.Errorf("первая кнопка ведёт в %q, ожидался выбор группы", r.Keyboard.Rows[0][0].Data)
	}
}

// Кнопки прошлой версии бота метки страницы не несут — и обязаны продолжать
// работать: они лежат в переписке у всех, кто когда-либо открывал обзор.
func TestSplitPageKeepsOldButtons(t *testing.T) {
	tests := []struct {
		data     string
		wantArgs []string
		wantPage int
	}{
		{data: "", wantArgs: []string{}},
		{data: "10", wantArgs: []string{"10"}},
		{data: "10:1", wantArgs: []string{"10", "1"}},
		{data: "p2", wantArgs: []string{}, wantPage: 2},
		{data: "10:1:p2", wantArgs: []string{"10", "1"}, wantPage: 2},
		// «p» без числа — не метка, а мусор; лучше первая страница, чем паника.
		{data: "10:1:p", wantArgs: []string{"10", "1", "p"}},
	}

	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			_, args := parseCB(cbBrowse + ":" + tt.data)
			if tt.data == "" {
				_, args = parseCB(cbBrowse)
			}
			args, page := splitPage(args)
			if page != tt.wantPage || !slices.Equal(args, tt.wantArgs) {
				t.Errorf("splitPage(%q) = %v, %d; ожидалось %v, %d",
					tt.data, args, page, tt.wantArgs, tt.wantPage)
			}
		})
	}
}

// ── уведомления ─────────────────────────────────────────────────────────────

// setUp доводит человека до настроенного состояния и возвращает нажималку.
func notifyBot(t *testing.T, id string) (*Bot, *fakeRasp, func(data string) []Reply) {
	t.Helper()
	client, f := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	press := func(data string) []Reply {
		return handle(t, b, Update{Platform: "tg", UserID: id, Callback: data})
	}
	press("g:39")
	press("s:61")
	return b, f, press
}

// Главный тумблер выключает всё разом, а включает вместе с утренним
// сообщением: вкладка, где всё включено и ничего не приходит, — ловушка.
func TestNotifyMasterSwitch(t *testing.T) {
	_, f, press := notifyBot(t, "1")
	saved := func() store.User { return f.users[key("tg", "1")] }

	if u := saved(); !u.Notify || !u.Morning || u.MorningAt != store.DefaultMorningAt {
		t.Fatalf("новичок не получил утреннюю рассылку по умолчанию: %+v", u)
	}
	if u := saved(); u.Evening || u.EmptyDays {
		t.Errorf("вечер и пустые дни должны быть выключены по умолчанию: %+v", u)
	}

	press(cbNotifyAll)
	if saved().Notify {
		t.Fatal("главный тумблер не выключился")
	}
	// Выключенная вкладка не показывает настроек того, что всё равно не придёт.
	rs := press(cbNotifyMen)
	if len(rs) == 0 || rs[0].Keyboard == nil || len(rs[0].Keyboard.Rows) != 2 {
		t.Fatalf("вкладка при выключенных уведомлениях: %+v", rs[0].Keyboard)
	}

	press(cbNotifyAll)
	if u := saved(); !u.Notify || !u.Morning {
		t.Errorf("включение не подняло утреннее сообщение: %+v", u)
	}
}

// Адаптеры обрабатывают апдейты параллельно. Действия одного пользователя
// должны сериализоваться, иначе оба обработчика читают один снимок и
// последнее сохранение стирает изменение первого.
func TestConcurrentNotifySwitchesDoNotOverwriteEachOther(t *testing.T) {
	b, f, _ := notifyBot(t, "parallel")
	const toggles = 21

	start := make(chan struct{})
	errCh := make(chan error, toggles*2)
	var wg sync.WaitGroup
	for _, callback := range []string{cbNotifyMor, cbNotifyEve} {
		for range toggles {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := b.Handle(context.Background(), Update{
					Platform: "tg", UserID: "parallel", Callback: callback,
				})
				errCh <- err
			}()
		}
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("параллельное переключение: %v", err)
		}
	}

	u := f.users[key("tg", "parallel")]
	if u.Morning {
		t.Error("нечётное число переключений не выключило утреннюю рассылку")
	}
	if !u.Evening {
		t.Error("нечётное число переключений не включило вечернюю рассылку")
	}
}

// Время рассылки человек пишет текстом — значит, бот обязан его ждать и
// понимать в том виде, в каком оно написано.
func TestNotifyTimeFromText(t *testing.T) {
	b, f, press := notifyBot(t, "2")
	saved := func() store.User { return f.users[key("tg", "2")] }
	write := func(text string) []Reply {
		return handle(t, b, Update{Platform: "tg", UserID: "2", Text: text})
	}

	press(cb(cbNotifyAsk, askMorning))
	if saved().Await != store.AwaitMorningTime {
		t.Fatalf("бот не ждёт времени: %q", saved().Await)
	}

	if got := firstText(write("7.15")); !strings.Contains(got, "07:15") {
		t.Errorf("время не подтверждено: %q", got)
	}
	if u := saved(); u.MorningAt != 7*60+15 || u.Await != store.AwaitNothing {
		t.Errorf("после ввода времени: %+v", u)
	}

	// За границей диапазона время не принимается, но ожидание остаётся:
	// человек хотел настроить рассылку, а не передумал.
	press(cb(cbNotifyAsk, askMorning))
	if got := firstText(write("9:00")); !strings.Contains(got, "08:30") {
		t.Errorf("границу диапазона не назвали: %q", got)
	}
	if u := saved(); u.MorningAt != 7*60+15 {
		t.Errorf("время за границей диапазона всё-таки записалось: %+v", u)
	}
	if saved().Await != store.AwaitMorningTime {
		t.Error("после промаха бот перестал ждать время")
	}
	if got := firstText(write("абракадабра")); !strings.Contains(got, "Не понял время") {
		t.Errorf("мусор вместо времени: %q", got)
	}

	// Вечерняя рассылка живёт в своём диапазоне и включается выбором времени.
	press(cb(cbNotifyAsk, askEvening))
	write("19:40")
	if u := saved(); !u.Evening || u.EveningAt != 19*60+40 {
		t.Errorf("вечернее время не сохранилось: %+v", u)
	}
}

// Из ожидания времени надо уметь выйти, не написав времени: иначе человек,
// случайно нажавший «⏰», теряет поиск группы до перезапуска бота.
func TestAwaitReleasedByCommand(t *testing.T) {
	b, f, press := notifyBot(t, "3")
	press(cb(cbNotifyAsk, askMorning))

	if got := firstText(handle(t, b, Update{Platform: "tg", UserID: "3", Text: MenuToday})); got == "" {
		t.Fatal("кнопка меню не сработала во время ожидания")
	}
	if u := f.users[key("tg", "3")]; u.Await != store.AwaitNothing {
		t.Fatalf("ожидание не снялось командой: %q", u.Await)
	}
	// Обычный текст снова ищет группу, а не разбирается в часах.
	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "3", Text: "гр12"}))
	if strings.Contains(got, "Не понял время") {
		t.Errorf("поиск группы уехал в разбор времени: %q", got)
	}
}

// Кнопки прошлой версии живут в переписке сколько угодно.
func TestLegacyNotifyButton(t *testing.T) {
	_, f, press := notifyBot(t, "4")

	press(cb(cbNotifySet, "480"))
	if u := f.users[key("tg", "4")]; u.MorningAt != 8*60 || !u.Morning {
		t.Errorf("старая кнопка времени не сработала: %+v", u)
	}
	press(cb(cbNotifySet, "off"))
	if u := f.users[key("tg", "4")]; u.Morning {
		t.Error("старая кнопка «выключить» не выключила утреннее сообщение")
	}
}

// Пустой день при выключенных уведомлениях о нём — молчание, но ровно один
// раз объяснённое. Иначе первое же тихое утро человек читает как поломку.
func TestEmptyDayHintOnlyOnce(t *testing.T) {
	client, f := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handle(t, b, Update{Platform: "tg", UserID: "5", Callback: "g:39"})

	sender := &fakeSender{}
	n := NewNotifier(b, client, sender, 1000, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	if err := n.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 1 || !strings.Contains(sender.msgs[0], "занятий нет") {
		t.Fatalf("разовая подсказка не пришла: %+v", sender.msgs)
	}
	if !f.users[key("tg", "5")].EmptyHinted {
		t.Fatal("подсказка не отмечена — придёт снова")
	}

	// Тот же день: отметка об отправке не даёт написать второй раз.
	if err := n.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 1 {
		t.Errorf("повтор в тот же день: %+v", sender.msgs)
	}

	// Следующий день — и снова пустой. Больше бот не пишет.
	u := f.users[key("tg", "5")]
	u.NotifiedOn = ""
	f.users[key("tg", "5")] = u
	if err := n.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 1 {
		t.Errorf("подсказка пришла второй раз: %+v", sender.msgs)
	}

	// Включил уведомления о пустых днях — расписание приходит и в них.
	u = f.users[key("tg", "5")]
	u.EmptyDays, u.NotifiedOn = true, ""
	f.users[key("tg", "5")] = u
	if err := n.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 2 || !strings.Contains(sender.msgs[1], "Доброе утро") {
		t.Errorf("в пустой день не пришло расписание: %+v", sender.msgs)
	}
}

// Вечернее сообщение спрашивает следующий учебный день, а не сегодняшний.
func TestEveningDigestAsksNextDay(t *testing.T) {
	client, f := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))

	d, err := b.Digest(context.Background(), store.User{GroupID: 39, TZOffset: 180}, store.NotifyEvening)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.dayDates) == 0 || f.dayDates[len(f.dayDates)-1] != api.DateNext {
		t.Errorf("вечером спрошен день %v, ожидался %q", f.dayDates, api.DateNext)
	}
	if !strings.Contains(d.Text, "завтра") {
		t.Errorf("вечернее сообщение не про завтра: %q", d.Text)
	}
	if !d.Empty {
		t.Error("день без пар должен считаться пустым")
	}
}

// fakeSender запоминает отправленное вместо похода в платформу.
type fakeSender struct{ msgs []string }

func (s *fakeSender) Send(_ context.Context, _, text string, _ *Keyboard) error {
	s.msgs = append(s.msgs, text)
	return nil
}
func (s *fakeSender) Platform() string                      { return "tg" }
func (s *fakeSender) Retryable(error) (time.Duration, bool) { return 0, false }
func (s *fakeSender) Undeliverable(error) bool              { return false }

func TestParseClock(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"7:15", 7*60 + 15, true},
		{"07:15", 7*60 + 15, true},
		{"7.15", 7*60 + 15, true},
		{"7 15", 7*60 + 15, true},
		{"715", 7*60 + 15, true},
		{"0715", 7*60 + 15, true},
		{"7", 7 * 60, true},
		{"23:59", 23*60 + 59, true},
		{"  18:30  ", 18*60 + 30, true},
		{"24:00", 0, false},
		{"7:60", 0, false},
		{"утром", 0, false},
		{"", 0, false},
		{"7:1:5", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseClock(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("parseClock(%q) = %d, %v; ожидалось %d, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// Новость о правке должна называть задетые дни и вести к сравнению «до/после».
func TestChangeMessageListsDays(t *testing.T) {
	b := testBot(t)
	u := store.User{Platform: "tg", ExtID: "1", GroupID: 39, TZOffset: 180}

	text, kb := b.ChangeMessage(u, store.ChangePayload{
		GroupID: 39, Year: 2025, Month: 9, Days: []int{18, 19},
	})
	for _, want := range []string{"сентябрь", "чт 18", "пт 19"} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте нет %q: %q", want, text)
		}
	}
	if kb == nil || len(kb.Rows) != 3 || kb.Rows[0][0].Data != cb(cbChanges, "39") {
		t.Fatalf("первой кнопкой ожидалось «подробнее»: %+v", kb)
	}
	// Отписка — последней строкой: рассылка приходит без спроса, и выключать
	// её человек должен там же, где её читает.
	if last := kb.Rows[len(kb.Rows)-1][0]; last.Data != cb(cbChangesSet, changesOff) {
		t.Errorf("в новости нет кнопки отписки: %+v", kb.Rows)
	}

	// Один день — незачем гонять человека через список: кнопка ведёт сразу
	// в сравнение.
	_, kb = b.ChangeMessage(u, store.ChangePayload{GroupID: 39, Year: 2025, Month: 9, Days: []int{18}})
	if kb.Rows[0][0].Data != cb(cbChangeDay, "39", "2025-09-18") {
		t.Errorf("кнопка одного дня ведёт не туда: %q", kb.Rows[0][0].Data)
	}
}

// Кнопка из новости выключает только правки, не трогая утреннюю рассылку, и
// не включает их обратно вторым нажатием.
func TestChangesOffButton(t *testing.T) {
	_, f, press := notifyBot(t, "1")
	saved := func() store.User { return f.users[key("tg", "1")] }

	if u := saved(); !u.Changes {
		t.Fatalf("правки должны быть включены по умолчанию: %+v", u)
	}

	rs := press(cb(cbChangesSet, changesOff))
	if saved().Changes {
		t.Fatal("кнопка не выключила новости о правках")
	}
	if u := saved(); !u.Notify || !u.Morning {
		t.Errorf("отписка от правок задела остальные рассылки: %+v", u)
	}
	// Новость с кнопкой «что изменилось» должна остаться в переписке, поэтому
	// подтверждение приходит отдельным сообщением.
	if len(rs) == 0 || rs[0].Edit {
		t.Errorf("подтверждение затирает саму новость: %+v", rs)
	}
	if !strings.Contains(firstText(rs), "не пишу о правках") {
		t.Errorf("нет подтверждения отписки: %q", firstText(rs))
	}

	// Кнопка из подтверждения возвращает рассылку — и тоже не переключает:
	// оба сообщения живут в переписке сколько угодно.
	press(cb(cbChangesSet, changesOn))
	if !saved().Changes {
		t.Fatal("возврат новостей не сработал")
	}
	press(cb(cbChangesSet, changesOn))
	if !saved().Changes {
		t.Error("повторное нажатие «вернуть» снова выключило новости")
	}
	press(cb(cbChangesSet, changesOff))
	press(cb(cbChangesSet, changesOff))
	if saved().Changes {
		t.Error("повторная отписка вернула новости о правках")
	}
}

// Возврат правок поднимает и главный тумблер: за время жизни сообщения
// уведомления могли выключить целиком, а включать рассылку внутри выключенных
// значит обещать то, что не придёт.
func TestChangesBackLiftsMasterSwitch(t *testing.T) {
	_, f, press := notifyBot(t, "2")
	saved := func() store.User { return f.users[key("tg", "2")] }

	press(cb(cbChangesSet, changesOff))
	press(cbNotifyAll)
	if saved().Notify {
		t.Fatal("главный тумблер не выключился")
	}
	press(cb(cbChangesSet, changesOn))
	if u := saved(); !u.Notify || !u.Changes {
		t.Errorf("правки включились в выключенных уведомлениях: %+v", u)
	}
}

// Утреннее расписание несёт кнопку отписки, вечернее — нет: просили выключать
// именно утреннюю рассылку, и подпись кнопки говорит ровно про неё.
func TestMorningDigestHasUnsubscribeButton(t *testing.T) {
	client, _ := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	u := store.User{GroupID: 39, TZOffset: 180}

	d, err := b.Digest(context.Background(), u, store.NotifyMorning)
	if err != nil {
		t.Fatal(err)
	}
	if d.KB == nil || len(d.KB.Rows) == 0 {
		t.Fatal("у утреннего сообщения нет кнопок")
	}
	if last := d.KB.Rows[len(d.KB.Rows)-1][0]; last.Data != cb(cbMorningSet, morningOff) {
		t.Errorf("в утреннем сообщении нет кнопки отписки: %+v", d.KB.Rows)
	}

	d, err = b.Digest(context.Background(), u, store.NotifyEvening)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range d.KB.Rows {
		for _, btn := range row {
			if strings.HasPrefix(btn.Data, cbMorningSet+":") {
				t.Errorf("вечернее сообщение отписывает от утреннего: %+v", d.KB.Rows)
			}
		}
	}
}

// Кнопка из утреннего сообщения выключает только утреннюю рассылку, не трогая
// новости о правках, и не включает её обратно вторым нажатием.
func TestMorningOffButton(t *testing.T) {
	_, f, press := notifyBot(t, "1")
	saved := func() store.User { return f.users[key("tg", "1")] }

	if u := saved(); !u.Morning {
		t.Fatalf("утренняя рассылка должна быть включена по умолчанию: %+v", u)
	}

	rs := press(cb(cbMorningSet, morningOff))
	if saved().Morning {
		t.Fatal("кнопка не выключила утреннюю рассылку")
	}
	if u := saved(); !u.Notify || !u.Changes {
		t.Errorf("отписка от утра задела остальные рассылки: %+v", u)
	}
	// Расписание на сегодня должно остаться в переписке, поэтому подтверждение
	// приходит отдельным сообщением.
	if len(rs) == 0 || rs[0].Edit {
		t.Errorf("подтверждение затирает расписание: %+v", rs)
	}
	if !strings.Contains(firstText(rs), "не пишу по утрам") {
		t.Errorf("нет подтверждения отписки: %q", firstText(rs))
	}

	// Кнопка из подтверждения возвращает рассылку — и тоже не переключает:
	// оба сообщения живут в переписке сколько угодно.
	press(cb(cbMorningSet, morningOn))
	if !saved().Morning {
		t.Fatal("возврат утренней рассылки не сработал")
	}
	press(cb(cbMorningSet, morningOn))
	if !saved().Morning {
		t.Error("повторное нажатие «вернуть» снова выключило рассылку")
	}
	press(cb(cbMorningSet, morningOff))
	press(cb(cbMorningSet, morningOff))
	if saved().Morning {
		t.Error("повторная отписка вернула утреннюю рассылку")
	}
}

// Возврат утренней рассылки поднимает и главный тумблер: за время жизни
// сообщения уведомления могли выключить целиком.
func TestMorningBackLiftsMasterSwitch(t *testing.T) {
	_, f, press := notifyBot(t, "2")
	saved := func() store.User { return f.users[key("tg", "2")] }

	press(cb(cbMorningSet, morningOff))
	press(cbNotifyAll)
	if saved().Notify {
		t.Fatal("главный тумблер не выключился")
	}
	press(cb(cbMorningSet, morningOn))
	if u := saved(); !u.Notify || !u.Morning {
		t.Errorf("утро включилось в выключенных уведомлениях: %+v", u)
	}
}

// Длинный список дней в текст не влезает — лишние прячутся за счётчиком.
func TestChangeDaysPhraseTruncates(t *testing.T) {
	got := changeDaysPhrase(2025, 9, []int{1, 2, 3, 4, 5, 6, 7, 8, 9}, changeDaysInText)
	if !strings.Contains(got, "и ещё 3 дня") {
		t.Errorf("перечень не обрезан: %q", got)
	}
	if got := changeDaysPhrase(2025, 9, []int{1, 2}, changeDaysInText); strings.Contains(got, "ещё") {
		t.Errorf("короткий перечень обрезать не надо: %q", got)
	}
}

// Кнопка «подробнее» открывает список дней, а день — сравнение с пометками
// того, что пропало и что появилось.
func TestChangeDetails(t *testing.T) {
	client, f := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handle(t, b, Update{Platform: "tg", UserID: "1", Callback: "g:39"})

	// Пока raspd ни о каких правках не знает, кнопка честно говорит об этом.
	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cb(cbChanges, "39")}))
	if !strings.Contains(got, "уже не храню") {
		t.Errorf("для пустого списка ожидалось объяснение, получено %q", got)
	}

	f.changed[39] = []string{"2025-09-22", "2025-09-23"}
	rs := handle(t, b, Update{Platform: "tg", UserID: "1", Callback: cb(cbChanges, "39")})
	if !strings.Contains(firstText(rs), "Что изменилось") {
		t.Errorf("список дней: %q", firstText(rs))
	}
	kb := rs[0].Keyboard
	if kb == nil || len(kb.Rows) != 2 || len(kb.Rows[0]) != 2 {
		t.Fatalf("ожидались два дня в строке и строка возврата: %+v", kb)
	}
	if kb.Rows[0][0].Data != cb(cbChangeDay, "39", "2025-09-22") {
		t.Errorf("кнопка дня ведёт не туда: %q", kb.Rows[0][0].Data)
	}

	got = firstText(handle(t, b, Update{Platform: "tg", UserID: "1", Callback: kb.Rows[0][0].Data}))
	if !strings.Contains(got, "Было") || !strings.Contains(got, "Стало") {
		t.Fatalf("в сравнении нет обеих половин: %q", got)
	}
	// Химию отменили: в «было» она помечена как пропавшая, физика осталась.
	if !strings.Contains(got, "➖ 1 · 08:30 - 10:05 · Химия") {
		t.Errorf("пропавшая пара не помечена: %q", got)
	}
	if !strings.Contains(got, "· 2 · 10:25 - 12:00 · Физика") {
		t.Errorf("оставшаяся пара помечена как изменённая: %q", got)
	}
}

// Снимок «до» живёт трое суток. Кнопка из старого сообщения не должна
// выдавать нынешний день за неизменённый.
func TestChangeDayWithoutSnapshot(t *testing.T) {
	client, _ := newFakeRaspWithState(t)
	b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handle(t, b, Update{Platform: "tg", UserID: "1", Callback: "g:39"})

	got := firstText(handle(t, b, Update{
		Platform: "tg", UserID: "1", Callback: cb(cbChangeDay, "39", "2025-09-22"),
	}))
	if strings.Contains(got, "Было") {
		t.Errorf("без снимка сравнивать не с чем: %q", got)
	}
	if !strings.Contains(got, "уже не помню") {
		t.Errorf("ожидалось честное «не помню»: %q", got)
	}
}

func TestUncertainEmptyDayIsNotSilenced(t *testing.T) {
	for _, freshness := range []api.Freshness{{Missing: true}, {Stale: true}} {
		client, f := newFakeRaspWithState(t)
		f.dayFreshness = freshness
		b := New(client, 180, slog.New(slog.NewTextHandler(io.Discard, nil)))
		u := store.User{Platform: "tg", ExtID: "1", GroupID: 232, TZOffset: 180, EmptyHinted: true}
		f.users[key("tg", "1")] = u
		sender := &fakeSender{}
		n := NewNotifier(b, client, sender, 1000, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err := n.notifyOne(context.Background(), newDayCache(b), store.NotifyTarget{User: u, Kind: store.NotifyMorning}); err != nil {
			t.Fatal(err)
		}
		if len(sender.msgs) != 1 {
			t.Fatal("неизвестное расписание принято за пустой день и скрыто")
		}
		if strings.Contains(sender.msgs[0], "Расписание я не прислал") {
			t.Fatal("неверная подсказка о пустом дне")
		}
	}
}

func TestMenuVersionTracksLabelsAndLayout(t *testing.T) {
	original := MainMenu().Version()
	changed := MainMenu()
	changed.Rows[0][1] = "⏭ Завтра"
	if changed.Version() == original {
		t.Fatal("подпись не изменила версию")
	}
	changed = MainMenu()
	changed.Rows[0], changed.Rows[1] = changed.Rows[1], changed.Rows[0]
	if changed.Version() == original {
		t.Fatal("расположение не изменило версию")
	}
	if MainMenu().Version() != original {
		t.Fatal("версия нестабильна")
	}
}

// Ссылка с сайта: t.me/<бот>?start=g232 приходит боту как «/start g232».
// Новичок должен попасть сразу к выбору подгруппы, а у настроенного человека
// чужая ссылка не имеет права переписать его собственную группу.
func TestStartPayloadOpensGroupFromSite(t *testing.T) {
	b := testBot(t)
	got := firstText(handle(t, b, Update{Platform: "tg", UserID: "77", Text: "/start g232"}))
	if strings.Contains(got, "Напиши название") {
		t.Fatalf("группа из ссылки потеряна, бот начал настройку с нуля: %q", got)
	}
	if !strings.Contains(got, "подгруппы") {
		t.Fatalf("ожидался выбор подгруппы: %q", got)
	}
	day := firstText(handle(t, b, Update{Platform: "tg", UserID: "77", Text: MenuToday}))
	if !strings.Contains(day, "ГР-12") {
		t.Errorf("расписание не для группы из ссылки: %q", day)
	}

	// Мусор в хвосте ссылки — обычный /start, а не повод искать группу по тексту.
	fresh := firstText(handle(t, b, Update{Platform: "tg", UserID: "78", Text: "/start мусор"}))
	if !strings.Contains(fresh, "Напиши название") {
		t.Errorf("подделанный хвост ссылки должен вести к обычному приветствию: %q", fresh)
	}
	for _, payload := range []string{"", "g", "g0", "g-1", "gx", "232", "g232x"} {
		if id := startGroup(payload); id != 0 {
			t.Errorf("startGroup(%q) = %d, ожидался 0", payload, id)
		}
	}
	if id := startGroup("g232"); id != 232 {
		t.Errorf("startGroup(\"g232\") = %d", id)
	}
}

// Случай из ВКонтакте: человек дошёл по дереву до выбора подгруппы, не связал
// абзац с кнопками на оторванной нижней панели и написал название подгруппы
// руками. Раньше он получал за это «Не нашёл группу» — притом что группа была
// уже сохранена, а написано было ровно то, о чём его спросили.
func TestSubgroupNameTypedByHand(t *testing.T) {
	b := testBot(t)
	u := func(cb, text string) Update {
		return Update{Platform: "vk", UserID: "555", Callback: cb, Text: text}
	}

	if got := firstText(handle(t, b, u("g:545", ""))); !strings.Contains(got, "ГР-22/2") {
		t.Fatalf("экран подгрупп обязан называть их в тексте, а не только на кнопках: %q", got)
	}

	got := firstText(handle(t, b, u("", "ГР-22/2")))
	if strings.Contains(got, "Не нашёл") {
		t.Fatalf("отказ на верно написанное название подгруппы: %q", got)
	}
	if !strings.Contains(got, "ГР-22/2") {
		t.Errorf("подгруппа не подтверждена: %q", got)
	}

	// Настройка обязана дожить до следующего сообщения: ради неё всё и затевалось.
	if s := buttonLabels(handle(t, b, u("set", ""))); !strings.Contains(s, "ГР-22/2") {
		t.Errorf("подгруппа не сохранилась, настройки показывают: %q", s)
	}
}

// То же название, написанное с нуля незнакомым человеком: группы у него ещё
// нет, а назвал он сразу и группу, и подгруппу.
func TestSubgroupNameFromScratch(t *testing.T) {
	b := testBot(t)
	got := firstText(handle(t, b, Update{Platform: "vk", UserID: "556", Text: "гр22/1"}))
	if strings.Contains(got, "Не нашёл") {
		t.Fatalf("отказ новичку, написавшему название подгруппы: %q", got)
	}
	if !strings.Contains(got, "ГР-22/1") {
		t.Errorf("подгруппа не подтверждена: %q", got)
	}
}

// Подгруппа чужой группы своей привязки не трогает: в гостях фильтр по
// подгруппе не работает вовсе, и записать чужой id значило бы спрятать от
// человека всё его собственное расписание.
func TestSubgroupNameOfForeignGroup(t *testing.T) {
	b := testBot(t)
	u := func(text string) Update { return Update{Platform: "vk", UserID: "557", Text: text} }
	handle(t, b, u("гр12")) // своя группа — ГР-12
	handle(t, b, Update{Platform: "vk", UserID: "557", Callback: "s:334"})

	if got := firstText(handle(t, b, u("ГР-22/2"))); strings.Contains(got, "Не нашёл") {
		t.Fatalf("отказ на название подгруппы чужой группы: %q", got)
	}
	s := buttonLabels(handle(t, b, Update{Platform: "vk", UserID: "557", Callback: "set"}))
	if !strings.Contains(s, "ГР-12/1") {
		t.Errorf("чужая подгруппа перебила собственную настройку: %q", s)
	}
}
