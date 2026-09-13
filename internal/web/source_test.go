package web

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceAndLicensesAvailableWithoutSession(t *testing.T) {
	t.Setenv("WEB_SOURCE_CODE_URL", "https://example.org/source/version?x=1&y=2")
	for _, path := range []string{"/source", "/license", "/third-party"} {
		w := httptest.NewRecorder()
		New("/missing", false, slog.Default(), "https://schedule.example.edu").ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s requires session: %d", path, w.Code)
		}
		if path == "/source" && !strings.Contains(w.Body.String(), "https://example.org/source/version?x=1&amp;y=2") {
			t.Fatal("missing source URL or unsafe escaping")
		}
	}
	t.Setenv("WEB_SOURCE_CODE_URL", "javascript:alert(1)")
	if sourceURL() != "" {
		t.Fatal("unsafe source URL accepted")
	}
}
