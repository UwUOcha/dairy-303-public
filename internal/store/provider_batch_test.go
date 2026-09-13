package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProviderBatchAtomicStableAndDoesNotReserveWriterForReads(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.BindProvider(ctx, "test", false); err != nil {
		t.Fatal(err)
	}
	keys := make([]ProviderIdentity, 550)
	for i := range keys {
		keys[i] = ProviderIdentity{"group", fmt.Sprint(i)}
	}
	ids, err := db.ProviderIDs(ctx, "test", keys, false)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the only writer occupied: already mapped identities and source checks
	// must remain available on the reader pool.
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	work, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	again, err := db.ProviderIDs(work, "test", keys, false)
	bindErr := db.BindProvider(work, "test", false)
	tx.Rollback()
	if err != nil || bindErr != nil || len(again) != len(ids) {
		t.Fatal("reads reserved writer", err, bindErr)
	}
	for key, id := range ids {
		if again[key] != id {
			t.Fatal("unstable identity")
		}
	}
	if _, err := db.ProviderIDs(ctx, "test", []ProviderIdentity{{"group", "new"}, {"invalid", "oops"}}, false); err == nil {
		t.Fatal("accepted invalid batch")
	}
	var count int
	if err := db.r.QueryRow(`SELECT count(*) FROM provider_ids WHERE external_id='new'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial batch committed", count, err)
	}
}

func TestProviderConcurrentBatchesShareIdentities(t *testing.T) {
	db := openTest(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			ids, err := db.ProviderIDs(context.Background(), "test", []ProviderIdentity{{"group", "shared"}, {"teacher", "shared"}}, false)
			if err != nil || ids[ProviderIdentity{"group", "shared"}] != 1 || ids[ProviderIdentity{"teacher", "shared"}] != 1 {
				t.Errorf("concurrent identity allocation: %v %v", ids, err)
			}
		})
	}
	wg.Wait()
}

func TestFreshSchemaUsesNeutralStaffTables(t *testing.T) {
	db := openTest(t)
	var count int
	if err := db.r.QueryRow(`SELECT count(*) FROM sqlite_master WHERE sql LIKE '%apeks%'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy names in new schema", count, err)
	}
}
