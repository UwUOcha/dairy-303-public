package web

import (
	"context"
	"encoding/json"
	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"golang.org/x/time/rate"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "__Host-mp_session"
const browserCookie = "__Host-mp_login"

func New(socket string, demo bool, log *slog.Logger, publicURL string) http.Handler {
	next := newPublic(socket, demo, log, publicURL)
	if demo {
		return next
	}
	if publicURL == "" {
		publicURL = profile.Current().PublicURL
	}
	return betaGate(next, api.NewClient(socket), strings.TrimRight(publicURL, "/"))
}

type authClient interface {
	WebAuth(context.Context, store.WebAuthRequest) (store.WebAuthResponse, error)
}

func betaGate(next http.Handler, client authClient, publicURL string) http.Handler {
	global := rate.NewLimiter(80, 160)
	starts := rate.NewLimiter(2, 10)
	botRequests := rate.NewLimiter(2, 20)
	var buckets [1024]*rate.Limiter
	for i := range buckets {
		buckets[i] = rate.NewLimiter(2, 20)
	}
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if serveSource(w, r) {
			return
		}
		if r.URL.Path == "/health" {
			w.Write([]byte("ok\n"))
			return
		}
		if !global.Allow() {
			writeError(w, 429, "Слишком много запросов. Попробуйте чуть позже.")
			return
		}
		if r.URL.Path == "/login" || r.URL.Path == "/account" {
			if r.Method != "GET" && r.Method != "HEAD" {
				writeError(w, 405, "Метод не поддерживается")
				return
			}
			body, _ := assets.ReadFile("assets/login.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Method != "HEAD" {
				w.Write(profileHTML(body, false))
			}
			return
		}
		// Public files contain no schedule or user data. The beta worker removes
		// earlier public/offline shells, including SSR schedule snapshots.
		switch r.URL.Path {
		case "/auth.js", "/auth.css", "/config.mjs", "/profile.css", "/icon.svg", "/app-icon-192.png", "/app-icon-512.png":
			next.ServeHTTP(w, r)
			return
		case "/sw.js":
			body, _ := assets.ReadFile("assets/beta-sw.js")
			w.Header().Set("Content-Type", "text/javascript")
			w.Header().Set("Service-Worker-Allowed", "/")
			w.Write(body)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			writeError(w, 429, "Слишком много запросов")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if strings.HasPrefix(r.URL.Path, "/api/bot/") {
			if !botRequests.Allow() {
				w.Header().Set("Retry-After", "2")
				writeError(w, 429, "Слишком много запросов")
				return
			}
			// raspd checks the dedicated Bearer key; browser cookies grant no access.
			next.ServeHTTP(&privateWriter{w}, r)
			return
		}
		token := ""
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			if r.Method != "POST" {
				writeError(w, 405, "Используйте POST")
				return
			}
			if r.Header.Get("Origin") != publicURL || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, 403, "Недопустимый источник запроса")
				return
			}
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			// Deployment exposes no webd port; Caddy overwrites this single-IP header.
			if v := r.Header.Get("X-Forwarded-For"); net.ParseIP(v) != nil {
				ip = v
			}
			h := fnv.New32a()
			h.Write([]byte(ip))
			if !buckets[h.Sum32()%1024].Allow() {
				w.Header().Set("Retry-After", "10")
				writeError(w, 429, "Подождите немного и повторите.")
				return
			}
			var body struct {
				Platform  string `json:"platform"`
				Challenge string `json:"challenge"`
				Code      string `json:"code"`
				ID        string `json:"id"`
			}
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			dec.DisallowUnknownFields()
			if dec.Decode(&body) != nil {
				writeError(w, 400, "Некорректный запрос")
				return
			}
			in := store.WebAuthRequest{Token: token}
			switch r.URL.Path {
			case "/auth/start":
				if !starts.Allow() {
					writeError(w, 429, "Подождите несколько секунд.")
					return
				}
				if body.Platform != "tg" && body.Platform != "vk" {
					writeError(w, 400, "Выберите мессенджер")
					return
				}
				in.Op = "start"
				in.Platform = body.Platform
				in.Browser = store.WebSecret()
				if c, err := r.Cookie(browserCookie); err == nil && len(c.Value) == 43 {
					in.Browser = c.Value
				}
			case "/auth/finish":
				in.Op = "finish"
				in.Challenge = body.Challenge
				in.Code = body.Code
				in.Label = deviceLabel(r.UserAgent())
				if c, err := r.Cookie(browserCookie); err == nil {
					in.Browser = c.Value
				}
			case "/auth/me":
				in.Op = "session"
			case "/auth/devices":
				in.Op = "devices"
			case "/auth/revoke":
				in.Op = "revoke"
				in.ID = body.ID
			case "/auth/logout":
				in.Op = "logout"
			default:
				writeError(w, 404, "Не найдено")
				return
			}
			out, err := client.WebAuth(ctx, in)
			if err != nil {
				writeError(w, 503, "Вход временно недоступен. Попробуйте ещё раз.")
				return
			}
			if out.Error != "" {
				status := 400
				if out.Error == "unauthorized" {
					status = 401
				}
				writeError(w, status, authError(out.Error))
				return
			}
			if in.Op == "start" {
				setAuthCookie(w, browserCookie, in.Browser, 300)
			}
			if in.Op == "finish" {
				setAuthCookie(w, sessionCookie, out.Token, store.WebSessionDays*86400)
				setAuthCookie(w, browserCookie, "", -1)
				out.Token = ""
			}
			if in.Op == "logout" {
				setAuthCookie(w, sessionCookie, "", -1)
				w.Header().Set("Clear-Site-Data", "\"cache\", \"storage\"")
			}
			w.Header().Set("Content-Type", "application/json")
			out.ExtID = ""
			json.NewEncoder(w).Encode(out)
			return
		}
		if profile.Current().Access == "public" {
			next.ServeHTTP(w, r)
			return
		}
		if len(token) != 43 {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, 401, "Войдите через бота, чтобы продолжить.")
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}
		out, err := client.WebAuth(ctx, store.WebAuthRequest{Op: "session", Token: token})
		if err != nil {
			writeError(w, 503, "Сайт временно недоступен. Попробуйте ещё раз.")
			return
		}
		if out.Error != "" {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, 401, "Войдите через бота, чтобы продолжить.")
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}
		// Authenticated HTML must never return a cached SSR page after revocation.
		r.Header.Del("If-None-Match")
		r.Header.Del("If-Modified-Since")
		next.ServeHTTP(&privateWriter{w}, r)
	})
}

type privateWriter struct{ http.ResponseWriter }

func (w *privateWriter) WriteHeader(code int) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.ResponseWriter.WriteHeader(code)
}
func (w *privateWriter) Write(b []byte) (int, error) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	return w.ResponseWriter.Write(b)
}
func setAuthCookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func deviceLabel(ua string) string {
	os := "Компьютер"
	switch {
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Macintosh"):
		os = "Mac"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	browser := "Браузер"
	switch {
	case strings.Contains(ua, "Edg"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari"):
		browser = "Safari"
	}
	return browser + " · " + os
}
func authError(code string) string {
	switch code {
	case "not_allowed":
		return "Аккаунту пока не разрешён вход в эту установку."
	case "expired":
		return "Время входа истекло. Начните заново."
	case "pending":
		return "Сначала получите код в личном диалоге с ботом."
	case "code":
		return "Код не подошёл. Проверьте 8 цифр из сообщения бота."
	case "device_limit":
		return "Уже подключены 4 устройства. Завершите ненужный сеанс в настройках сайта или отправьте боту /web_logout для выхода везде."
	case "unauthorized":
		return "Войдите через бота, чтобы продолжить."
	default:
		return "Не удалось войти. Попробуйте начать заново."
	}
}
