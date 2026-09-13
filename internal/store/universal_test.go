package store

import (
	"context"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"path/filepath"
	"testing"
	"time"
)

func TestGenericReplacementWaitsForSubgroupsAndRetainsSelection(t *testing.T) {
	ctx := context.Background()
	db, e := Open(ctx, filepath.Join(t.TempDir(), "generic.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.SaveGroupTree(ctx, importdata.GroupTree{Groups: []importdata.Group{{ID: 1, Name: "Same", Active: true}, {ID: 2, Name: "Same", Active: true}}}); e != nil {
		t.Fatal(e)
	}
	found, e := db.SearchGroups(ctx, "Same", 10)
	if e != nil || len(found) != 2 {
		t.Fatal("generic core applies university duplicate rules")
	}
	db.SaveSubgroups(ctx, 1, []importdata.Subgroup{{ID: 11, Name: "Lab A"}})
	if e = db.SaveUser(ctx, User{Platform: "tg", ExtID: "test", GroupID: 1, SubgroupID: 11}); e != nil {
		t.Fatal(e)
	}
	tree := importdata.GroupTree{Groups: []importdata.Group{{ID: 2, Name: "Renamed", Active: true}}, Replacements: []importdata.Replacement{{From: 1, To: 2, MatchSubgroupsByName: true}}}
	if e = db.SaveGroupTree(ctx, tree); e != nil {
		t.Fatal(e)
	}
	u, e := db.User(ctx, "tg", "test")
	if e != nil || u.GroupID != 1 || u.SubgroupID != 11 {
		t.Fatal("lost pending subgroup selection", u, e)
	}
	db.SaveSubgroups(ctx, 2, []importdata.Subgroup{{ID: 22, Name: "Lab A"}})
	if e = db.SubgroupsImported(ctx, 2); e != nil {
		t.Fatal(e)
	}
	u, e = db.User(ctx, "tg", "test")
	if e != nil || u.GroupID != 2 || u.SubgroupID != 22 {
		t.Fatal("did not migrate subgroup", u, e)
	}
}
func TestIANAUserOffsetTracksDST(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if e := db.SaveUser(ctx, User{Platform: "tg", ExtID: "dst", Timezone: "Europe/Berlin", TZOffset: 60}); e != nil {
		t.Fatal(e)
	}
	for _, tt := range []struct {
		date   string
		offset int
	}{{"2026-01-15", 60}, {"2026-07-15", 120}, {"2026-12-15", 60}} {
		at, _ := time.Parse("2006-01-02", tt.date)
		if e := db.RefreshUserTimezones(ctx, at); e != nil {
			t.Fatal(e)
		}
		var offset int
		if e := db.r.QueryRowContext(ctx, `SELECT tz_offset FROM users WHERE ext_id='dst'`).Scan(&offset); e != nil || offset != tt.offset {
			t.Fatalf("%s: %d %v", tt.date, offset, e)
		}
	}
}
func TestImportBaselineSuppressesOnlyFirstConversion(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	date := time.Now().UTC().AddDate(0, 1, 0)
	m := importdata.MonthSchedule{GroupID: 1, Year: date.Year(), Month: int(date.Month()), Lessons: []importdata.Lesson{{ID: 1, Date: date.Format("2006-01-02"), Discipline: "Before"}}}
	if _, e := db.SaveMonth(ctx, m); e != nil {
		t.Fatal(e)
	}
	m.BaselineKey = "provider_baseline:test"
	m.Lessons[0].Discipline = "Converted"
	if changed, e := db.SaveMonth(ctx, m); e != nil || changed {
		t.Fatal("conversion notified", changed, e)
	}
	m.Lessons[0].Discipline = "Real update"
	if changed, e := db.SaveMonth(ctx, m); e != nil || !changed {
		t.Fatal("subsequent real change lost", changed, e)
	}
}

func TestLegacyIDDoesNotCollideWithPreviouslyAllocatedOpaqueID(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	first, err := db.ProviderID(ctx, "test", "group", "campus:alpha", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.ProviderID(ctx, "test", "group", fmt.Sprint(first), true)
	if err != nil || second == first {
		t.Fatalf("identity collision: %d %d %v", first, second, err)
	}
	again, err := db.ProviderID(ctx, "test", "group", fmt.Sprint(first), true)
	if err != nil || again != second {
		t.Fatalf("identity changed: %d %d %v", second, again, err)
	}
}
