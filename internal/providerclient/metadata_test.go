package providerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/store"
	"github.com/UwUOcha/dairy-303-public/pkg/provider"
)

func TestMetadataCacheCapabilityRefreshAndFailure(t *testing.T) {
	var calls, teachers atomic.Int64
	var enabled, fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/teachers" {
			teachers.Add(1)
			http.Error(w, "unexpected directory", 500)
			return
		}
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		json.NewEncoder(w).Encode(provider.Info{Version: "1", Source: "test", Timezone: "UTC", StaffDirectory: enabled.Load()})
	}))
	defer server.Close()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := New(server.URL, "", db, false, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if yes, err := c.HasStaffDirectory(ctx); err != nil || yes {
				t.Errorf("capability: %v %v", yes, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("metadata calls: %d", calls.Load())
	}
	if _, err := c.StaffDirectory(ctx); err == nil || teachers.Load() != 0 {
		t.Fatal("disabled directory requested")
	}
	enabled.Store(true)
	c.infoAt = time.Now().Add(-6 * time.Minute)
	if yes, err := c.HasStaffDirectory(ctx); err != nil || !yes {
		t.Fatal("capability did not refresh", err)
	}
	fail.Store(true)
	if _, err := c.metadata(ctx, true); err == nil {
		t.Fatal("refresh error hidden")
	}
	if _, err := c.metadata(ctx, false); err == nil {
		t.Fatal("stale metadata reused after failed refresh")
	}
	fail.Store(false)
	if yes, err := c.HasStaffDirectory(ctx); err != nil || !yes {
		t.Fatal("failure poisoned retry", err)
	}
}
