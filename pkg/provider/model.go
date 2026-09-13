// Package provider defines version 1 of the language-independent university adapter API.
package provider

import "context"

const Version = "1"

type Info struct {
	Version        string `json:"version"`
	Source         string `json:"source"`
	Timezone       string `json:"timezone"`
	StaffDirectory bool   `json:"staff_directory"`
}
type Department struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Group struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DepartmentID   string `json:"department_id,omitempty"`
	Course         int    `json:"course,omitempty"`
	GraduationYear int    `json:"graduation_year,omitempty"`
	Active         bool   `json:"active"`
	Hidden         bool   `json:"hidden,omitempty"`
}
type Replacement struct {
	MatchSubgroupsByName bool   `json:"match_subgroups_by_name,omitempty"`
	From                 string `json:"from"`
	To                   string `json:"to"`
}
type Catalog struct {
	Complete     bool          `json:"complete"`
	FetchedAt    string        `json:"fetched_at"`
	Departments  []Department  `json:"departments"`
	Groups       []Group       `json:"groups"`
	Replacements []Replacement `json:"replacements,omitempty"`
}
type Subgroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Teacher struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	FullName    string   `json:"full_name,omitempty"`
	Degree      string   `json:"degree,omitempty"`
	Departments []string `json:"departments,omitempty"`
	// Identity explicitly joins multiple source accounts. Names alone never join people.
	Identity string `json:"identity,omitempty"`
}
type Lesson struct {
	ID    string `json:"id"`
	Date  string `json:"date"`
	Start string `json:"start"`
	End   string `json:"end"`
	// SlotID is optional, for stable bell schedules and migration of existing installations.
	SlotID        string    `json:"slot_id,omitempty"`
	Subject       string    `json:"subject"`
	Kind          string    `json:"kind,omitempty"`
	Room          string    `json:"room,omitempty"`
	Teachers      []Teacher `json:"teachers,omitempty"`
	SubgroupID    string    `json:"subgroup_id,omitempty"`
	GroupIDs      []string  `json:"group_ids,omitempty"`
	SubgroupIDs   []string  `json:"subgroup_ids,omitempty"`
	AudienceLabel string    `json:"audience_label,omitempty"`
	// Audience is group, subgroup, flow or combined; optional defaults to group.
	Audience      string `json:"audience,omitempty"`
	AnchorGroupID string `json:"anchor_group_id,omitempty"`
	Empty         bool   `json:"empty,omitempty"`
	SelfWork      bool   `json:"self_work,omitempty"`
	Remote        bool   `json:"remote,omitempty"`
	NonStudy      bool   `json:"non_study,omitempty"`
	Comments      string `json:"comments,omitempty"`
	Topic         string `json:"topic,omitempty"`
}
type TimeSlot struct {
	ID     string `json:"id"`
	Start  string `json:"start"`
	End    string `json:"end"`
	Number int    `json:"number"`
}
type Snapshot struct {
	Slots   []TimeSlot `json:"slots,omitempty"`
	GroupID string     `json:"group_id"`
	From    string     `json:"from"`
	To      string     `json:"to"`
	// Only complete, published snapshots are authoritative for deletions.
	Complete      bool              `json:"complete"`
	Status        string            `json:"status"`
	FetchedAt     string            `json:"fetched_at"`
	Lessons       []Lesson          `json:"lessons"`
	Subgroups     []Subgroup        `json:"subgroups"`
	GroupNames    map[string]string `json:"group_names,omitempty"`
	SubgroupNames map[string]string `json:"subgroup_names,omitempty"`
	Workdays      []int             `json:"workdays,omitempty"`
	// Visibility updates are explicit adapter decisions, e.g. after loading a duplicate group.
	Visibility map[string]bool `json:"visibility,omitempty"`
}
type Directory struct {
	Complete  bool      `json:"complete"`
	FetchedAt string    `json:"fetched_at"`
	Teachers  []Teacher `json:"teachers"`
	Vacancies []string  `json:"vacancies,omitempty"`
}
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Service interface {
	Info(context.Context) (Info, error)
	Catalog(context.Context) (Catalog, error)
	Schedule(context.Context, string, string, string) (Snapshot, error)
	Directory(context.Context) (Directory, error)
}
