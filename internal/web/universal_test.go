package web

import (
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndependentProfilePublicWebsiteAndLogin(t *testing.T) {
	previous := profile.Current()
	p := profile.Default()
	p.AppName = "Study <Space>"
	p.WebMark = "<S>"
	p.University = "Another University"
	p.Access = "public"
	p.Timezone = "Europe/Berlin"
	p.TelegramURL = "https://t.me/other_bot"
	p.AdminTG = "do-not-expose"
	profile.Set(p)
	defer profile.Set(previous)
	for _, path := range []string{"/", "/api/config", "/manifest.webmanifest", "/profile.css"} {
		w := httptest.NewRecorder()
		New("/does-not-exist", false, slog.Default(), "https://example.org").ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		body := w.Body.String()
		if w.Code != 200 {
			t.Fatalf("public profile blocked %s: %d", path, w.Code)
		}
		if strings.Contains(body, "КГУ") || strings.Contains(body, "tksu") || strings.Contains(body, "do-not-expose") {
			t.Fatalf("profile leak on %s", path)
		}
		if path == "/" && (!strings.Contains(body, `class="brand-symbol">&lt;S&gt;`) || !strings.Contains(body, "Study &lt;Space&gt;") || !strings.Contains(body, "Europe/Berlin")) {
			t.Fatal("profile missing / unescaped")
		}
	}
	p.Access = "allowlist"
	profile.Set(p)
	w := httptest.NewRecorder()
	New("/missing", false, slog.New(slog.NewTextHandler(io.Discard, nil)), "https://example.org").ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "other_bot") || !strings.Contains(w.Body.String(), "Another University") {
		t.Fatal("login did not use installation configuration")
	}
}
