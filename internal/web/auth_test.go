package web

import (
	"context"
	"encoding/json"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type authFake struct {
	calls    []store.WebAuthRequest
	response store.WebAuthResponse
}

func (f *authFake) WebAuth(_ context.Context, in store.WebAuthRequest) (store.WebAuthResponse, error) {
	f.calls = append(f.calls, in)
	return f.response, nil
}
func TestBetaGateBlocksShellAndEveryAPI(t *testing.T) {
	f := &authFake{}
	hits := 0
	gate := betaGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.Write([]byte("private schedule")) }), f, "https://beta.test")
	for _, path := range []string{"/", "/index.html", "/app.js", "/api/config", "/api/schedule/week?group=545", "/api/users/get", "/web-auth", "/beta-sw.js"} {
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, httptest.NewRequest("GET", "https://beta.test"+path, nil))
		if w.Code != 303 && w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if hits != 0 || len(f.calls) != 0 {
		t.Fatal("unauthenticated work reached upstream")
	}
	for _, path := range []string{"/login", "/sw.js"} {
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, httptest.NewRequest("GET", "https://beta.test"+path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
}
func TestBetaGateCSRFAndNoPrivateRPCPassthrough(t *testing.T) {
	f := &authFake{}
	gate := betaGate(http.NotFoundHandler(), f, "https://beta.test")
	for _, origin := range []string{"", "https://evil.test", "null"} {
		r := httptest.NewRequest("POST", "https://beta.test/auth/start", strings.NewReader(`{"platform":"tg"}`))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	for _, body := range []string{`{"platform":"tg","op":"issue","ext_id":"1"}`, `{"platform":"other"}`} {
		r := httptest.NewRequest("POST", "https://beta.test/auth/start", strings.NewReader(body))
		r.Header.Set("Origin", "https://beta.test")
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("untrusted request reached RPC")
	}
}
func TestBetaCookiesNoBearerInJSONAndCache(t *testing.T) {
	f := &authFake{response: store.WebAuthResponse{Token: store.WebSecret()}}
	gate := betaGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Write([]byte("schedule"))
	}), f, "https://beta.test")
	r := httptest.NewRequest("POST", "https://beta.test/auth/finish", strings.NewReader(`{"challenge":"abc","code":"12345678"}`))
	r.Header.Set("Origin", "https://beta.test")
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if strings.Contains(w.Body.String(), f.response.Token) {
		t.Fatal("bearer exposed to JS")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal(cookies)
	}
	c := cookies[0]
	if c.Name != sessionCookie || !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 60*86400 {
		t.Fatal(c)
	}
	var out map[string]any
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal(w.Body.String())
	}
	f.response = store.WebAuthResponse{}
	r = httptest.NewRequest("GET", "https://beta.test/", nil)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Header())
	}
}
