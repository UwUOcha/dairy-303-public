package providerclient_test

import (
	"context"
	"fmt"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/providerclient"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"github.com/UwUOcha/dairy-303-public/internal/syncer"
	"github.com/UwUOcha/dairy-303-public/pkg/provider"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type fixture struct {
	decorate func(*provider.Snapshot)
	source   string
	complete bool
	empty    bool
	start    string
	shared   bool
	fetched  string
}

func (f *fixture) Info(context.Context) (provider.Info, error) {
	return provider.Info{Version: "1", Source: f.source, Timezone: "UTC", StaffDirectory: true}, nil
}
func (f *fixture) Catalog(context.Context) (provider.Catalog, error) {
	return provider.Catalog{Complete: true, FetchedAt: f.fetched, Departments: []provider.Department{}, Groups: []provider.Group{{ID: "alpha", Name: "Same name", Active: true, Course: 1}, {ID: "beta", Name: "Same name", Active: true, Course: 2}}}, nil
}
func (f *fixture) Schedule(_ context.Context, g, a, b string) (provider.Snapshot, error) {
	v := provider.Snapshot{GroupID: g, From: a, To: b, Complete: f.complete, Status: "published", FetchedAt: f.fetched, Subgroups: []provider.Subgroup{{ID: g + ":sub", Name: "1"}}, Lessons: []provider.Lesson{}, Workdays: []int{1, 2, 3, 4, 5}}
	if !f.empty {
		id := g + ":" + a
		if f.shared {
			id = "shared:" + a
		}
		v.Lessons = []provider.Lesson{{ID: id, Date: a, Start: f.start, End: "11:20", SlotID: "slot-1", Subject: "Algorithms", Kind: "lecture", AnchorGroupID: g, Teachers: []provider.Teacher{{ID: "person:1", Name: "Same Person"}, {ID: "person:2", Name: "Same Person"}}}}
	}
	if f.decorate != nil {
		f.decorate(&v)
	}
	return v, nil
}
func (f *fixture) Directory(context.Context) (provider.Directory, error) {
	return provider.Directory{Complete: true, FetchedAt: f.fetched, Teachers: []provider.Teacher{{ID: "person:1", Name: "Same Person", FullName: "Same Full Name"}, {ID: "person:2", Name: "Same Person", FullName: "Same Full Name"}}}, nil
}
func setup(t *testing.T) (*fixture, *store.DB, *providerclient.Client, *syncer.Syncer, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	db, e := store.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{source: "independent", complete: true, start: "09:10", fetched: time.Now().UTC().Format(time.RFC3339)}
	server := httptest.NewServer(provider.Handler(f, "test-credential"))
	t.Cleanup(server.Close)
	c, e := providerclient.New(server.URL, "test-credential", db, false, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	s := syncer.New(c, db, syncer.Options{Location: time.UTC}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e = s.SyncGroupTree(ctx); e != nil {
		t.Fatal(e)
	}
	return f, db, c, s, server.URL
}
func period() (int, int, string, string) {
	now := time.Now().UTC().AddDate(0, 1, 0)
	y, m := now.Year(), int(now.Month())
	a, b := store.MonthBounds(y, m)
	return y, m, a, b
}
func groupID(t *testing.T, db *store.DB, g string) int64 {
	t.Helper()
	id, e := db.ProviderID(context.Background(), "independent", "group", g, false)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func TestIndependentAdapterImportRejectsIncompleteAndPreservesIdentities(t *testing.T) {
	f, db, _, s, base := setup(t)
	ctx := context.Background()
	g := groupID(t, db, "alpha")
	y, m, a, b := period()
	gs, e := db.SearchGroups(ctx, "same", 10)
	if e != nil || len(gs) != 2 {
		t.Fatalf("core guessed duplicates: %v %v", gs, e)
	}
	var changes int
	s.OnChange(func(context.Context, int64, int, int) { changes++ })
	for range 2 {
		if e = s.SyncMonth(ctx, g, y, m); e != nil {
			t.Fatal(e)
		}
	}
	if changes != 0 {
		t.Fatal("initial or repeated snapshot notified")
	}
	before, e := db.MonthState(ctx, g, y, m)
	if e != nil {
		t.Fatal(e)
	}
	f.complete = false
	f.empty = true
	if e = s.SyncMonth(ctx, g, y, m); e == nil {
		t.Fatal("accepted incomplete snapshot")
	}
	after, _ := db.MonthState(ctx, g, y, m)
	if before != after {
		t.Fatal("failed import changed persisted state")
	}
	lessons, e := db.Lessons(ctx, g, a, b)
	if e != nil || len(lessons) != 1 {
		t.Fatal("lost cached lessons", e)
	}
	lessonID := lessons[0].ID
	// Same binary and database, fresh HTTP client after a restart.
	c2, e := providerclient.New(base, "test-credential", db, false, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	f.complete = true
	f.empty = false
	ms, e := c2.Month(ctx, g, y, m)
	if e != nil || ms.Lessons[0].ID != lessonID {
		t.Fatal("identity changed after client restart", e)
	}
	dir, e := c2.StaffDirectory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.SaveStaffDirectory(ctx, dir, time.Now()); e != nil {
		t.Fatal(e)
	}
	people, e := db.SearchTeachers(ctx, "Same", a, b, 0)
	if e != nil || len(people) != 2 {
		t.Fatalf("names merged independent people: %+v %v", people, e)
	}
	f.source = "different-university"
	if _, e = c2.GroupTree(ctx); e == nil {
		t.Fatal("accepted a different provider into live database")
	}
}
func TestExplicitTimesDoNotRewriteOtherGroupsAndChangesAreIdempotent(t *testing.T) {
	f, db, _, s, _ := setup(t)
	ctx := context.Background()
	aID, bID := groupID(t, db, "alpha"), groupID(t, db, "beta")
	y, m, a, b := period()
	if e := s.SyncMonth(ctx, aID, y, m); e != nil {
		t.Fatal(e)
	}
	if e := s.SyncMonth(ctx, bID, y, m); e != nil {
		t.Fatal(e)
	}
	var changes int
	s.OnChange(func(context.Context, int64, int, int) { changes++ })
	f.start = "09:40"
	for range 2 {
		if e := s.SyncMonth(ctx, aID, y, m); e != nil {
			t.Fatal(e)
		}
	}
	first, _ := db.Lessons(ctx, aID, a, b)
	second, _ := db.Lessons(ctx, bID, a, b)
	if first[0].MinuteFrom != 580 || second[0].MinuteFrom != 550 || changes != 1 {
		t.Fatalf("time isolation / change count: %v %v %d", first, second, changes)
	}
	grid, e := db.GridForGroup(ctx, aID)
	if e != nil || grid.NumberOf(first[0].TimeID) != 0 {
		t.Fatal("invented bell number", e)
	}
}
func TestDeletionOnlyAffectsAuthoritativeScope(t *testing.T) {
	f, db, _, s, _ := setup(t)
	ctx := context.Background()
	f.shared = true
	aID, bID := groupID(t, db, "alpha"), groupID(t, db, "beta")
	y, m, a, b := period()
	for _, g := range []int64{aID, bID} {
		if e := s.SyncMonth(ctx, g, y, m); e != nil {
			t.Fatal(e)
		}
	}
	f.empty = true
	if e := s.SyncMonth(ctx, aID, y, m); e != nil {
		t.Fatal(e)
	}
	first, _ := db.Lessons(ctx, aID, a, b)
	second, _ := db.Lessons(ctx, bID, a, b)
	if len(first) != 0 || len(second) != 1 {
		t.Fatal("deletion crossed group scope")
	}
	f.fetched = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if e := s.SyncMonth(ctx, bID, y, m); e == nil {
		t.Fatal("accepted stale snapshot")
	}
}
func TestLegacyIDsRequireOptInAndStayStable(t *testing.T) {
	ctx := context.Background()
	db, e := store.Open(ctx, filepath.Join(t.TempDir(), "legacy.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.SaveGroupTree(ctx, importdata.GroupTree{Groups: []importdata.Group{{ID: 545, Name: "Existing", Active: true}}}); e != nil {
		t.Fatal(e)
	}
	if e = db.BindProvider(ctx, "legacy", false); e == nil {
		t.Fatal("silently adopted existing catalog")
	}
	if e = db.BindProvider(ctx, "legacy", true); e != nil {
		t.Fatal(e)
	}
	id, e := db.ProviderID(ctx, "legacy", "group", "545", true)
	if e != nil || id != 545 {
		t.Fatal("lost legacy group ID", id, e)
	}
	newID, e := db.ProviderID(ctx, "legacy", "group", "uuid:new", false)
	if e != nil || newID <= 545 {
		t.Fatal("new ID collided", newID, e)
	}
}

func TestUnusedAudienceNamesDoNotAllocateLocalIdentities(t *testing.T) {
	var nextGroup, nextSubgroup int64
	for _, extraNames := range []bool{false, true} {
		f, db, client, _, _ := setup(t)
		f.decorate = func(v *provider.Snapshot) {
			v.Lessons[0].Audience = "combined"
			v.Lessons[0].SubgroupID = "foreign:sub"
			v.Lessons[0].GroupIDs = []string{"alpha"}
			v.Lessons[0].SubgroupIDs = []string{"foreign:sub"}
			v.GroupNames = map[string]string{"alpha": "Своя группа", "beta": "Другая группа"}
			v.SubgroupNames = map[string]string{"foreign:sub": "Чужая подгруппа"}
			// Явное решение адаптера должно применяться и к группе вне занятий.
			v.Visibility = map[string]bool{"beta": true}
			if extraNames {
				for i := range 2000 {
					v.GroupNames[fmt.Sprint("unused-group:", i)] = "Лишняя группа"
					v.SubgroupNames[fmt.Sprint("unused-subgroup:", i)] = "Лишняя подгруппа"
				}
			}
		}
		ctx := context.Background()
		y, m, _, _ := period()
		ms, err := client.Month(ctx, groupID(t, db, "alpha"), y, m)
		if err != nil {
			t.Fatal(err)
		}
		if ms.Lessons[0].SubgroupID == 0 || len(ms.Subgroups) != 1 || ms.Subgroups[0].ID == ms.Lessons[0].SubgroupID {
			t.Fatal("потеряна принадлежность чужой подгруппы", ms)
		}
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
		groups, err := db.SearchGroups(ctx, "same", 10)
		if err != nil || len(groups) != 1 || groups[0].ID != ms.GroupID {
			t.Fatal("не применена явная видимость", groups, err)
		}
		group, err := db.ProviderID(ctx, "independent", "group", "next-group", false)
		if err != nil {
			t.Fatal(err)
		}
		subgroup, err := db.ProviderID(ctx, "independent", "subgroup", "next-subgroup", false)
		if err != nil {
			t.Fatal(err)
		}
		if extraNames && (group != nextGroup || subgroup != nextSubgroup) {
			t.Fatalf("лишние словари выделили локальные ID: группа %d → %d, подгруппа %d → %d", nextGroup, group, nextSubgroup, subgroup)
		}
		nextGroup, nextSubgroup = group, subgroup
	}
}
