package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// Потоковое занятие хранится один раз, но обновляется из нескольких лент.
// Независимый сжатый снимок ленты не даёт обновлению первой группы стереть
// исходное состояние для уведомлений подписчиков остальных групп потока.
func monthSnapshotKey(group int64, year, month int) string {
	return fmt.Sprintf("month_snapshot:%d:%04d-%02d", group, year, month)
}

func saveMonthSnapshot(ctx context.Context, tx *sql.Tx, ms importdata.MonthSchedule, touched []ChangedDay, notify bool) error {
	if ms.FetchedAt != "" {
		if err := saveImportMetadata(ctx, tx, ms); err != nil {
			return err
		}
		for _, t := range ms.LessonTimes {
			if _, e := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprintf("slot_number:%d", t.ID), fmt.Sprint(t.Number)); e != nil {
				return e
			}
		}
		days, _ := json.Marshal(ms.Workdays)
		if _, e := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprintf("group_workdays:%d", ms.GroupID), string(days)); e != nil {
			return e
		}
	}
	if ms.BaselineKey != "" {
		if _, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO meta(key,value) VALUES(?,'1')`, ms.BaselineKey); e != nil {
			return e
		}
	}
	times := map[int64]importdata.LessonTime{}
	for _, tm := range ms.LessonTimes {
		times[tm.ID] = tm
	}
	lessons := make([]schedule.Lesson, 0, len(ms.Lessons))
	for _, l := range ms.Lessons {
		tm := times[l.LessonTimeID]
		item := schedule.Lesson{ID: l.ID, Date: l.Date, TimeID: l.LessonTimeID, MinuteFrom: tm.MinuteFrom, MinuteTo: tm.MinuteTo, TimeLabel: tm.Label,
			Discipline: l.Discipline, ClassType: l.ClassType, Classroom: l.Classroom, Audience: schedule.Audience(l.Audience),
			SubgroupID: l.SubgroupID, AudienceLabel: l.AudienceLabel, Flags: flagsOf(l), Comments: l.Comments}
		for _, staff := range l.Staff {
			item.Staff = append(item.Staff, staff.Name)
		}
		lessons = append(lessons, item)
	}
	ids, err := saveScheduleRevisions(ctx, tx, ms, lessons, touched)
	if err != nil {
		return err
	}
	if notify {
		refs := changeRevisionRefs{IDs: ids}
		for _, d := range touched {
			refs.Dates = append(refs.Dates, d.Date)
		}
		raw, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, changeEventKey(ms.GroupID, ms.Year, ms.Month), string(raw)); err != nil {
			return err
		}
	}
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err := json.NewEncoder(writer).Encode(lessons); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, monthSnapshotKey(ms.GroupID, ms.Year, ms.Month), base64.StdEncoding.EncodeToString(compressed.Bytes()))
	return err
}

func (db *DB) monthSnapshot(ctx context.Context, group int64, year, month int) ([]schedule.Lesson, bool, error) {
	raw, err := db.Meta(ctx, monthSnapshotKey(group, year, month))
	if err != nil || raw == "" {
		return nil, false, err
	}
	lessons, err := decodeMonthSnapshot(raw)
	return lessons, true, err
}

func decodeMonthSnapshot(raw string) ([]schedule.Lesson, error) {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var lessons []schedule.Lesson
	err = json.NewDecoder(io.LimitReader(reader, 32<<20)).Decode(&lessons)
	return lessons, err
}
