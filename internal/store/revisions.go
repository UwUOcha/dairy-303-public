package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

const RevisionTTL = 14 * 24 * time.Hour
const scheduleRevisionsSchema = `CREATE TABLE IF NOT EXISTS schedule_revisions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 group_id INTEGER NOT NULL,
 monday TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 before_at INTEGER NOT NULL,
 before_json TEXT NOT NULL,
 after_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_revisions_week ON schedule_revisions(group_id, monday, id DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_revisions_expiry ON schedule_revisions(created_at);`

type ScheduleRevision struct {
	ID        int64             `json:"id"`
	CreatedAt time.Time         `json:"created_at"`
	BeforeAt  time.Time         `json:"before_at"`
	Before    []schedule.Lesson `json:"before,omitempty"`
	After     []schedule.Lesson `json:"after,omitempty"`
}

// Each observed transition owns both sides. A → B → A remains two revisions,
// independent of browser visits, notification batching and the live lessons table.
// Neighboring month data is read from the group's own feed, never shared lessons.
func saveScheduleRevisions(ctx context.Context, tx *sql.Tx, ms importdata.MonthSchedule, after []schedule.Lesson, fallback []ChangedDay) ([]int64, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, monthSnapshotKey(ms.GroupID, ms.Year, ms.Month)).Scan(&raw)
	var before []schedule.Lesson
	if errors.Is(err, sql.ErrNoRows) {
		if len(fallback) == 0 {
			return nil, nil
		} // First observation is not a change.
		before = restoreChangedDays(after, fallback)
	} else {
		if err != nil {
			return nil, err
		}
		before, err = decodeMonthSnapshot(raw)
		if err != nil {
			return nil, err
		}
	}
	var beforeAt int64
	if err := tx.QueryRowContext(ctx, `SELECT fetched_at FROM month_state WHERE group_id=? AND year=? AND month=?`, ms.GroupID, ms.Year, ms.Month).Scan(&beforeAt); err != nil {
		return nil, err
	}
	return saveRevisionWeeks(ctx, tx, ms, before, after, beforeAt, nowFunc().Unix())
}

