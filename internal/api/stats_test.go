package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/logbuf"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// statsServer поднимает сервер вместе с подключённой диагностикой: обычный
// testServer её не ставит, а именно она отличает /stats от остальных
// обработчиков.
func statsServer(t *testing.T) (*httptest.Server, *Server, *logbuf.Ring, *stubSync) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	sync := &stubSync{}
	srv := NewServer(db, sync, time.UTC, 24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ring := logbuf.NewRing(logbuf.Capacity)
	srv.Diagnostics(ring, time.Now().Add(-90*time.Minute))

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, ring, sync
}

func getStats(t *testing.T, ts *httptest.Server) StatsResponse {
	t.Helper()
	resp, err := http.Get(ts.URL + PathStats)
	if err != nil {
		t.Fatalf("запрос статистики: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код ответа = %d, ожидался 200", resp.StatusCode)
	}
	var out StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	return out
}

func TestStatsReportsRuntimeAndSyncError(t *testing.T) {
	ts, _, ring, sync := statsServer(t)

	sync.last = errors.New("адаптер не отвечает")
	ring.Add(logbuf.Entry{At: time.Now(), Level: "ERROR", Msg: "месяц не загрузился", Attrs: "группа=39"})

	got := getStats(t, ts)

	if got.Now == 0 {
		t.Error("время демона не проставлено: панель не сможет считать возраст данных")
	}
	// Аптайм считается от момента старта процесса, а не от первого запроса.
	if got.Rasp.UptimeSec < 5000 {
		t.Errorf("аптайм = %d с, ожидалось около 5400", got.Rasp.UptimeSec)
	}
	if got.Rasp.Goroutines == 0 {
		t.Error("число горутин не снято")
	}
	if got.SyncError != "адаптер не отвечает" {
		t.Errorf("ошибка контура = %q, ожидалась переданная синком", got.SyncError)
	}
	if len(got.Log) != 1 || got.Log[0].Msg != "месяц не загрузился" {
		t.Errorf("журнал = %+v, ожидалась одна запись из кольца", got.Log)
	}

	// Сломанный синк не делает сервис мёртвым — то же правило, что и в
	// healthcheck: локальная копия для того и держится.
	if got.Stats.Content.DBBytes <= 0 {
		t.Error("статистика базы не собралась при сломанном синке")
	}
}

// Отчёт бота едет от botd к raspd, потому что у бота нет ни одного входящего
// порта. Проверяется весь путь: приняли, запомнили, отдали панели.
func TestBotReportRoundTrip(t *testing.T) {
	ts, _, _, _ := statsServer(t)

	if got := getStats(t, ts); got.Bot != nil {
		t.Error("до первого отчёта бот должен быть неизвестен, а не пустой структурой")
	}

	report := BotReport{
		Runtime:   Runtime{UptimeSec: 120, Goroutines: 12, Sys: 7 << 20},
		Platforms: []BotCounters{{Platform: "tg", Updates: 40, Sent: 9, Errors: 1}},
		Log:       []logbuf.Entry{{At: time.Now(), Level: "WARN", Msg: "telegram 429"}},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+PathStatsBot, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("отправка отчёта: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код приёма отчёта = %d, ожидался 200", resp.StatusCode)
	}

	got := getStats(t, ts)
	if got.Bot == nil {
		t.Fatal("отчёт бота не сохранился")
	}
	if got.Bot.ReceivedAt == 0 {
		t.Error("не отмечено время приёма: панель не сможет понять, что бот замолчал")
	}
	if len(got.Bot.Platforms) != 1 || got.Bot.Platforms[0].Updates != 40 {
		t.Errorf("счётчики площадок = %+v, ожидались переданные", got.Bot.Platforms)
	}
	if len(got.Bot.Log) != 1 {
		t.Errorf("журнал бота = %+v, ожидалась одна запись", got.Bot.Log)
	}

	// Второй отчёт замещает первый, а не складывается с ним: счётчики считаются
	// от старта процесса, и сумма двух отчётов удвоила бы работу бота.
	report.Platforms[0].Updates = 55
	raw, _ = json.Marshal(report)
	resp, err = http.Post(ts.URL+PathStatsBot, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("повторная отправка: %v", err)
	}
	resp.Body.Close()

	if got := getStats(t, ts); got.Bot.Platforms[0].Updates != 55 {
		t.Errorf("после второго отчёта событий = %d, ожидалось 55", got.Bot.Platforms[0].Updates)
	}
}

// Диагностику подключать необязательно: без неё /stats обязан отдавать цифры
// базы, а не падать. Иначе тесты обработчиков тянули бы за собой полдемона.
func TestStatsWithoutDiagnostics(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := NewServer(db, &stubSync{}, time.UTC, 24*time.Hour,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	got := getStats(t, ts)
	if got.Log != nil {
		t.Errorf("журнал без кольца = %+v, ожидался пустым", got.Log)
	}
	if got.Stats.Users.Total != 0 {
		t.Errorf("пользователей = %d, ожидалось 0", got.Stats.Users.Total)
	}
}

func TestSampleRuntime(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	r := SampleRuntime(started)

	if r.StartedAt != started.Unix() {
		t.Errorf("момент старта = %d, ожидался %d", r.StartedAt, started.Unix())
	}
	if r.UptimeSec < 7100 || r.UptimeSec > 7300 {
		t.Errorf("аптайм = %d с, ожидалось около 7200", r.UptimeSec)
	}
	if r.GoVersion == "" || r.Sys == 0 {
		t.Errorf("снимок рантайма неполон: %+v", r)
	}
}
