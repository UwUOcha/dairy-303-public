package provider

import (
	"fmt"
	"strings"
	"time"
)

func validID(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func ValidateRange(from, to string) error {
	a, e := time.Parse("2006-01-02", from)
	if e != nil {
		return e
	}
	b, e := time.Parse("2006-01-02", to)
	if e != nil || b.Before(a) || b.Sub(a) > 30*24*time.Hour {
		return fmt.Errorf("invalid date range")
	}
	return nil
}
func stamp(s string) error {
	t, e := time.Parse(time.RFC3339, s)
	if e != nil || t.After(time.Now().Add(5*time.Minute)) {
		return fmt.Errorf("invalid fetched_at")
	}
	return nil
}
func ValidateCatalog(v Catalog) error {
	if !v.Complete || v.Groups == nil || v.Departments == nil {
		return fmt.Errorf("incomplete catalog")
	}
	if e := stamp(v.FetchedAt); e != nil {
		return e
	}
	ds := map[string]bool{}
	gs := map[string]bool{}
	for _, d := range v.Departments {
		if !validID(d.ID) || d.Name == "" || ds[d.ID] {
			return fmt.Errorf("invalid department")
		}
		ds[d.ID] = true
	}
	for _, g := range v.Groups {
		if !validID(g.ID) || g.Name == "" || gs[g.ID] || (g.DepartmentID != "" && !ds[g.DepartmentID]) {
			return fmt.Errorf("invalid group")
		}
		gs[g.ID] = true
	}
	replacements := map[string]string{}
	for _, r := range v.Replacements {
		if !validID(r.From) || !gs[r.To] || r.From == r.To || replacements[r.From] != "" {
			return fmt.Errorf("invalid replacement")
		}
		replacements[r.From] = r.To
	}
	for from := range replacements {
		seen := map[string]bool{}
		for id := from; replacements[id] != ""; id = replacements[id] {
			if seen[id] {
				return fmt.Errorf("replacement cycle")
			}
			seen[id] = true
		}
	}
	return nil
}
func Minutes(s string) (int, error) {
	t, e := time.Parse("15:04", s)
	if e != nil {
		return 0, e
	}
	return t.Hour()*60 + t.Minute(), nil
}
func ValidateSnapshot(v Snapshot, g, f, t string) error {
	if e := ValidateRange(f, t); e != nil {
		return e
	}
	if v.GroupID != g || v.From != f || v.To != t {
		return fmt.Errorf("provider snapshot scope mismatch")
	}
	if !v.Complete || v.Status != "published" || v.Lessons == nil || v.Subgroups == nil {
		return fmt.Errorf("schedule is not authoritative (%s)", v.Status)
	}
	if e := stamp(v.FetchedAt); e != nil {
		return e
	}
	ss := map[string]bool{}
	ls := map[string]bool{}
	slots := map[string]string{}
	for _, slot := range v.Slots {
		a, e := Minutes(slot.Start)
		b, e2 := Minutes(slot.End)
		if !validID(slot.ID) || slots[slot.ID] != "" || e != nil || e2 != nil || b <= a || slot.Number < 1 {
			return fmt.Errorf("invalid bell grid")
		}
		slots[slot.ID] = slot.Start + "/" + slot.End
	}
	for _, s := range v.Subgroups {
		if !validID(s.ID) || s.Name == "" || ss[s.ID] {
			return fmt.Errorf("invalid subgroup")
		}
		ss[s.ID] = true
	}
	for _, l := range v.Lessons {
		if !validID(l.ID) || ls[l.ID] || l.Date < f || l.Date > t {
			return fmt.Errorf("invalid lesson identity or date")
		}
		if _, e := time.Parse("2006-01-02", l.Date); e != nil {
			return e
		}
		ls[l.ID] = true
		a, e := Minutes(l.Start)
		b, e2 := Minutes(l.End)
		if (l.Start != "" || l.End != "") && (e != nil || e2 != nil || b <= a) {
			return fmt.Errorf("invalid lesson times")
		}
		if l.SlotID != "" {
			value := l.Start + "/" + l.End
			if slots[l.SlotID] != "" && slots[l.SlotID] != value {
				return fmt.Errorf("conflicting time slot")
			}
			slots[l.SlotID] = value
		}
		if l.SlotID != "" && !validID(l.SlotID) {
			return fmt.Errorf("invalid slot ID")
		}
		for _, id := range append(append([]string{}, l.GroupIDs...), l.SubgroupIDs...) {
			if !validID(id) {
				return fmt.Errorf("invalid audience ID")
			}
		}
		if l.AnchorGroupID != "" && !validID(l.AnchorGroupID) {
			return fmt.Errorf("invalid anchor ID")
		}
		if l.Audience == "subgroup" && l.SubgroupID == "" {
			return fmt.Errorf("subgroup audience requires subgroup_id")
		}
		// A combined event may carry an anchor subgroup from another group.
		// It must be named in the global audience dictionary, never added to
		// this group's selectable subgroup catalog.
		if l.SubgroupID != "" && !ss[l.SubgroupID] && !(l.Audience == "combined" && validID(l.SubgroupID) && v.SubgroupNames[l.SubgroupID] != "") {
			return fmt.Errorf("unknown subgroup")
		}
		switch l.Audience {
		case "", "group", "subgroup", "flow", "combined":
		default:
			return fmt.Errorf("unknown audience")
		}
		for _, p := range l.Teachers {
			if p.Name == "" || (p.ID != "" && !validID(p.ID)) {
				return fmt.Errorf("invalid teacher")
			}
		}
	}
	for _, names := range []map[string]string{v.GroupNames, v.SubgroupNames} {
		for id, name := range names {
			if !validID(id) || name == "" {
				return fmt.Errorf("invalid audience name")
			}
		}
	}
	for id := range v.Visibility {
		if !validID(id) {
			return fmt.Errorf("invalid visibility ID")
		}
	}
	for _, d := range v.Workdays {
		if d < 1 || d > 7 {
			return fmt.Errorf("invalid workday")
		}
	}
	return nil
}
func ValidateDirectory(v Directory) error {
	if !v.Complete || v.Teachers == nil {
		return fmt.Errorf("incomplete directory")
	}
	if e := stamp(v.FetchedAt); e != nil {
		return e
	}
	ids := map[string]bool{}
	for _, p := range v.Teachers {
		if !validID(p.ID) || p.Name == "" || ids[p.ID] {
			return fmt.Errorf("invalid teacher")
		}
		ids[p.ID] = true
	}
	for _, id := range v.Vacancies {
		if !validID(id) || ids[id] {
			return fmt.Errorf("invalid or conflicting vacancy")
		}
		ids[id] = true
	}
	return nil
}