func saveRevisionWeeks(ctx context.Context, tx *sql.Tx, ms importdata.MonthSchedule, before, after []schedule.Lesson, beforeAt, now int64) ([]int64, error) {
	ids := []int64{}
	from, to := MonthBounds(ms.Year, ms.Month)
	start, _ := schedule.ParseDate(from)
	start = start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
	for day := start; schedule.FormatDate(day) <= to; day = day.AddDate(0, 0, 7) {
		mon, end := schedule.FormatDate(day), schedule.FormatDate(day.AddDate(0, 0, 6))
		was, next := revisionWeek(before, mon, end), revisionWeek(after, mon, end)
		if sameRevisionLessons(was, next) {
			continue
		}
		// A week may straddle two months. Preserve its unchanged half in both copies.
		neighbor := day
		if mon >= from {
			neighbor = day.AddDate(0, 0, 6)
		}
		if neighbor.Month() != time.Month(ms.Month) || neighbor.Year() != ms.Year {
			var other string
			err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, monthSnapshotKey(ms.GroupID, neighbor.Year(), int(neighbor.Month()))).Scan(&other)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				lessons, err := decodeMonthSnapshot(other)
				if err != nil {
					return nil, err
				}
				extra := revisionWeek(lessons, mon, end)
				was = append(was, extra...)
				next = append(next, extra...)
			}
		}
		b, err := json.Marshal(was)
		if err != nil {
			return nil, err
		}
		a, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		// Use the previous version's observation time where one exists.
		prevAt := beforeAt
		err = tx.QueryRowContext(ctx, `SELECT created_at FROM schedule_revisions WHERE group_id=? AND monday=? ORDER BY id DESC LIMIT 1`, ms.GroupID, mon).Scan(&prevAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO schedule_revisions(group_id,monday,created_at,before_at,before_json,after_json) VALUES(?,?,?,?,?,?)`, ms.GroupID, mon, now, prevAt, string(b), string(a))
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func revisionWeek(lessons []schedule.Lesson, from, to string) []schedule.Lesson {
	out := []schedule.Lesson{}
	for _, l := range lessons {
		if l.Date >= from && l.Date <= to {
			out = append(out, l)
		}
	}
	return out
}

func sameRevisionLessons(a, b []schedule.Lesson) bool {
	// Preserve ID changes for bot notification compatibility; times matter even when the
	// university retains the same slot ID. Themes aren't schedule changes.
	rows := func(ls []schedule.Lesson) []string {
		out := make([]string, 0, len(ls))
		for _, l := range ls {
			staff := append([]string{}, l.Staff...)
			sort.Strings(staff)
			raw, _ := json.Marshal([]any{l.ID, l.Date, l.MinuteFrom, l.MinuteTo, l.Discipline, l.ClassType, l.Classroom, staff, l.SubgroupID, l.Audience, l.AudienceLabel, l.Flags, l.Comments})
			out = append(out, string(raw))
		}
		return out
	}
	return sameRows(rows(a), rows(b))
}

func (db *DB) ScheduleRevisions(ctx context.Context, group int64, monday string) ([]ScheduleRevision, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id,created_at,before_at FROM schedule_revisions WHERE group_id=? AND monday=? AND created_at>=? ORDER BY id DESC`, group, monday, nowFunc().Add(-RevisionTTL).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleRevision{}
	for rows.Next() {
		var r ScheduleRevision
		var at, before int64
		if err := rows.Scan(&r.ID, &at, &before); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(at, 0).UTC()
		r.BeforeAt = time.Unix(before, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) ScheduleRevision(ctx context.Context, group int64, monday string, id int64) (ScheduleRevision, error) {
	var r ScheduleRevision
	var at, before int64
	var b, a string
	err := db.r.QueryRowContext(ctx, `SELECT id,created_at,before_at,before_json,after_json FROM schedule_revisions WHERE group_id=? AND monday=? AND id=? AND created_at>=?`, group, monday, id, nowFunc().Add(-RevisionTTL).Unix()).Scan(&r.ID, &at, &before, &b, &a)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.CreatedAt = time.Unix(at, 0).UTC()
	r.BeforeAt = time.Unix(before, 0).UTC()
	if err := json.Unmarshal([]byte(b), &r.Before); err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(a), &r.After)
	return r, err
}

// The notification event is an index into the same archive the website reads.
// The outbox still freezes each recipient's baseline until delivery, as before.
type changeRevisionRefs struct {
	IDs   []int64  `json:"revision_ids"`
	Dates []string `json:"dates"`
}

func (db *DB) changeBeforeFromRevisions(ctx context.Context, group int64, raw string) ([]ChangedDay, error) {
	var refs changeRevisionRefs
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil, err
	}
	days := map[string][]schedule.Lesson{}
	for _, date := range refs.Dates {
		days[date] = []schedule.Lesson{}
	}
	for _, id := range refs.IDs {
		var before string
		err := db.r.QueryRowContext(ctx, `SELECT before_json FROM schedule_revisions WHERE group_id=? AND id=?`, group, id).Scan(&before)
		if err != nil {
			return nil, err
		}
		var lessons []schedule.Lesson
		if err := json.Unmarshal([]byte(before), &lessons); err != nil {
			return nil, err
		}
		for _, l := range lessons {
			if _, ok := days[l.Date]; ok {
				days[l.Date] = append(days[l.Date], l)
			}
		}
	}
	out := []ChangedDay{}
	for _, date := range refs.Dates {
		out = append(out, ChangedDay{Date: date, Before: days[date]})
	}
	return out, nil
}

// Recover the last transition retained by older installations. We know when
// the after-state was observed, but cannot invent the observation time of its
// predecessor, nor recover earlier versions that were already overwritten.
func backfillScheduleRevisions(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT key,value FROM meta WHERE key LIKE 'change_event:%' ORDER BY key`)
	if err != nil {
		return err
	}
	type event struct{ key, raw string }
	var events []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.key, &e.raw); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range events {
		if !strings.HasPrefix(e.raw, "[") {
			continue
		}
		var group int64
		var year, month int
		if _, err := fmt.Sscanf(e.key, "change_event:%d:%d-%d", &group, &year, &month); err != nil {
			return err
		}
		var at int64
		err := tx.QueryRowContext(ctx, `SELECT changed_at FROM month_state WHERE group_id=? AND year=? AND month=?`, group, year, month).Scan(&at)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if at < nowFunc().Add(-RevisionTTL).Unix() {
			continue
		}
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, monthSnapshotKey(group, year, month)).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		after, err := decodeMonthSnapshot(raw)
		if err != nil {
			return err
		}
		var days []ChangedDay
		if err := json.Unmarshal([]byte(e.raw), &days); err != nil {
			return err
		}
		before := restoreChangedDays(after, days)
		if _, err := saveRevisionWeeks(ctx, tx, importdata.MonthSchedule{GroupID: group, Year: year, Month: month}, before, after, 0, at); err != nil {
			return err
		}
	}
	return nil
}

func restoreChangedDays(after []schedule.Lesson, days []ChangedDay) []schedule.Lesson {
	changed := map[string]bool{}
	before := []schedule.Lesson{}
	for _, d := range days {
		changed[d.Date] = true
		before = append(before, d.Before...)
	}
	for _, l := range after {
		if !changed[l.Date] {
			before = append(before, l)
		}
	}
	return before
}
