package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/botcore"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"github.com/UwUOcha/dairy-303-public/internal/providerclient"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"github.com/UwUOcha/dairy-303-public/internal/syncer"
	"github.com/UwUOcha/dairy-303-public/internal/web"
)

// The second adapter is a separate Python process. It shares no Go imports or
// database with the ecosystem, and uses opaque IDs and non-standard lesson times.
func TestPythonAdapterFeedsRealCoreWebsiteAndBot(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Fatal("python3 required for the independent adapter integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	command := exec.CommandContext(ctx, python, "../examples/adapter-json/server.py")
	command.Env = append(os.Environ(), fmt.Sprintf("ADAPTER_PORT=%d", port), "ADAPTER_HOST=127.0.0.1", "ADAPTER_TOKEN=integration-test", "ADAPTER_DATA=../examples/adapter-json/data.json")
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	ready := false
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for i := 0; i < 100; i++ {
		res, e := client.Get(base + "/health")
		if e == nil {
			res.Body.Close()
			ready = res.StatusCode == 200
			if ready {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Python adapter did not start")
	}
	p, e := profile.Load("../profiles/example.json")
	if e != nil {
		t.Fatal(e)
	}
	profile.Set(p)
	dir := t.TempDir()
	db, e := store.Open(ctx, filepath.Join(dir, "core.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	provider, e := providerclient.New(base, "integration-test", db, false, p.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sync := syncer.New(provider, db, syncer.Options{Location: p.Location()}, log)
	if e = sync.SyncGroupTree(ctx); e != nil {
		t.Fatal(e)
	}
	groups, e := db.SearchGroups(ctx, "Альфа", 10)
	if e != nil || len(groups) != 1 {
		t.Fatal(groups, e)
	}
	group := groups[0].ID
	now := time.Now().In(p.Location())
	for i := 0; i < 2; i++ {
		month := now.AddDate(0, i, 0)
		if e = sync.SyncMonth(ctx, group, month.Year(), int(month.Month())); e != nil {
			t.Fatal(e)
		}
	}
	sock := filepath.Join(dir, "api.sock")
	ln, e := api.Listen(sock)
	if e != nil {
		t.Fatal(e)
	}
	core := &http.Server{Handler: api.NewServer(db, sync, p.Location(), 24*time.Hour, log).Handler()}
	go core.Serve(ln)
	defer core.Close()
	website := httptest.NewServer(web.New(sock, false, log, ""))
	defer website.Close()
	res, e := http.Get(fmt.Sprintf("%s/api/schedule/week?group=%d&monday=2026-09-07", website.URL, group))
	if e != nil {
		t.Fatal(e)
	}
	var week api.WeekResponse
	e = json.NewDecoder(res.Body).Decode(&week)
	res.Body.Close()
	if e != nil || res.StatusCode != 200 {
		t.Fatal(res.Status, e)
	}
	found := false
	for _, day := range week.Week.Days {
		for _, item := range day.Items {
			if item.Discipline == "Математика" && item.MinuteFrom == 555 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("website did not receive normalized Python data")
	}
	bot := botcore.New(api.NewClient(sock), 0, log)
	replies, e := bot.Handle(ctx, botcore.Update{Platform: "tg", UserID: "test-user", Private: true, Text: fmt.Sprintf("/start g%d", group)})
	if e != nil || len(replies) == 0 {
		t.Fatal("bot cannot use independent catalog", e)
	}
	replies, e = bot.Handle(ctx, botcore.Update{Platform: "tg", UserID: "test-user", Private: true, Text: "/week"})
	if e != nil || len(replies) == 0 {
		t.Fatal(e)
	}
	for _, r := range replies {
		if strings.Contains(r.Text, "КГУ") || strings.Contains(r.Text, "Кондрашов") {
			t.Fatal("university-specific bot copy escaped profile")
		}
	}
	// Stop the independent adapter: cached schedules must remain usable.
	command.Process.Kill()
	res, e = http.Get(fmt.Sprintf("%s/api/schedule/week?group=%d&monday=2026-09-07", website.URL, group))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("cached website fails with offline adapter")
	}
}
