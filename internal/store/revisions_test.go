package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

func TestRevisionArchiveRetainsRevertsAndBotBaseline(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	fixNow(t, "2026-09-08")
	ms := importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 9, LessonTimes: testTimes}
	save := func() {
		t.Helper()
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}
	save()
	ms.Lessons = []importdata.Lesson{{ID: 1, GroupID: 232, Date: "2026-09-08", LessonTimeID: 1, Discipline: "Анатомия"}}
	save()
	save() // An unchanged poll isn't a revision.
	ms.Lessons = nil
	save()
	revisions, err := db.ScheduleRevisions(ctx, 232, "2026-09-07")
	if err != nil || len(revisions) != 2 {
		t.Fatalf("%+v %v", revisions, err)
	}
	newest, err := db.ScheduleRevision(ctx, 232, "2026-09-07", revisions[0].ID)
	if err != nil || len(newest.Before) != 1 || len(newest.After) != 0 {
		t.Fatalf("%+v %v", newest, err)
	}
	older, err := db.ScheduleRevision(ctx, 232, "2026-09-07", revisions[1].ID)
	if err != nil || len(older.Before) != 0 || len(older.After) != 1 {
		t.Fatalf("%+v %v", older, err)
	}
	// Both notifications and website read the archive. Compatibility day API
	// still starts at the first edit, as the bot's existing buttons expect.
	event, err := db.Meta(ctx, changeEventKey(232, 2026, 9))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event, "revision_ids") {
		t.Fatal(event)
	}
	before, err := db.changeBeforeFromRevisions(ctx, 232, event)
	if err != nil || len(before) != 1 || len(before[0].Before) != 1 {
		t.Fatalf("%+v %v", before, err)
	}
	day, err := db.ChangedBefore(ctx, 232, "2026-09-08")
	if err != nil || len(day) != 0 {
		t.Fatalf("first baseline: %+v %v", day, err)
	}
	var raw string
	if err := db.r.QueryRowContext(ctx, `SELECT before_json FROM change_days WHERE group_id=232`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "revision_id") {
		t.Fatal("duplicate baseline instead of archive reference", raw)
	}
	// Past schedule dates remain inspectable for the full retention period.
	initial := nowFunc()
	nowFunc = func() time.Time { return initial.Add(13 * 24 * time.Hour) }
	if err := db.PurgeChangedDays(ctx, 0, "2026-09-21"); err != nil {
		t.Fatal(err)
	}
	revisions, err = db.ScheduleRevisions(ctx, 232, "2026-09-07")
	if err != nil || len(revisions) != 2 {
		t.Fatalf("history prematurely removed: %+v %v", revisions, err)
	}
	nowFunc = func() time.Time { return initial.Add(14*24*time.Hour + time.Second) }
	revisions, err = db.ScheduleRevisions(ctx, 232, "2026-09-07")
	if err != nil || len(revisions) != 0 {
		t.Fatal("expired history is publicly visible")
	}
	if _, err := db.ScheduleRevision(ctx, 232, "2026-09-07", newest.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := db.PurgeChangedDays(ctx, 0, "2026-09-22"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.r.QueryRowContext(ctx, `SELECT count(*) FROM schedule_revisions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("purge %d %v", count, err)
	}
}

func TestRevisionWeekSpansMonthsAndRecordsBellTimeChanges(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	fixNow(t, "2026-09-28")
	sep := importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 9, LessonTimes: testTimes, Lessons: []importdata.Lesson{{ID: 1, GroupID: 232, Date: "2026-09-30", LessonTimeID: 1, Discipline: "Химия"}}}
	oct := importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 10, LessonTimes: testTimes, Lessons: []importdata.Lesson{{ID: 2, GroupID: 232, Date: "2026-10-01", LessonTimeID: 1, Discipline: "Анатомия"}}}
	save := func(ms importdata.MonthSchedule) {
		t.Helper()
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}
	save(sep)
	save(oct)
	sep.Lessons[0].Classroom = "A"
	save(sep)
	oct.Lessons[0].Classroom = "B"
	save(oct)
	revs, err := db.ScheduleRevisions(ctx, 232, "2026-09-28")
	if err != nil || len(revs) != 2 {
		t.Fatalf("%+v %v", revs, err)
	}
	rev, err := db.ScheduleRevision(ctx, 232, "2026-09-28", revs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Before) != 2 || len(rev.After) != 2 {
		t.Fatal("month boundary lost context", rev)
	}
	prior, _ := db.ScheduleRevision(ctx, 232, "2026-09-28", revs[1].ID)
	if !sameRevisionLessons(prior.After, rev.Before) {
		t.Fatal("non-consecutive versions")
	}
	oct.LessonTimes = append([]importdata.LessonTime{}, testTimes...)
	oct.LessonTimes[0].MinuteFrom = 540
	save(oct)
	revs, _ = db.ScheduleRevisions(ctx, 232, "2026-09-28")
	if len(revs) != 3 {
		t.Fatal("bell-only change missing")
	}
}

func TestRevisionMigrationRecoversLegacyIntermediate(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	fixNow(t, "2026-09-08")
	ms := importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 9, LessonTimes: testTimes}
	if _, err := db.SaveMonth(ctx, ms); err != nil {
		t.Fatal(err)
	}
	ms.Lessons = []importdata.Lesson{{ID: 1, GroupID: 232, Date: "2026-09-08", LessonTimeID: 1, Discipline: "Анатомия"}}
	if _, err := db.SaveMonth(ctx, ms); err != nil {
		t.Fatal(err)
	}
	before, _, err := db.monthSnapshot(ctx, 232, 2026, 9)
	if err != nil {
		t.Fatal(err)
	}
	ms.Lessons = nil
	if _, err := db.SaveMonth(ctx, ms); err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal([]ChangedDay{{Date: "2026-09-08", Before: before}})
	if err := db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_revisions`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key=?`, string(legacy), changeEventKey(232, 2026, 9)); err != nil {
			return err
		}
		return upgrades[9](ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
	revs, err := db.ScheduleRevisions(ctx, 232, "2026-09-07")
	if err != nil || len(revs) != 1 {
		t.Fatalf("%+v %v", revs, err)
	}
	rev, err := db.ScheduleRevision(ctx, 232, "2026-09-07", revs[0].ID)
	if err != nil || len(rev.Before) != 1 || len(rev.After) != 0 || rev.BeforeAt.Unix() != 0 {
		t.Fatalf("recovery: %+v %v", rev, err)
	}
}

func TestRevisionUpgradeWithoutOldMonthSnapshot(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	fixNow(t, "2026-09-08")
	ms := importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 9, LessonTimes: testTimes, Lessons: []importdata.Lesson{{ID: 1, GroupID: 232, Date: "2026-09-08", LessonTimeID: 1, Discipline: "Химия", Classroom: "A"}}}
	if _, err := db.SaveMonth(ctx, ms); err != nil {
		t.Fatal(err)
	}
	if err := db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, monthSnapshotKey(232, 2026, 9))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ms.Lessons[0].Classroom = "B"
	if _, err := db.SaveMonth(ctx, ms); err != nil {
		t.Fatal(err)
	}
	revs, err := db.ScheduleRevisions(ctx, 232, "2026-09-07")
	if err != nil || len(revs) != 1 {
		t.Fatalf("%+v %v", revs, err)
	}
	rev, err := db.ScheduleRevision(ctx, 232, "2026-09-07", revs[0].ID)
	if err != nil || rev.Before[0].Classroom != "A" || rev.After[0].Classroom != "B" {
		t.Fatalf("%+v %v", rev, err)
	}
}
