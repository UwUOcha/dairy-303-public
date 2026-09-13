package web

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicBoundary(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case r.URL.Path == "/groups/search" && r.URL.Query().Get("limit") == "50":
		case r.URL.Path == "/disciplines/get":
		default:
			t.Errorf("unexpected upstream request: %s", r.URL)
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("browser credentials reached private API")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"groups":[]}`))
	})}
	go upstream.Serve(ln)
	t.Cleanup(func() { upstream.Shutdown(context.Background()) })
	h := newPublic(socket, false, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/users/get", 404}, {"GET", "/api/outbox/take", 404}, {"GET", "/api/stats", 404},
		{"POST", "/api/groups/search", 405}, {"GET", "/api/groups/search?q=test&limit=500", 200},
		{"GET", "/api/schedule/week?group=1&group=2", 400}, {"GET", "/api/schedule/week?group=-1", 400},
		{"GET", "/api/schedule/week?group=1&monday=2026-02-30", 400}, {"GET", "/api/groups/search?token=bad", 400},
		{"GET", "/api/groups/search?q=" + strings.Repeat("x", 1100), 400},
		{"GET", "/api/disciplines/get?group=39&discipline=2", 200},
		{"GET", "/api/disciplines/get?group=39&discipline=-2", 400},
		{"GET", "/", 200}, {"GET", "/app.js", 200}, {"GET", "/model.mjs", 200}, {"GET", "/manifest.webmanifest", 200},
		{"GET", "/icon-192.png", 200}, {"GET", "/icon-512.png", 200}, {"GET", "/sw.js", 200}, {"GET", "/api/config", 200},
	} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.Header.Set("Cookie", "private=secret")
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Errorf("%s %s: %d != %d: %s", tt.method, tt.path, w.Code, tt.status, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors") {
			t.Error("missing CSP")
		}
		if tt.path == "/model.mjs" && !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
			t.Error("ES module served with wrong MIME type")
		}
	}
	// Два разрешённых запроса: каталог групп и предмет. Всё остальное из
	// таблицы обязано было закончиться, не дойдя до сокета.
	if calls.Load() != 2 {
		t.Fatalf("private upstream called %d times", calls.Load())
	}
}

func TestUnavailableBackendNeverBecomesDemo(t *testing.T) {
	h := newPublic(filepath.Join(t.TempDir(), "missing.sock"), false, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/schedule/week?group=39", nil))
	if w.Code != 503 {
		t.Fatalf("expected unavailable: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/config", nil))
	if !strings.Contains(w.Body.String(), `"demo":false`) {
		t.Fatal("demo enabled by failure")
	}
}

// Ссылка на расписание, кинутая в чат, должна разворачиваться карточкой:
// абсолютным og:image и описанием, а не пустотой.
func TestSharePreviewUsesAbsoluteURLs(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	socket := filepath.Join(t.TempDir(), "api.sock")
	for _, tt := range []struct {
		name, publicURL, host, proto, want string
	}{
		{"из запроса", "", "schedule.example.edu", "https", "https://schedule.example.edu/og.png"},
		{"из настройки", "https://example.org/", "attacker.test", "https", "https://example.org/og.png"},
		{"негодный хост", "", "bad\"host", "", "/og.png"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = tt.host
			if tt.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			w := httptest.NewRecorder()
			newPublic(socket, false, log, tt.publicURL).ServeHTTP(w, r)
			body := w.Body.String()
			if !strings.Contains(body, `<meta property="og:image" content="`+tt.want+`"`) {
				t.Errorf("нет og:image %q", tt.want)
			}
			if strings.Contains(body, "{{origin}}") {
				t.Error("шаблон не подставлен")
			}
			if !strings.Contains(body, `name="twitter:card" content="summary_large_image"`) {
				t.Error("нет twitter-карточки")
			}
		})
	}
	w := httptest.NewRecorder()
	newPublic(socket, false, log, "").ServeHTTP(w, httptest.NewRequest("GET", "/og.png", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("картинка предпросмотра: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}

// Ссылка на группу должна разворачиваться именем группы, а не общим слоганом;
// при этом заголовок страницы не повод дёргать приватный API на каждый заход.
func TestPreviewNamesTheGroupAndCachesIt(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Страница заодно тянет неделю для первого кадра; здесь считаем только
		// походы за именем группы — именно их не должно быть на каждый заход.
		if r.URL.Path == "/schedule/week" {
			w.Write([]byte(`{"group":{"id":39},"week":{"monday":"2026-08-31","days":[]}}`))
			return
		}
		calls.Add(1)
		if r.URL.Path != "/groups/get" || r.URL.Query().Get("id") != "39" {
			t.Errorf("неожиданный запрос: %s", r.URL)
		}
		w.Write([]byte(`{"id":39,"name":"ГР-22"}`))
	})}
	go upstream.Serve(ln)
	t.Cleanup(func() { upstream.Shutdown(context.Background()) })

	h := newPublic(socket, false, slog.New(slog.NewTextHandler(io.Discard, nil)), "https://example.org")
	get := func(path string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Body.String()
	}
	for i := 0; i < 3; i++ {
		body := get("/?group=39")
		if !strings.Contains(body, "<title>ГР-22 · Расписание Тестовый университет</title>") {
			t.Fatal("нет имени группы в заголовке")
		}
		if !strings.Contains(body, `content="https://example.org/?group=39"`) {
			t.Error("ссылка не указывает на группу")
		}
		if !strings.Contains(body, "Расписание группы ГР-22") {
			t.Error("нет имени группы в описании")
		}
	}
	if calls.Load() != 1 {
		t.Errorf("приватный API опрошен %d раз вместо одного", calls.Load())
	}
	if body := get("/"); !strings.Contains(body, "<title>Между парами · Расписание Тестовый университет</title>") {
		t.Error("без группы ожидался общий заголовок")
	}
	if body := get("/?group=не-число"); !strings.Contains(body, "<title>Между парами · Расписание Тестовый университет</title>") {
		t.Error("мусор в параметре не должен менять заголовок")
	}
	// Настройки приложения приезжают в разметке, а не отдельным запросом.
	if body := get("/"); !strings.Contains(body, `&#34;timezone&#34;:&#34;Europe/Moscow&#34;`) {
		t.Errorf("нет настроек в разметке: %s", body[:400])
	}
}

