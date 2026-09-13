package botcore

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/time/rate"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// fakeReacher — площадка, которая отвечает заранее известным образом.
type fakeReacher struct {
	fakeSender
	// reachable — что площадка отвечает про адресата; отсутствие ключа значит
	// «доступен».
	reachable map[string]bool
	// broken — адресаты, о которых площадка отвечает ошибкой.
	broken map[string]bool

	mu    sync.Mutex
	asked []string
}

func (r *fakeReacher) Reachable(_ context.Context, extID string) (bool, error) {
	r.mu.Lock()
	r.asked = append(r.asked, extID)
	r.mu.Unlock()
	if r.broken[extID] {
		return false, errors.New("площадка не ответила")
	}
	ok, seen := r.reachable[extID]
	return ok || !seen, nil
}

// probeServer поднимает raspd-заглушку и отдаёт готовый обходчик.
func probeServer(t *testing.T, targets []store.ProbeTarget, sender *fakeReacher) (*Prober, *sync.Map) {
	t.Helper()

	marks := &sync.Map{}
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathProbeList, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, api.ProbeResponse{Targets: targets})
	})
	mux.HandleFunc(api.PathProbeMark, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		marks.Store(q.Get("ext_id"), q.Get("reachable") == "1")
		writeJSON(w, map[string]bool{"ok": true})
	})

	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := NewProber(api.NewClient(sock), sender, log)
	// Ограничитель в тесте только мешает: скорость проверок — свойство
	// площадки, а не логики обхода.
	p.limit = rate.NewLimiter(rate.Inf, 1)
	return p, marks
}

// Обход записывает ответ площадки о каждом адресате — и молчит о тех, о ком
// ответа не получил.
func TestProbeSweepRecordsAnswers(t *testing.T) {
	sender := &fakeReacher{
		reachable: map[string]bool{"1": true, "2": false},
		broken:    map[string]bool{"3": true},
	}
	p, marks := probeServer(t, []store.ProbeTarget{
		{Platform: "tg", ExtID: "1"},
		{Platform: "tg", ExtID: "2"},
		{Platform: "tg", ExtID: "3"},
	}, sender)

	if err := p.sweep(context.Background()); err != nil {
		t.Fatalf("обход прервался: %v", err)
	}

	if v, ok := marks.Load("1"); !ok || v != true {
		t.Errorf("доступный адресат отмечен как %v (записан: %v)", v, ok)
	}
	if v, ok := marks.Load("2"); !ok || v != false {
		t.Errorf("недоступный адресат отмечен как %v (записан: %v)", v, ok)
	}
	// Молчание площадки — не ответ: такой человек должен остаться в начале
	// очереди, а не получить отметку «проверен».
	if _, ok := marks.Load("3"); ok {
		t.Error("сбой проверки записан как результат")
	}
}

// Когда площадка отказывает всем подряд, обход бросает порцию, а не жжёт
// лимит до конца.
func TestProbeSweepGivesUp(t *testing.T) {
	broken := map[string]bool{}
	var targets []store.ProbeTarget
	for i := 0; i < probeGiveUp*3; i++ {
		id := strconv.Itoa(i)
		broken[id] = true
		targets = append(targets, store.ProbeTarget{Platform: "tg", ExtID: id})
	}
	sender := &fakeReacher{broken: broken}
	p, marks := probeServer(t, targets, sender)

	if err := p.sweep(context.Background()); err == nil {
		t.Fatal("обход при отказе площадки завершился успехом")
	}
	if len(sender.asked) != probeGiveUp {
		t.Errorf("площадку спросили %d раз, ожидалось %d", len(sender.asked), probeGiveUp)
	}
	marks.Range(func(k, _ any) bool {
		t.Errorf("при отказе площадки записан результат для %v", k)
		return false
	})
}

// Пустая очередь — обычное дело: обход её молча пропускает.
func TestProbeSweepEmpty(t *testing.T) {
	sender := &fakeReacher{}
	p, _ := probeServer(t, nil, sender)
	if err := p.sweep(context.Background()); err != nil {
		t.Fatalf("пустой обход вернул ошибку: %v", err)
	}
	if len(sender.asked) != 0 {
		t.Errorf("на пустой очереди площадку спросили %d раз", len(sender.asked))
	}
}

// Клиент обязан звать площадку по её метке: у каждой своя очередь.
func TestProbeAsksOwnPlatform(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathProbeList, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		writeJSON(w, api.ProbeResponse{})
	})
	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := NewProber(api.NewClient(sock), &fakeReacher{}, log)
	if err := p.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Get("platform") != "tg" {
		t.Errorf("очередь запрошена для платформы %q", got.Get("platform"))
	}
	if got.Get("limit") != strconv.Itoa(probeBatch) {
		t.Errorf("размер порции = %q, ожидался %d", got.Get("limit"), probeBatch)
	}
}
