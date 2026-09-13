package botcore

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func TestDrainRenderFailure(t *testing.T) {
	for _, tc := range []struct {
		name, kind, payload string
		retry               bool
	}{
		{"API unavailable", store.OutboxChange, `{"group_id":232}`, true},
		{"bad JSON", store.OutboxChange, `{`, false},
		{"bad feedback", store.OutboxFeedback, `{`, false},
		{"bad answer", store.OutboxAnswer, `{`, false},
		{"unknown kind", "unknown", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var done, retry atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc(api.PathOutboxTake, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"items": []store.OutboxItem{{ID: 1, Platform: "tg", ExtID: "1", Kind: tc.kind, Payload: tc.payload}}})
			})
			mux.HandleFunc(api.PathUserGet, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) })
			mux.HandleFunc(api.PathOutboxDone, func(w http.ResponseWriter, r *http.Request) { done.Add(1); writeJSON(w, map[string]bool{"ok": true}) })
			mux.HandleFunc(api.PathOutboxFail, func(w http.ResponseWriter, r *http.Request) { retry.Add(1); writeJSON(w, map[string]bool{"ok": true}) })
			sock := filepath.Join(t.TempDir(), "api.sock")
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: mux}
			go srv.Serve(ln)
			t.Cleanup(func() { srv.Close() })
			client := api.NewClient(sock)
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			n := NewNotifier(New(client, 180, log), client, &fakeSender{}, 1000, log)
			if err := n.drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.retry && (retry.Load() != 1 || done.Load() != 0) {
				t.Fatal("временный сбой удалил сообщение")
			}
			if !tc.retry && (retry.Load() != 0 || done.Load() != 1) {
				t.Fatal("невалидное сообщение осталось в очереди")
			}
		})
	}
}
