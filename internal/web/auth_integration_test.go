package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/botcore"
	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBetaBrowserBotSocketRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "beta.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`INSERT INTO web_allowlist VALUES('tg','1',1),('vk','2',1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.SaveGroupTree(ctx, importdata.GroupTree{Departments: []importdata.Department{{ID: 10, Name: "Институт"}}, Groups: []importdata.Group{{ID: 545, DepartmentID: 10, Name: "ГР-22", Active: true}}}); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveSubgroups(ctx, 545, []importdata.Subgroup{{ID: 707, Name: "ГР-22/1"}}); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []struct{ p, id string }{{"tg", "1"}, {"vk", "2"}} {
		if err = db.SaveUser(ctx, store.User{Platform: identity.p, ExtID: identity.id, GroupID: 545, SubgroupID: 707}); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(t.TempDir(), "api.sock")
	ln, err := api.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: api.NewServer(db, nil, time.UTC, time.Hour, slog.Default()).Handler()}
	go server.Serve(ln)
	defer server.Close()
	gate := New(socket, false, slog.Default(), "https://beta.test")
	bot := botcore.New(api.NewClient(socket), 180, slog.Default())
	request := func(path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "https://beta.test/auth/"+path, strings.NewReader(body))
		r.Header.Set("Origin", "https://beta.test")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w
	}
	for _, identity := range []struct{ p, id string }{{"tg", "1"}, {"vk", "2"}} {
		start := request("start", `{"platform":"`+identity.p+`"}`, nil)
		var ch store.WebAuthResponse
		json.Unmarshal(start.Body.Bytes(), &ch)
		if start.Code != 200 || len(ch.Challenge) != 43 {
			t.Fatal(start.Code, start.Body.String())
		}
		browser := start.Result().Cookies()[0]
		reply, err := bot.Handle(ctx, botcore.Update{Platform: identity.p, UserID: identity.id, Private: false, Text: "/login " + ch.Challenge})
		if err != nil || !strings.Contains(reply[0].Text, "личный диалог") {
			t.Fatal(reply, err)
		}
		reply, err = bot.Handle(ctx, botcore.Update{Platform: identity.p, UserID: identity.id, Private: true, Text: "/login " + ch.Challenge})
		if err != nil {
			t.Fatal(err)
		}
		_, tail, ok := strings.Cut(reply[0].Text, "<code>")
		if !ok {
			t.Fatal(reply)
		}
		code, _, _ := strings.Cut(tail, "</code>")
		if len(code) != 8 {
			t.Fatal(reply)
		}
		finishBody := `{"challenge":"` + ch.Challenge + `","code":"` + code + `"}`
		// The public challenge and even a valid code are useless without the initiating cookie.
		if bad := request("finish", finishBody, nil); bad.Code == 200 {
			t.Fatal("unbound finish succeeded")
		}
		done := request("finish", finishBody, browser)
		if done.Code != 200 {
			t.Fatal(done.Code, done.Body.String())
		}
		session := done.Result().Cookies()[0]
		me := request("me", `{}`, session)
		var profile store.WebAuthResponse
		json.Unmarshal(me.Body.Bytes(), &profile)
		if me.Code != 200 || profile.GroupID != 545 || profile.SubgroupID != 707 {
			t.Fatal(me.Body.String())
		}
		if got := request("logout", `{}`, session); got.Code != 200 {
			t.Fatal(got.Body.String())
		}
		if got := request("me", `{}`, session); got.Code != 401 {
			t.Fatal("logged-out session accepted")
		}
	}
}
