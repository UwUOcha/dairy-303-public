package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/config"
)

func TestPageDoesNotShortenHistoryCPUInterval(t *testing.T) {
	ctx := context.Background()
	proc := t.TempDir()
	writeCPU := func(busy, idle int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(proc, "stat"), []byte(fmt.Sprintf("cpu %d 0 0 %d 0 0 0\n", busy, idle)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	history := openHistory(t, time.Hour)
	s, err := New(config.Admin{ProcPath: proc, DiskPath: proc, SocketPath: filepath.Join(proc, "нет-сокета")}, slog.New(slog.NewTextHandler(io.Discard, nil)), history)
	if err != nil {
		t.Fatal(err)
	}
	writeCPU(100, 100)
	if err := s.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	// Очищаем точку, чтобы повторный замер в той же секунде не схлопнулся.
	if _, err := history.db.ExecContext(ctx, "DELETE FROM samples"); err != nil {
		t.Fatal(err)
	}
	writeCPU(190, 110)
	s.handleStats(httptest.NewRecorder(), httptest.NewRequest("GET", "/admin/api/stats", nil))
	writeCPU(200, 200)
	if err := s.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	points, err := history.Range(ctx, time.Hour, 100)
	if err != nil || len(points) != 1 || points[0].CPUPercent != 50 {
		t.Fatalf("Интервал CPU истории: %+v, %v", points, err)
	}
}
