package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/config"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

type adminIdentity struct {
	response store.WebAuthResponse
	err      error
}

func (a *adminIdentity) WebAuth(_ context.Context, r store.WebAuthRequest) (store.WebAuthResponse, error) {
	if r.Op != "session" {
		return store.WebAuthResponse{}, errors.New("unexpected operation")
	}
	return a.response, a.err
}
func TestAdminRequiresVerifiedIdentityAndAllowedAddressForEveryRoute(t *testing.T) {
	old := profile.Current()
	p := old
	p.AdminTG = "700"
	p.AdminVK = "800"
	profile.Set(p)
	defer profile.Set(old)
	ips, _ := config.ParsePrefixes("203.0.113.7")
	proxies, _ := config.ParsePrefixes("10.0.0.2")
	identity := &adminIdentity{}
	hits := 0
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Cache-Control", "public,max-age=3600")
		w.Write([]byte("private metrics"))
	})
	h := adminGate(http.NotFoundHandler(), panel, identity, config.Admin{AllowIPs: ips, TrustedProxies: proxies}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cases := []struct {
		name, peer, xff, platform, id string
		session                       bool
		err                           string
		want                          int
	}{
		{"both", "203.0.113.7:1", "", "tg", "700", true, "", 200},
		{"VK admin", "203.0.113.7:1", "", "vk", "800", true, "", 200},
		{"other account", "203.0.113.7:1", "", "tg", "701", true, "", 403},
		{"same ID wrong platform", "203.0.113.7:1", "", "vk", "700", true, "", 403},
		{"wrong address", "203.0.113.8:1", "", "tg", "700", true, "", 403},
		{"spoof header public peer", "203.0.113.8:1", "203.0.113.7", "tg", "700", true, "", 403},
		{"spoof header private peer", "10.0.0.3:1", "203.0.113.7", "tg", "700", true, "", 403},
		{"trusted proxy", "10.0.0.2:1", "203.0.113.7", "tg", "700", true, "", 200},
		{"forwarded chain rejected", "10.0.0.2:1", "203.0.113.7, 203.0.113.8", "tg", "700", true, "", 403},
		{"trusted proxy missing header", "10.0.0.2:1", "", "tg", "700", true, "", 403},
		{"expired session", "203.0.113.7:1", "", "tg", "700", true, "unauthorized", 401},
	}
	for _, path := range []string{"/admin/", "/admin/api/stats", "/admin/api/history", "/admin/static/app.js", "/admin/static/app.css"} {
		for _, tt := range cases {
			t.Run(tt.name+path, func(t *testing.T) {
				identity.response = store.WebAuthResponse{Platform: tt.platform, ExtID: tt.id, Error: tt.err}
				before := hits
				r := httptest.NewRequest("GET", path, nil)
				r.RemoteAddr = tt.peer
				r.Header.Set("X-Forwarded-For", tt.xff)
				r.Header.Set("X-Admin-ID", "700")
				r.Header.Set("If-None-Match", "cached")
				if tt.session {
					r.AddCookie(&http.Cookie{Name: sessionCookie, Value: strings.Repeat("a", 43)})
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tt.want {
					t.Fatalf("got %d want %d: %s", w.Code, tt.want, w.Body.String())
				}
				if tt.want != 200 && hits != before {
					t.Fatal("denied request reached admin handler")
				}
				if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
					t.Fatal("admin response can be cached")
				}
			})
		}
	}
	identity.err = errors.New("offline")
	r := httptest.NewRequest("GET", "/admin/api/stats", nil)
	r.RemoteAddr = "203.0.113.7:1"
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: strings.Repeat("a", 43)})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("unavailable auth must fail closed", w.Code)
	}
	identity.err = nil
	for _, path := range []string{"/admin/", "/admin/api/stats"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "203.0.113.7:1"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 401
		if path == "/admin/" {
			want = 303
		}
		if w.Code != want {
			t.Fatalf("IP without session: %d", w.Code)
		}
	}
}
