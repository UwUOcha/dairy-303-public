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

func TestSettledReplacementRechecksUsersOnLaterImport(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	apply := func() {
		t.Helper()
		if err := db.tx(ctx, func(tx *sql.Tx) error { return applyGroupReplacements(ctx, tx) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.tx(ctx, func(tx *sql.Tx) error {
		return saveGroupReplacements(ctx, tx, []importdata.Replacement{{From: 1, To: 2}})
	}); err != nil {
		t.Fatal(err)
	}
	apply() // No users: this rule is skipped, but must not be retired or cached.
	if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: "new", GroupID: 1}); err != nil {
		t.Fatal(err)
	}
	apply()
	u, err := db.User(ctx, "tg", "new")
	if err != nil || u.GroupID != 2 {
		t.Fatalf("new user left in a previously empty source: %+v %v", u, err)
	}
}

// Models the common month-import case: hundreds of persisted replacement rules,
// with all users already migrated. Rules without users should need no per-rule SQL.
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

func TestGroupChangePreservesCorrespondence(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			db := openTest(t)
			ctx := context.Background()
			u := User{Platform: "tg", ExtID: "пользователь", GroupID: 1}
			if err := db.SaveUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			if err := db.tx(ctx, func(tx *sql.Tx) error {
				for _, kind := range []string{OutboxChange, OutboxAnswer, OutboxFeedback} {
					if _, err := tx.ExecContext(ctx, "INSERT INTO outbox(platform,ext_id,kind,payload,dedup_key,created_at,next_try_at) VALUES(?,?,?,?,?,?,1)", u.Platform, u.ExtID, kind, "{}", kind, 1); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if replacement {
				if err := db.tx(ctx, func(tx *sql.Tx) error {
					if err := saveGroupReplacements(ctx, tx, []importdata.Replacement{{From: 1, To: 2}}); err != nil {
						return err
					}
					return applyGroupReplacements(ctx, tx)
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				u.GroupID = 2
				if err := db.SaveUser(ctx, u); err != nil {
					t.Fatal(err)
				}
			}
			for _, kind := range []string{OutboxChange, OutboxAnswer, OutboxFeedback} {
				var count int
				if err := db.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox WHERE kind=?", kind).Scan(&count); err != nil {
					t.Fatal(err)
				}
				want := 1
				if kind == OutboxChange {
					want = 0
				}
				if count != want {
					t.Fatalf("Очередь %s: %d вместо %d", kind, count, want)
				}
			}
		})
	}
}
