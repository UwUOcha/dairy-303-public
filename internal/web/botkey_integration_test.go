package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

type botAPISync struct{}

func (botAPISync) EnsureRange(context.Context, int64, string, string) error { return nil }
func (botAPISync) SyncMonth(context.Context, int64, int, int) error         { return nil }
func (botAPISync) LastError() error                                         { return nil }

func TestPersonalBotThroughPublicGateAndSocket(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveGroupTree(ctx, importdata.GroupTree{Groups: []importdata.Group{{ID: 545, Name: "ГР-22", Active: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSubgroups(ctx, 545, []importdata.Subgroup{{ID: 707, Name: "1"}, {ID: 708, Name: "2"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveUser(ctx, store.User{Platform: "tg", ExtID: store.BotKeyOwner(), GroupID: 545, SubgroupID: 707}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMonth(ctx, importdata.MonthSchedule{GroupID: 545, Year: 2026, Month: 9, Lessons: []importdata.Lesson{
		{ID: 1, GroupID: 545, SubgroupID: 707, Date: "2026-09-11", Discipline: "Своя пара"},
		{ID: 2, GroupID: 545, SubgroupID: 708, Date: "2026-09-11", Discipline: "Чужая подгруппа"},
		{ID: 3, GroupID: 545, Date: "2026-09-11", Discipline: "Общая пара"},
	}}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "api.sock")
	ln, err := api.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: api.NewServer(db, botAPISync{}, time.UTC, time.Hour, slog.Default()).Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	client := api.NewClient(socket)
	key, err := client.BotKey(ctx, "create")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, auth string) *httptest.ResponseRecorder {
		t.Helper()
		gate := New(socket, false, slog.Default(), "https://beta.test")
		r := httptest.NewRequest(method, "https://beta.test"+path, strings.NewReader(`{}`))
		r.Header.Set("Origin", "https://beta.test")
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, path, auth string
		want               int
	}{
		{"GET", "/api/bot/me", "", 401},
		{"GET", "/api/bot/me", "Bearer " + store.WebSecret(), 401},
		{"GET", "/api/bot/me", "Bearer " + store.BotKeyPrefix + store.WebSecret(), 401},
		{"GET", "/api/bot/me", "Basic " + key.Token, 401},
		{"GET", "/api/bot/me", "Bearer " + key.Token + ",other", 401},
		{"GET", "/api/bot/me?access_token=" + key.Token, "", 401},
		{"GET", "/api/bot/me", "bEaReR " + key.Token, 200},
		{"GET", "/api/bot/schedule/day?date=2026-09-11", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/schedule/week?monday=2026-09-07", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/schedule/now", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/schedule/next-lesson?from=tomorrow", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/schedule/upcoming", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/schedule/exams", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/changes/dates", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/changes/day?date=2026-09-11", "Bearer " + key.Token, 200},
		{"GET", "/api/bot/schedule/day?group=39", "Bearer " + key.Token, 400},
		{"GET", "/api/bot/schedule/day?subgroup=708", "Bearer " + key.Token, 400},
		{"GET", "/api/bot/me?platform=vk&ext_id=1", "Bearer " + key.Token, 400},
		{"GET", "/api/bot/schedule/day?date=2026-09-11&date=2026-09-12", "Bearer " + key.Token, 400},
		{"GET", "/api/bot/schedule/day?date=%zz", "Bearer " + key.Token, 400},
		{"GET", "/api/bot/schedule/day?date=2026-02-30", "Bearer " + key.Token, 400},
		{"POST", "/api/bot/schedule/day", "Bearer " + key.Token, 405},
		{"GET", "/api/bot/users/get", "Bearer " + key.Token, 404},
		{"POST", "/api/bot/bot-key", "Bearer " + key.Token, 405},
		{"POST", "/auth/bot-key", "Bearer " + key.Token, 404},
		{"GET", "/api/bot-key", "Bearer " + key.Token, 401},
		{"GET", "/api/schedule/week?group=545", "Bearer " + key.Token, 401},
		{"GET", "/", "Bearer " + key.Token, 303},
	} {
		w := request(tc.method, tc.path, tc.auth)
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), key.Token) {
			t.Error("secret leaked in response")
		}
		if strings.HasPrefix(tc.path, "/api/bot/") && !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Error("cacheable bot response")
		}
		if w.Code == 401 && strings.HasPrefix(tc.path, "/api/bot/") && w.Header().Get("WWW-Authenticate") == "" {
			t.Error("missing Bearer challenge")
		}
	}
	w := request("GET", "/api/bot/schedule/day?date=2026-09-11", "Bearer "+key.Token)
	var day api.DayResponse
	if err := json.Unmarshal(w.Body.Bytes(), &day); err != nil {
		t.Fatal(err)
	}
	if day.Group.ID != 545 || day.Subgroup == nil || day.Subgroup.ID != 707 || !strings.Contains(w.Body.String(), "Своя пара") || !strings.Contains(w.Body.String(), "Общая пара") || strings.Contains(w.Body.String(), "Чужая подгруппа") {
		t.Fatal(w.Body.String())
	}
	for _, duplicateHeader := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/api/bot/me", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: key.Token})
		if duplicateHeader {
			r.Header.Add("Authorization", "Bearer "+key.Token)
			r.Header.Add("Authorization", "Bearer "+key.Token)
		}
		w := httptest.NewRecorder()
		New(socket, false, slog.Default(), "https://beta.test").ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("cookie or duplicate Authorization accepted", w.Code)
		}
	}
	// Even a valid browser session cannot expose the private management RPC.
	for _, path := range []string{"/api/bot-key", "/api/web-auth"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: store.WebSecret()})
		w := httptest.NewRecorder()
		betaGate(newPublic(socket, false, slog.Default(), ""), &authFake{}, "https://beta.test").ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatal("private management exposed", path, w.Code)
		}
	}
	rotated, err := client.BotKey(ctx, "rotate")
	if err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/bot/me", "Bearer "+key.Token); w.Code != 401 {
		t.Fatal("old key accepted")
	}
	if w := request("GET", "/api/bot/me", "Bearer "+rotated.Token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := client.BotKey(ctx, "revoke"); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/bot/me", "Bearer "+rotated.Token); w.Code != 401 {
		t.Fatal("revoked key accepted")
	}
}

func TestBotAPIDoesNotUseBrowserSessionAndLimitsRequests(t *testing.T) {
	f := &authFake{}
	hits := 0
	gate := betaGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.Write([]byte("ok")) }), f, "https://beta.test")
	limited := false
	for i := 0; i < 25; i++ {
		r := httptest.NewRequest("GET", "/api/bot/me", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: store.WebSecret()})
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		if w.Code == 429 {
			limited = true
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("no retry delay")
			}
		}
	}
	if !limited || hits == 0 || len(f.calls) != 0 {
		t.Fatal("bot boundary/limiter failed", hits, len(f.calls))
	}
}