// Статика встроена в бинарь и не имеет даты изменения: без явного тега браузер
// качал бы css и js целиком при каждом открытии.
func TestStaticRevalidatesInsteadOfRedownloading(t *testing.T) {
	h := newPublic(filepath.Join(t.TempDir(), "api.sock"), false, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	for _, path := range []string{"/app.css", "/app.js", "/model.mjs", "/og.png", "/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		tag := w.Header().Get("ETag")
		if tag == "" {
			t.Fatalf("%s: нет ETag", path)
		}
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("If-None-Match", tag)
		again := httptest.NewRecorder()
		h.ServeHTTP(again, r)
		if again.Code != http.StatusNotModified {
			t.Errorf("%s: %d вместо 304", path, again.Code)
		}
		if again.Body.Len() != 0 {
			t.Errorf("%s: тело в ответе 304", path)
		}
	}
}

// Неделя, вложенная в страницу: расписание появляется вместе с разметкой.
// Подгруппу сервер знает только из ссылки, поэтому вкладывать её из куки он
// не вправе — клиент такую неделю отвергнет, но отдавать чужие пары нельзя и
// на один кадр.
func TestPageCarriesTheWeekItsVisitorIsAbout(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	asked := make(chan string, 8)
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/groups/get" {
			w.Write([]byte(`{"id":39,"name":"ГР-22"}`))
			return
		}
		asked <- r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		// Дисциплина с угловой скобкой не должна закрывать блок данных.
		w.Write([]byte(`{"group":{"id":39},"week":{"monday":"2026-09-07","days":[{"date":"2026-09-07","items":[{"id":1,"discipline":"a</script><b"}]}]}}`))
	})}
	go upstream.Serve(ln)
	t.Cleanup(func() { upstream.Shutdown(context.Background()) })
	h := newPublic(socket, false, slog.New(slog.NewTextHandler(io.Discard, nil)), "")

	get := func(path string, cookie string) string {
		r := httptest.NewRequest("GET", path, nil)
		if cookie != "" {
			r.Header.Set("Cookie", "mp_group="+cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}
	body := get("/?group=39&subgroup=334&week=2026-09-09", "")
	if !strings.Contains(body, `<script type="application/json" id="app-week"`) {
		t.Fatal("страница пришла без вложенной недели")
	}
	if strings.Contains(body, "a</script>") {
		t.Error("угловая скобка из данных вышла из блока данных")
	}
	if q := <-asked; q != "group=39&monday=2026-09-07&subgroup=334" {
		t.Errorf("спросили не ту неделю: %q", q)
	}
	// Группа из куки: подгруппу браузер держит у себя, поэтому спрашиваем всю.
	get("/", "39")
	if q := <-asked; strings.Contains(q, "subgroup") {
		t.Errorf("подгруппа из куки взяться не может: %q", q)
	}
	// Экран предмета не должен ждать несвязанную с ним неделю.
	for _, query := range []string{"subject=2", "subject_name=Анатомия"} {
		if body := get("/?group=39&"+query, ""); strings.Contains(body, "app-week") {
			t.Error("в экран предмета вложена лишняя неделя")
		}
	}
	// Без группы вкладывать нечего — и ходить за этим к API незачем.
	if body := get("/", ""); strings.Contains(body, "app-week") {
		t.Error("неделя вложена без группы")
	}
	select {
	case q := <-asked:
		t.Errorf("лишний запрос к API: %q", q)
	default:
	}
}
