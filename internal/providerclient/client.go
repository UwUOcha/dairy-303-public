// Package providerclient translates the public adapter contract into local storage IDs.
package providerclient

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
	"github.com/UwUOcha/dairy-303-public/pkg/provider"
)

type Client struct {
	remote   *provider.Client
	db       *store.DB
	legacy   bool
	timezone string
	mu       sync.Mutex
	info     provider.Info
	infoAt   time.Time
}

func New(base, token string, db *store.DB, legacy bool, timezone string) (*Client, error) {
	r, e := provider.NewClient(base, token)
	if e != nil {
		return nil, e
	}
	return &Client{remote: r, db: db, legacy: legacy, timezone: timezone}, nil
}
func (c *Client) metadata(ctx context.Context, refresh bool) (provider.Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !refresh && !c.infoAt.IsZero() && time.Since(c.infoAt) < 5*time.Minute {
		return c.info, nil
	}
	// Never reuse stale metadata after a failed refresh (including a changed source).
	c.infoAt = time.Time{}
	i, e := c.remote.Info(ctx)
	if e != nil {
		return i, e
	}
	if _, e = time.LoadLocation(i.Timezone); e != nil {
		return i, e
	}
	if i.Timezone != c.timezone {
		return i, fmt.Errorf("provider timezone %s does not match profile %s", i.Timezone, c.timezone)
	}
	if e = c.db.BindProvider(ctx, i.Source, c.legacy); e != nil {
		return i, e
	}
	c.info, c.infoAt = i, time.Now()
	return i, nil
}
func (c *Client) HasStaffDirectory(ctx context.Context) (bool, error) {
	i, err := c.metadata(ctx, false)
	return i.StaffDirectory, auth(err)
}
func (c *Client) GroupTree(ctx context.Context) (out importdata.GroupTree, err error) {
	i, e := c.metadata(ctx, true)
	if e != nil {
		return out, auth(e)
	}
	v, e := c.remote.Catalog(ctx)
	if e != nil {
		return out, auth(e)
	}
	m := mapper{c: c, ctx: ctx, source: i.Source}
	for _, d := range v.Departments {
		m.add("department", d.ID)
	}
	for _, g := range v.Groups {
		m.add("group", g.ID)
		m.add("department", g.DepartmentID)
	}
	for _, r := range v.Replacements {
		m.add("group", r.From, r.To)
	}
	if e := m.resolve(); e != nil {
		return out, e
	}

	for _, d := range v.Departments {
		out.Departments = append(out.Departments, importdata.Department{ID: m.id("department", d.ID), Name: d.Name})
	}
	for _, g := range v.Groups {
		out.Groups = append(out.Groups, importdata.Group{ID: m.id("group", g.ID), Name: g.Name, DepartmentID: m.id("department", g.DepartmentID), Course: g.Course, GradYear: g.GraduationYear, Active: g.Active, Hidden: g.Hidden})
	}
	for _, r := range v.Replacements {
		out.Replacements = append(out.Replacements, importdata.Replacement{MatchSubgroupsByName: r.MatchSubgroupsByName, From: m.id("group", r.From), To: m.id("group", r.To)})
	}
	return out, m.err
}
func (c *Client) Month(ctx context.Context, group int64, year, month int) (out importdata.MonthSchedule, err error) {
	i, e := c.metadata(ctx, false)
	if e != nil {
		return out, auth(e)
	}
	external, e := c.db.ProviderExternalID(ctx, i.Source, "group", group)
	if e != nil {
		if !c.legacy || !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		external = strconv.FormatInt(group, 10)
	}
	from, to := store.MonthBounds(year, month)
	v, e := c.remote.Schedule(ctx, external, from, to)
	if e != nil {
		return out, auth(e)
	}
	m := mapper{c: c, ctx: ctx, source: i.Source}
	for _, g := range v.Subgroups {
		m.add("subgroup", g.ID)
	}
	for k := range v.GroupNames {
		m.add("group", k)
	}
	for k := range v.SubgroupNames {
		m.add("subgroup", k)
	}
	for k := range v.Visibility {
		m.add("group", k)
	}
	slotKeys := map[string]string{}
	for _, t := range v.Slots {
		key, e := c.db.ProviderSlotKey(ctx, t.ID, t.Start, t.End, c.legacy)
		if e != nil {
			return out, e
		}
		slotKeys[t.ID] = key
		m.add("slot", key)
	}
	lessonKeys := make([]string, len(v.Lessons))
	for n, l := range v.Lessons {
		m.add("lesson", l.ID)
		m.add("group", l.AnchorGroupID)
		m.add("group", l.GroupIDs...)
		m.add("subgroup", l.SubgroupID)
		m.add("subgroup", l.SubgroupIDs...)
		for _, p := range l.Teachers {
			m.add("teacher", p.ID)
		}
		if l.Start != "" {
			key := slotKeys[l.SlotID]
			if key == "" {
				var e error
				key, e = c.db.ProviderSlotKey(ctx, l.SlotID, l.Start, l.End, c.legacy)
				if e != nil {
					return out, e
				}
			}
			lessonKeys[n] = key
			m.add("slot", key)
		}
	}
	if e := m.resolve(); e != nil {
		return out, e
	}

	out = importdata.MonthSchedule{GroupID: group, Year: year, Month: month, Workdays: map[int][]int64{}, GroupNames: map[int64]string{}, SubgroupNames: map[int64]string{}, Visibility: map[int64]bool{}, FetchedAt: v.FetchedAt}
	if c.legacy {
		out.BaselineKey = fmt.Sprintf("provider_baseline:%s:%d:%d:%d", i.Source, group, year, month)
	}
	for _, s := range v.Subgroups {
		out.Subgroups = append(out.Subgroups, importdata.Subgroup{ID: m.id("subgroup", s.ID), Name: s.Name})
	}
	for k, n := range v.GroupNames {
		out.GroupNames[m.id("group", k)] = n
	}
	for k, n := range v.SubgroupNames {
		out.SubgroupNames[m.id("subgroup", k)] = n
	}
	for _, d := range v.Workdays {
		out.Workdays[d] = nil
	}
	for k, h := range v.Visibility {
		out.Visibility[m.id("group", k)] = h
	}
	times := map[int64]bool{}
	slotIDs := map[string]int64{}
	for _, t := range v.Slots {
		a, _ := provider.Minutes(t.Start)
		b, _ := provider.Minutes(t.End)
		key := slotKeys[t.ID]
		sid := m.id("slot", key)
		slotIDs[t.ID] = sid
		times[sid] = true
		out.LessonTimes = append(out.LessonTimes, importdata.LessonTime{ID: sid, MinuteFrom: a, MinuteTo: b, Label: t.Start + " - " + t.End, Number: t.Number})
	}
	for n, l := range v.Lessons {
		timeID := int64(0)
		if l.Start != "" {
			if sid := slotIDs[l.SlotID]; sid != 0 {
				timeID = sid
			} else {
				key := lessonKeys[n]
				timeID = m.id("slot", key)
			}
		}
		a, _ := provider.Minutes(l.Start)
		b, _ := provider.Minutes(l.End)
		if timeID != 0 && !times[timeID] {
			out.LessonTimes = append(out.LessonTimes, importdata.LessonTime{ID: timeID, MinuteFrom: a, MinuteTo: b, Label: l.Start + " - " + l.End})
			times[timeID] = true
		}
		kind := importdata.AudienceGroup
		switch l.Audience {
		case "subgroup":
			kind = importdata.AudienceSubgroup
		case "flow":
			kind = importdata.AudienceFlow
		case "combined":
			kind = importdata.AudienceSuperflow
		}
		item := importdata.Lesson{ID: m.id("lesson", l.ID), Date: l.Date, LessonTimeID: timeID, Discipline: l.Subject, ClassType: l.Kind, Classroom: l.Room, GroupID: m.id("group", l.AnchorGroupID), SubgroupID: m.id("subgroup", l.SubgroupID), Audience: kind, AudienceLabel: l.AudienceLabel, IsEmpty: l.Empty, SelfWork: l.SelfWork, Remote: l.Remote, NonStudy: l.NonStudy, Comments: l.Comments, Topic: l.Topic}
		for _, p := range l.Teachers {
			item.Staff = append(item.Staff, importdata.Staff{ID: m.id("teacher", p.ID), Name: p.Name})
		}
		for _, id := range l.GroupIDs {
			item.SuperflowGroupIDs = append(item.SuperflowGroupIDs, m.id("group", id))
		}
		for _, id := range l.SubgroupIDs {
			item.SuperflowSubgroupIDs = append(item.SuperflowSubgroupIDs, m.id("subgroup", id))
		}
		out.Lessons = append(out.Lessons, item)
	}
	return out, m.err
}
func (c *Client) StaffDirectory(ctx context.Context) (out importdata.StaffDirectory, err error) {
	i, e := c.metadata(ctx, false)
	if e != nil {
		return out, auth(e)
	}
	if !i.StaffDirectory {
		return out, &provider.Error{Code: "not_supported", Message: "staff directory unavailable"}
	}
	v, e := c.remote.Directory(ctx)
	if e != nil {
		return out, auth(e)
	}
	out.FetchedAt = v.FetchedAt
	m := mapper{c: c, ctx: ctx, source: i.Source}
	for _, p := range v.Teachers {
		m.add("teacher", p.ID)
	}
	m.add("teacher", v.Vacancies...)
	if e := m.resolve(); e != nil {
		return out, e
	}

	for _, p := range v.Teachers {
		out.Staff = append(out.Staff, importdata.StaffDetails{ID: m.id("teacher", p.ID), Name: p.Name, FullName: p.FullName, Degree: p.Degree, Departments: p.Departments, Identity: p.Identity})
	}
	for _, id := range v.Vacancies {
		out.Vacancies = append(out.Vacancies, m.id("teacher", id))
	}
	return out, m.err
}

type mapper struct {
	c      *Client
	ctx    context.Context
	source string
	err    error
	keys   []store.ProviderIdentity
	cache  map[store.ProviderIdentity]int64
}

func (m *mapper) add(kind string, ids ...string) {
	for _, id := range ids {
		m.keys = append(m.keys, store.ProviderIdentity{Kind: kind, External: id})
	}
}
func (m *mapper) resolve() error {
	m.cache, m.err = m.c.db.ProviderIDs(m.ctx, m.source, m.keys, m.c.legacy)
	return m.err
}
func (m *mapper) id(kind, id string) int64 {
	if id == "" {
		return 0
	}
	v, ok := m.cache[store.ProviderIdentity{Kind: kind, External: id}]
	if !ok && m.err == nil {
		m.err = fmt.Errorf("unresolved %s identity %q", kind, id)
	}
	return v
}
func auth(e error) error {
	var p *provider.Error
	if errors.As(e, &p) && (p.Code == "unauthorized" || p.Code == "upstream_auth") {
		return fmt.Errorf("%w: %s", importdata.ErrAuth, p.Code)
	}
	return e
}
