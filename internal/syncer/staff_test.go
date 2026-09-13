package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func TestStaffMonthlyCacheSurvivesRestartAndFailure(t *testing.T) {
	ctx := context.Background()
	body, err := json.Marshal(importdata.StaffDirectory{Staff: []importdata.StaffDetails{{ID: 1, Name: "Иванов И.И.", FullName: "Иванов Иван Иванович", Identity: "person:one"}}})
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/staff" || len(r.URL.Query()) != 0 {
			t.Error("directory refresh requested schedule or used wrong token")
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	s, db := testSyncer(t, srv.URL+"/group")
	s.client = fixtureSource{base: srv.URL, staff: true}
	if err := s.SyncStaffDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	// Both a fresh syncer and concurrent callers use the persisted timestamp.
	restarted := New(s.client, db, Options{}, testLog())
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := restarted.SyncStaffDirectory(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("repeat fetch: %d", hits.Load())
	}
	old := fmt.Sprint(s.Now().AddDate(0, -2, 0).Unix())
	for _, key := range []string{store.MetaStaffSyncedAt, store.MetaStaffAttemptedAt} {
		if err := db.SetMeta(ctx, key, old); err != nil {
			t.Fatal(err)
		}
	}
	fail.Store(true)
	if err := restarted.SyncStaffDirectory(ctx); err == nil {
		t.Fatal("expected API failure")
	}
	if err := restarted.SyncStaffDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("failure retried this month: %d", hits.Load())
	}
	stamp, err := db.Meta(ctx, store.MetaStaffSyncedAt)
	if err != nil || stamp != old {
		t.Fatal("failure changed successful timestamp")
	}
	teacher, err := db.Teacher(ctx, 1)
	if err != nil || teacher.FullName == "" {
		t.Fatalf("saved information lost: %+v %v", teacher, err)
	}
	// Optional token and schedule operations remain independent.
	noToken := New(fixtureSource{base: srv.URL}, db, Options{}, testLog())
	if err := noToken.SyncStaffDirectory(ctx); err != nil || hits.Load() != 2 {
		t.Fatal("missing optional token should skip")
	}
	if _, err := db.Meta(ctx, store.MetaFullSyncAt); err != nil {
		t.Fatal(err)
	}
}
