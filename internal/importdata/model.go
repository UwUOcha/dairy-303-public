// Package importdata contains storage-facing normalized records. External adapters use pkg/provider.
package importdata

type Department struct {
	ID   int64
	Name string
}
type Group struct {
	Hidden       bool
	ID           int64
	Name         string
	DepartmentID int64
	Course       int
	GradYear     int
	Active       bool
}
type Replacement struct {
	From, To             int64
	MatchSubgroupsByName bool
}
type GroupTree struct {
	Replacements []Replacement
	Departments  []Department
	Groups       []Group
}
type LessonTime struct {
	Number     int
	ID         int64
	MinuteFrom int
	MinuteTo   int
	Label      string
}
type Subgroup struct {
	ID   int64
	Name string
}
type Staff struct {
	ID   int64
	Name string
}
type Audience uint8

const (
	AudienceGroup Audience = iota
	AudienceFlow
	AudienceSubgroup
	AudienceSuperflow
)

type Lesson struct {
	ID           int64
	Date         string
	LessonTimeID int64

	DisciplineID int64
	Discipline   string
	ClassTypeID  int64
	ClassType    string
	Classroom    string
	Staff        []Staff

	Audience             Audience
	GroupID              int64
	SubgroupID           int64
	FlowNumber           int64
	SuperflowGroupIDs    []int64
	SuperflowSubgroupIDs []int64
	AudienceLabel        string

	IsEmpty  bool
	SelfWork bool
	Remote   bool
	NonStudy bool

	Comments string
	Topic    string
}
type MonthSchedule struct {
	BaselineKey string
	Visibility  map[int64]bool
	FetchedAt   string
	GroupID     int64
	GroupName   string
	Year        int
	Month       int

	Lessons     []Lesson
	LessonTimes []LessonTime
	Workdays    map[int][]int64

	Subgroups []Subgroup
}
