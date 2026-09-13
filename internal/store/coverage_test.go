package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCatalogCoverage(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	_, err := db.w.ExecContext(ctx, `
 INSERT INTO groups(id,name,name_norm,is_active,shadowed) VALUES
 (1,'A','a',1,0),(2,'B','b',1,0),(3,'C','c',0,0),(4,'D','d',1,1);
 INSERT INTO month_state(group_id,year,month,content_hash,fetched_at,changed_at) VALUES
 (1,2026,9,'',200,0),(2,2026,9,'',300,0),(1,2026,10,'',400,0),
 (3,2026,9,'',10,0),(4,2026,9,'',20,0),(1,2025,9,'',30,0),(1,2026,12,'',0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name             string
		months           [][2]int
		loaded, expected int
		stamp            int64
	}{
		{"empty", nil, 0, 0, 0},
		{"complete", [][2]int{{2026, 9}}, 2, 2, 200},
		{"partial", [][2]int{{2026, 9}, {2026, 10}, {2026, 11}}, 3, 6, 200},
		{"missing", [][2]int{{2026, 11}}, 0, 2, 0},
		{"duplicate", [][2]int{{2026, 9}, {2026, 9}}, 4, 4, 200},
		{"year", [][2]int{{2025, 9}}, 1, 2, 30},
		{"unknown freshness", [][2]int{{2026, 9}, {2026, 12}}, 3, 4, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loaded, expected, oldest, err := db.CatalogCoverage(ctx, tt.months)
			want := time.Time{}
			if tt.stamp != 0 {
				want = time.Unix(tt.stamp, 0)
			}
			if err != nil || loaded != tt.loaded || expected != tt.expected || !oldest.Equal(want) {
				t.Fatalf("got %d/%d %v (%v), want %d/%d %v", loaded, expected, oldest, err, tt.loaded, tt.expected, want)
			}
		})
	}
}

func TestCatalogCoverageWithoutActiveGroups(t *testing.T) {
	db := openTest(t)
	loaded, expected, oldest, err := db.CatalogCoverage(context.Background(), [][2]int{{2026, 9}})
	if err != nil || loaded != 0 || expected != 0 || !oldest.IsZero() {
		t.Fatalf("empty catalog: %d/%d %v (%v)", loaded, expected, oldest, err)
	}
}

func BenchmarkCatalogCoverage(b *testing.B) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(b.TempDir(), "coverage.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	for group := 1; group <= 527; group++ {
		if _, err := tx.Exec(`INSERT INTO groups(id,name,name_norm) VALUES(?,'A','a')`, group); err != nil {
			b.Fatal(err)
		}
		for month := 1; month <= 12; month++ {
			if _, err := tx.Exec(`INSERT INTO month_state(group_id,year,month,content_hash,fetched_at,changed_at) VALUES(?,2026,?,'',100,0)`, group, month); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	months := [][2]int{{2026, 2}, {2026, 3}, {2026, 4}, {2026, 5}, {2026, 6}, {2026, 7}, {2026, 8}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := db.CatalogCoverage(ctx, months); err != nil {
			b.Fatal(err)
		}
	}
}
