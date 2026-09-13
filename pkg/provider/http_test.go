package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type testService struct{}

func (testService) Info(context.Context) (Info, error) {
	return Info{Version: "1", Source: "test", Timezone: "UTC"}, nil
}
func (testService) Catalog(context.Context) (Catalog, error) { return Catalog{}, nil }
func (testService) Schedule(context.Context, string, string, string) (Snapshot, error) {
	return Snapshot{}, nil
}
func (testService) Directory(context.Context) (Directory, error) { return Directory{}, nil }
func TestHTTPAuthAndValidation(t *testing.T) {
	server := httptest.NewServer(Handler(testService{}, "secret"))
	defer server.Close()
	for _, test := range []struct {
		path, token string
		want        int
	}{{"/v1/info", "", 401}, {"/health", "", 200}, {"/v1/info", "secret", 200}, {"/v1/schedule?group=a&from=2026-09-01&to=2026-10-31", "secret", 400}, {"/v1/schedule?group=a&group=b&from=2026-09-01&to=2026-09-30", "secret", 400}} {
		r, _ := http.NewRequest("GET", server.URL+test.path, nil)
		if test.token != "" {
			r.Header.Set("Authorization", "Bearer "+test.token)
		}
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != test.want {
			t.Fatalf("%s: %d", test.path, res.StatusCode)
		}
	}
}
func TestInvalidSnapshotNeverBecomesAnEmptyAuthoritativeMonth(t *testing.T) {
	valid := Snapshot{GroupID: "uuid", From: "2026-09-01", To: "2026-09-30", Complete: true, Status: "published", FetchedAt: time.Now().UTC().Format(time.RFC3339), Lessons: []Lesson{{ID: "a", Date: "2026-09-09", Start: "09:15", End: "10:45"}}, Subgroups: []Subgroup{}}
	for _, change := range []func(*Snapshot){func(s *Snapshot) { s.Complete = false }, func(s *Snapshot) { s.Status = "unpublished" }, func(s *Snapshot) { s.GroupID = "other" }, func(s *Snapshot) { s.Lessons[0].Date = "2026-10-01" }, func(s *Snapshot) { s.Lessons[0].End = "08:15" }, func(s *Snapshot) { s.Lessons = append(s.Lessons, s.Lessons[0]) }, func(s *Snapshot) { s.Lessons[0].SubgroupID = "missing" }} {
		var v Snapshot
		b, _ := json.Marshal(valid)
		json.Unmarshal(b, &v)
		change(&v)
		if e := ValidateSnapshot(v, "uuid", "2026-09-01", "2026-09-30"); e == nil {
			t.Fatalf("accepted invalid snapshot: %+v", v)
		}
	}
	valid.Lessons[0].Start = ""
	valid.Lessons[0].End = ""
	if e := ValidateSnapshot(valid, "uuid", "2026-09-01", "2026-09-30"); e != nil {
		t.Fatal("unknown time should remain unknown", e)
	}
}
func TestRedirectDoesNotForwardServiceCredential(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true; w.WriteHeader(200) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer server.Close()
	c, _ := NewClient(server.URL, "secret")
	if _, e := c.Info(context.Background()); e == nil || hit {
		t.Fatal("followed adapter redirect")
	}
}

func TestCombinedAudienceMayReferenceNamedExternalSubgroup(t *testing.T) {
	v := Snapshot{GroupID: "g", From: "2026-09-01", To: "2026-09-30", Complete: true, Status: "published", FetchedAt: time.Now().UTC().Format(time.RFC3339), Subgroups: []Subgroup{{ID: "own", Name: "Own"}}, SubgroupNames: map[string]string{"foreign": "Another group's subgroup"}, Lessons: []Lesson{{ID: "shared", Date: "2026-09-14", Audience: "combined", SubgroupID: "foreign", GroupIDs: []string{"g"}}}}
	if e := ValidateSnapshot(v, "g", v.From, v.To); e != nil {
		t.Fatal(e)
	}
	v.Lessons[0].Audience = "subgroup"
	if e := ValidateSnapshot(v, "g", v.From, v.To); e == nil {
		t.Fatal("accepted foreign subgroup as local subgroup event")
	}
	v.Lessons[0].Audience = "combined"
	delete(v.SubgroupNames, "foreign")
	if e := ValidateSnapshot(v, "g", v.From, v.To); e == nil {
		t.Fatal("accepted undeclared external subgroup")
	}
}

func TestDirectoryRejectsInvalidOrConflictingVacancies(t *testing.T) {
	for _, vacancies := range [][]string{{""}, {" bad"}, {"vacant", "vacant"}, {"active"}} {
		v := Directory{Complete: true, FetchedAt: time.Now().UTC().Format(time.RFC3339), Teachers: []Teacher{{ID: "active", Name: "Person"}}, Vacancies: vacancies}
		if err := ValidateDirectory(v); err == nil {
			t.Fatalf("invalid vacancies accepted: %q", vacancies)
		}
	}
	v := Directory{Complete: true, FetchedAt: time.Now().UTC().Format(time.RFC3339), Teachers: []Teacher{}, Vacancies: []string{"vacant"}}
	if err := ValidateDirectory(v); err != nil {
		t.Fatal(err)
	}
}
