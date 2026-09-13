package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

func TestReplacementChainContinuesUntilUsersReachDestination(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	// The SQL key order visits 1 -> 2 before 9 -> 1, requiring another pass.
	rs := []importdata.Replacement{{From: 1, To: 2}, {From: 9, To: 1}}
	if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: "chain", GroupID: 9}); err != nil {
		t.Fatal(err)
	}
	if err := db.tx(ctx, func(tx *sql.Tx) error {
		if err := saveGroupReplacements(ctx, tx, rs); err != nil {
			return err
		}
		return applyGroupReplacements(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
	u, err := db.User(ctx, "tg", "chain")
	if err != nil || u.GroupID != 2 {
		t.Fatalf("chain did not finish: %+v %v", u, err)
	}
}

// Models the common month-import case: hundreds of persisted replacement rules,
// with all users already migrated. Work must stop after one unchanged pass.
func BenchmarkSettledGroupReplacements(b *testing.B) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(b.TempDir(), "replacements.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	rs := make([]importdata.Replacement, 259)
	for i := range rs {
		rs[i] = importdata.Replacement{From: int64(i + 1), To: int64(i + 1000), MatchSubgroupsByName: true}
	}
	for i := 0; i < 221; i++ {
		if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: fmt.Sprint(i), GroupID: 1000}); err != nil {
			b.Fatal(err)
		}
	}
	if err := db.tx(ctx, func(tx *sql.Tx) error { return saveGroupReplacements(ctx, tx, rs) }); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.tx(ctx, func(tx *sql.Tx) error { return applyGroupReplacements(ctx, tx) }); err != nil {
			b.Fatal(err)
		}
	}
}
