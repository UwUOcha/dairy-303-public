package store

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

type Teacher struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	FullName    string   `json:"full_name,omitempty"`
	Degree      string   `json:"degree,omitempty"`
	Departments []string `json:"departments,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
	Groups      []string `json:"groups,omitempty"`
}

// SearchTeachers searches both names and subjects in the selected half-year.
// Unicode matching stays in Go: SQLite LOWER only folds ASCII.
func (db *DB) SearchTeachers(ctx context.Context, query, from, to string, group int64) ([]Teacher, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT DISTINCT s.id, s.name, s.full_name, s.degree,
 (SELECT json_group_array(department) FROM (SELECT department FROM staff_departments WHERE staff_id=s.id ORDER BY department)), COALESCE(di.name,''), g.name
		FROM staff s JOIN lesson_staff ls ON ls.staff_id=s.id
		JOIN lessons l ON l.id=ls.lesson_id
		LEFT JOIN disciplines di ON di.id=l.discipline_id
		JOIN group_lessons gl ON gl.lesson_id=l.id JOIN groups g ON g.id=gl.group_id
		WHERE l.date BETWEEN ? AND ? AND (l.flags & 9)=0
		AND g.is_active=1 AND g.shadowed=0 AND (?=0 OR g.id=?)
		ORDER BY s.name, s.id, di.name, g.name`, from, to, group, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	catalog := []Teacher{}
	indices := map[int64]int{}
	for rows.Next() {
		var id int64
		var name, fullName, degree, departments, subject, groupName string
		if err := rows.Scan(&id, &name, &fullName, &degree, &departments, &subject, &groupName); err != nil {
			return nil, err
		}
		i, ok := indices[id]
		if !ok {
			i = len(catalog)
			indices[id] = i
			var deps []string
			if err := json.Unmarshal([]byte(departments), &deps); err != nil {
				return nil, err
			}
			catalog = append(catalog, Teacher{ID: id, Name: name, FullName: fullName, Degree: degree, Departments: deps, Subjects: []string{}})
		}
		if !slices.Contains(catalog[i].Groups, groupName) {
			catalog[i].Groups = append(catalog[i].Groups, groupName)
		}
		if subject != "" && !slices.Contains(catalog[i].Subjects, subject) {
			catalog[i].Subjects = append(catalog[i].Subjects, subject)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Teacher{}
	query = NormalizeName(query)
	for _, t := range catalog {

		match := strings.Contains(NormalizeName(t.Name), query) || strings.Contains(NormalizeName(t.FullName), query)
		for _, subject := range t.Subjects {
			match = match || strings.Contains(NormalizeName(subject), query)
		}
		if match {
			out = append(out, t)
		}
	}
	score := func(t Teacher) int {
		parts := strings.Fields(t.Name)
		if NormalizeName(t.Name) == query || (len(parts) > 0 && NormalizeName(parts[0]) == query) {
			return 0
		}
		if strings.HasPrefix(NormalizeName(t.Name), query) {
			return 1
		}
		if strings.Contains(NormalizeName(t.Name), query) {
			return 2
		}
		return 3
	}
	if query != "" {
		sort.SliceStable(out, func(i, j int) bool { return score(out[i]) < score(out[j]) })
	}
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

func (db *DB) Teacher(ctx context.Context, id int64) (Teacher, error) {
	var t Teacher
	var departments string
	err := db.r.QueryRowContext(ctx, `SELECT id, name, full_name, degree,
 (SELECT json_group_array(department) FROM (SELECT department FROM staff_departments WHERE staff_id=staff.id ORDER BY department))
 FROM staff WHERE id = COALESCE((SELECT staff_id FROM staff_aliases WHERE id=?),?)`, id, id).Scan(&t.ID, &t.Name, &t.FullName, &t.Degree, &departments)
	if err == nil {
		err = json.Unmarshal([]byte(departments), &t.Departments)
	}
	return t, err
}

// TeacherLessons starts from unique lessons, so a lecture shared by several
// groups appears once. It never fetches every group's schedule from upstream.
func (db *DB) TeacherLessons(ctx context.Context, id int64, from, to string) ([]schedule.Lesson, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT l.id, l.date, l.lesson_time_id,
		COALESCE(lt.minute_from,0), COALESCE(lt.minute_to,0), COALESCE(lt.label,''),
		COALESCE(di.name,''), COALESCE(ct.name,''), COALESCE(cr.name,''),
		l.audience, l.subgroup_id, COALESCE(NULLIF(sg.name,''),l.audience_label), l.flags, l.comments, l.topic,
		(SELECT json_group_array(name) FROM (SELECT s.name FROM lesson_staff ls
		 JOIN staff s ON s.id=ls.staff_id WHERE ls.lesson_id=l.id ORDER BY ls.pos)),
		(SELECT json_group_array(json_object('id',id,'name',name)) FROM
		 (SELECT DISTINCT g.id, g.name FROM group_lessons gl JOIN groups g ON g.id=gl.group_id
		 WHERE gl.lesson_id=l.id AND g.is_active=1 AND g.shadowed=0 ORDER BY g.name, g.id))
		FROM lessons l JOIN lesson_staff target ON target.lesson_id=l.id AND target.staff_id=COALESCE((SELECT staff_id FROM staff_aliases WHERE id=?),?)
		LEFT JOIN lesson_times lt ON lt.id=l.lesson_time_id
		LEFT JOIN disciplines di ON di.id=l.discipline_id
		LEFT JOIN class_types ct ON ct.id=l.class_type_id
		LEFT JOIN classrooms cr ON cr.id=l.classroom_id
		LEFT JOIN subgroups sg ON sg.id=l.subgroup_id
		WHERE l.date BETWEEN ? AND ? AND EXISTS (SELECT 1 FROM group_lessons gl
		JOIN groups g ON g.id=gl.group_id WHERE gl.lesson_id=l.id AND g.is_active=1 AND g.shadowed=0)
		ORDER BY l.date, lt.minute_from, l.id`, id, id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedule.Lesson{}
	for rows.Next() {
		var l schedule.Lesson
		var staff, groups string
		if err := rows.Scan(&l.ID, &l.Date, &l.TimeID, &l.MinuteFrom, &l.MinuteTo, &l.TimeLabel,
			&l.Discipline, &l.ClassType, &l.Classroom, &l.Audience, &l.SubgroupID,
			&l.AudienceLabel, &l.Flags, &l.Comments, &l.Topic, &staff, &groups); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(staff), &l.Staff); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(groups), &l.Groups); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CatalogCoverage counts downloaded group-months, including empty schedules.
// Absence of lessons is never evidence that a teacher is free.
func (db *DB) CatalogCoverage(ctx context.Context, months [][2]int) (loaded, expected int, oldest time.Time, err error) {
	var groups int
	err = db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE is_active=1 AND shadowed=0`).Scan(&groups)
	if err != nil {
		return
	}
	expected = groups * len(months)
	for _, m := range months {
		var n, stamp int64
		err = db.r.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(ms.fetched_at),0)
		FROM month_state ms JOIN groups g ON g.id=ms.group_id
		WHERE g.is_active=1 AND g.shadowed=0 AND ms.year=? AND ms.month=?`, m[0], m[1]).Scan(&n, &stamp)
		if err != nil {
			return
		}
		loaded += int(n)
		if stamp > 0 && (oldest.IsZero() || time.Unix(stamp, 0).Before(oldest)) {
			oldest = time.Unix(stamp, 0)
		}
	}
	return
}
