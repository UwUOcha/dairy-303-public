// Package web serves the student application and a deliberately small public
// read API. Admin, users, feedback and bot outbox routes stay on the socket.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/*
var assets embed.FS

// New собирает обработчик сайта. publicURL — внешний адрес («https://…»),
// нужный только для абсолютных ссылок в OG-разметке: без них ссылка на группу,
// кинутая в чат, разворачивается пустотой. Пустое значение означает «взять
// адрес из запроса».
func newPublic(socket string, demo bool, log *slog.Logger, publicURL string) http.Handler {
	files, _ := fs.Sub(assets, "assets")
	static := http.FileServer(http.FS(files))
	index, _ := fs.ReadFile(files, "index.html")
	publicURL = strings.TrimSuffix(publicURL, "/")
	// Сильные теги считаем один раз на старте: содержимое встроено в бинарь и
	// внутри одного процесса не меняется.
	tags := map[string]string{}
	fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if body, err := fs.ReadFile(files, name); err == nil {
			tags["/"+name] = etag(body)
		}
		return nil
	})
	allowed := map[string]bool{
		"/groups/search": true, "/groups/departments": true, "/groups/list": true,
		"/groups/get": true, "/groups/subgroups": true, "/groups/twins": true,
		"/schedule/week": true, "/schedule/day": true, "/schedule/exams": true, "/schedule/upcoming": true, "/teachers/search": true, "/teachers/week": true,
		// Предмет за полугодие: чем он занят, кто ведёт и где проходит. Ручка
		// читающая и привязана к группе — личного в ней нет.
		"/disciplines/get": true,
		// Правки расписания: какие дни вуз трогал и что в них было до правки.
		// Обе ручки только читают и привязаны к группе — личного в них нет,
		// а без них «что изменилось» работает лишь у тех, кто уже заходил
		// с этого устройства.
		"/changes/dates": true, "/changes/day": true, "/changes/history": true,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme = "http"
			p.Out.URL.Host = "rasp"
			p.Out.URL.Path = p.In.URL.Path[len("/api"):]
			p.Out.URL.RawPath = ""
			p.Out.Host = "rasp"
			p.Out.Header.Del("Cookie")
			p.Out.Header.Del("Authorization")
			if strings.HasPrefix(p.In.URL.Path, "/api/bot/") {
				for _, value := range p.In.Header.Values("Authorization") {
					p.Out.Header.Add("Authorization", value)
				}
			}
		},
		Transport: &http.Transport{MaxIdleConns: 16, MaxIdleConnsPerHost: 16, ResponseHeaderTimeout: 15 * time.Second,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
			}},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("сайт: API недоступен", "ошибка", err)
			writeError(w, 503, "Не удалось загрузить расписание. Попробуйте ещё раз.")
		},
		ModifyResponse: func(r *http.Response) error {
			r.Header.Del("Set-Cookie")
			r.Header.Set("Cache-Control", "no-store")
			return nil
		},
	}
	// Bound simultaneous public work, including upstream catch-up. No stateful
	// per-IP map, no trust in arbitrary X-Forwarded-For headers.
	slots := make(chan struct{}, 16)
	shell := newPage(index, demo, newUpstream(socket, !demo, slots))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://telegram.org; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'self' https://web.telegram.org https://*.telegram.org")
		if serveSource(w, r) {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, 405, "Метод не поддерживается")
			return
		}
		if r.URL.Path == "/api/config" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(publicConfig(demo))
			return
		}
		if r.URL.Path == "/health" {
			w.Write([]byte("ok\n"))
			return
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			if !demo && strings.HasPrefix(r.URL.Path, "/api/bot/") {
				// ReverseProxy drops malformed query values during Rewrite; reject
				// them before that cleanup can silently turn a date into "today".
				if _, err := url.ParseQuery(r.URL.RawQuery); err != nil || len(r.URL.RawQuery) > 256 {
					writeError(w, 400, "invalid_query")
					return
				}
				ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
				defer cancel()
				proxy.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if demo || !allowed[r.URL.Path[4:]] {
				writeError(w, 404, "Не найдено")
				return
			}
			if len(r.URL.RawQuery) > 1024 {
				writeError(w, 400, "Слишком длинный запрос")
				return
			}
			q := r.URL.Query()
			// Reject malformed/repeated parameters before touching the private API.
			for key, values := range q {
				if len(values) != 1 {
					writeError(w, 400, "Повтор параметра")
					return
				}
				switch key {
				case "group", "subgroup", "department", "course", "id", "teacher", "limit", "discipline":
					n, err := strconv.ParseInt(values[0], 10, 64)
					if err != nil || n < 0 || n > 1_000_000_000 {
						writeError(w, 400, "Некорректный номер")
						return
					}
				case "monday", "date":
					d, err := time.Parse("2006-01-02", values[0])
					if err != nil || d.Year() < 2020 || d.Year() > time.Now().Year()+2 {
						writeError(w, 400, "Некорректная дата")
						return
					}
				case "q":
					if len(values[0]) > 200 {
						writeError(w, 400, "Слишком длинный поиск")
						return
					}
				default:
					writeError(w, 400, "Неизвестный параметр")
					return
				}
			}
			if q.Has("limit") {
				n, _ := strconv.Atoi(q.Get("limit"))
				if n == 0 || n > 50 {
					q.Set("limit", "50")
				}
			}
			r.URL.RawQuery = q.Encode()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				w.Header().Set("Retry-After", "2")
				writeError(w, 429, "Слишком много запросов. Попробуйте через пару секунд.")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			proxy.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		if len(index) > 0 && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
			body := shell.render(r.Context(), r, origin(r, publicURL))
			if notModified(w, r, etag([]byte(body))) {
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			if r.Method == http.MethodHead {
				return
			}
			w.Write([]byte(body))
			return
		}
		if profileAsset(w, r) {
			return
		}
		if tag := tags[r.URL.Path]; tag != "" && notModified(w, r, tag) {
			return
		}
		static.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// origin возвращает внешний адрес сайта для OG-разметки: настроенный
// WEB_PUBLIC_URL, иначе — адрес из запроса.
//
// Host приходит от клиента, поэтому в разметку он попадает только после
// проверки на символы имени хоста: подставить туда кавычку и дописать свой
// тег не выйдет. Если хост непригоден, отдаём пустую строку — ссылки станут
// относительными, и это честнее выдуманного адреса.
func origin(r *http.Request, configured string) string {
	if configured != "" {
		return configured
	}
	host := r.Host
	if host == "" || len(host) > 253 {
		return ""
	}
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == ':' || c == '[' || c == ']') {
			return ""
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// Сайт всегда стоит за Caddy, и он единственный, кто ставит этот заголовок.
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	}
	return scheme + "://" + host
}
