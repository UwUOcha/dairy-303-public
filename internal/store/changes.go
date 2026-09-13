package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// Совместимость прежних кнопок бота: первый снимок задетого дня остаётся
// базой сравнения на трое суток. Новые записи содержат ссылку на общий архив
// schedule_revisions; JSON-массивы читаются только для данных до миграции.
// Сам архив хранит обе стороны каждой правки 14 дней, включая прошедшие даты.

// maxChangeDays — сколько дней одной правки мы готовы запомнить.
//
// Сохраняем весь месяц: перезаливка не должна терять личные правки в его конце.
const maxChangeDays = 31

// changeDayTTL — сколько живёт снимок дня.
//
// Больше, чем живёт сообщение в очереди (outboxTTL): человек может открыть
// «подробнее» через сутки после того, как получил новость, и кнопка не должна
// упираться в пустоту.
const changeDayTTL = 72 * time.Hour

// ChangedDay — один день, в котором правка что-то поменяла.
type ChangedDay struct {
	Date string `json:"date"`
	// Before — занятия дня до правки; пустой список значит, что занятий не
	// было вовсе.
	Before []schedule.Lesson `json:"before"`
}

// lessonRow — нормализованная строка занятия для сравнения.
//
// Одна на все источники: и на ответ вуза, и на то, что лежит в базе. Иначе
// «изменилось ли расписание» и «какие дни изменились» отвечали бы по разным
// наборам полей, и человек получал бы новость без единого изменённого дня.
//
// Темы занятия здесь намеренно нет: вуз проставляет её задним числом, и для
// студента это не изменение расписания.
func lessonRow(id int64, date string, timeID int64, discipline, classType, classroom string,
	staff []string, subgroupID int64, audience int, audienceLabel string, flags int, comments string) string {
	sorted := make([]string, len(staff))
	copy(sorted, staff)
	// Порядок преподавателей upstream не гарантирует, а перестановка фамилий
	// не должна выглядеть переносом пары.
	sort.Strings(sorted)

	return strings.Join([]string{
		strconv.FormatInt(id, 10),
		date,
		strconv.FormatInt(timeID, 10),
		discipline,
		classType,
		classroom,
		strings.Join(sorted, ","),
		strconv.FormatInt(subgroupID, 10),
		strconv.Itoa(audience),
		audienceLabel,
		strconv.Itoa(flags),
		comments,
	}, "\x1f")
}

func importRow(l importdata.Lesson) string {
	staff := make([]string, 0, len(l.Staff))
	for _, s := range l.Staff {
		staff = append(staff, s.Name)
	}
	return lessonRow(l.ID, l.Date, l.LessonTimeID, l.Discipline, l.ClassType, l.Classroom,
		staff, l.SubgroupID, int(l.Audience), l.AudienceLabel, int(flagsOf(l)), l.Comments)
}

func storedRow(l schedule.Lesson) string {
	return lessonRow(l.ID, l.Date, l.TimeID, l.Discipline, l.ClassType, l.Classroom,
		l.Staff, l.SubgroupID, int(l.Audience), l.AudienceLabel, int(l.Flags), l.Comments)
}

// changedDays сравнивает день за днём то, что лежало в базе, с тем, что
// приехало от вуза, и отдаёт снимки задетых дней.
//
// notBefore отсекает прошедшие дни: правка вчерашней пары новостью не
// является, и открывать её «до/после» человеку незачем.
func changedDays(before []schedule.Lesson, after []importdata.Lesson, notBefore string) []ChangedDay {
	was := map[string][]string{}
	snapshot := map[string][]schedule.Lesson{}
	for _, l := range before {
		was[l.Date] = append(was[l.Date], storedRow(l))
		snapshot[l.Date] = append(snapshot[l.Date], l)
	}
	became := map[string][]string{}
	for _, l := range after {
		became[l.Date] = append(became[l.Date], importRow(l))
	}

	dates := make([]string, 0, len(was)+len(became))
	for date := range was {
		dates = append(dates, date)
	}
	for date := range became {
		if _, ok := was[date]; !ok {
			dates = append(dates, date)
		}
	}
	sort.Strings(dates)

	out := make([]ChangedDay, 0, 4)
	for _, date := range dates {
		if date < notBefore || sameRows(was[date], became[date]) {
			continue
		}
		// Пустой, а не nil: «занятий в этот день не было» — такой же ответ,
		// как и список пар, и в JSON он должен выглядеть списком.
		lessons := snapshot[date]
		if lessons == nil {
			lessons = []schedule.Lesson{}
		}
		out = append(out, ChangedDay{Date: date, Before: lessons})
		if len(out) == maxChangeDays {
			break
		}
	}
	return out
}

// sameRows сравнивает два набора нормализованных строк как множества.
func sameRows(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := make([]string, len(a))
	copy(x, a)
	y := make([]string, len(b))
	copy(y, b)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// saveChangedDays сохраняет ссылки на общий архив для прежнего дневного API.
//
// Повторная правка того же дня снимок не перетирает: «до» обязано остаться
// тем состоянием, которое человек видел последним, иначе после второй правки
// подряд «до/после» показывало бы разницу между двумя правками, а не между
// расписанием, которое он помнит, и нынешним.
func saveChangedDays(ctx context.Context, tx *sql.Tx, groupID int64, days []ChangedDay, now int64) error {
	for _, d := range days {
		date, _ := schedule.ParseDate(d.Date)
		mon := schedule.FormatDate(date.AddDate(0, 0, -(int(date.Weekday())+6)%7))
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM schedule_revisions WHERE group_id=? AND monday=? ORDER BY id DESC LIMIT 1`, groupID, mon).Scan(&id); err != nil {
			return err
		}
		raw, err := json.Marshal(struct {
			ID int64 `json:"revision_id"`
		}{id})
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO change_days(group_id, date, before_json, created_at)
			VALUES(?, ?, ?, ?)
			ON CONFLICT(group_id, date) DO NOTHING`,
			groupID, d.Date, string(raw), now); err != nil {
			return err
		}
	}
	return nil
}

// ChangedDates отдаёт даты, в которых у группы недавно правили расписание,
// начиная с from. Порядок — от ближайшей.
func (db *DB) ChangedDates(ctx context.Context, groupID int64, from string) ([]string, error) {
	rows, err := db.r.QueryContext(ctx,
		`SELECT date FROM change_days WHERE group_id = ? AND date >= ? ORDER BY date`,
		groupID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ChangedBefore отдаёт снимок дня до правки. Отсутствие снимка — ErrNotFound:
// он мог протухнуть, и это нормальный исход, а не поломка.
func (db *DB) ChangedBefore(ctx context.Context, groupID int64, date string) ([]schedule.Lesson, error) {
	var raw string
	err := db.r.QueryRowContext(ctx,
		`SELECT before_json FROM change_days WHERE group_id = ? AND date = ?`,
		groupID, date).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(raw, "{") {
		var ref struct {
			ID int64 `json:"revision_id"`
		}
		if err := json.Unmarshal([]byte(raw), &ref); err != nil {
			return nil, err
		}
		var before string
		err := db.r.QueryRowContext(ctx, `SELECT before_json FROM schedule_revisions WHERE group_id=? AND id=?`, groupID, ref.ID).Scan(&before)
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		var lessons []schedule.Lesson
		if err := json.Unmarshal([]byte(before), &lessons); err != nil {
			return nil, err
		}
		return revisionWeek(lessons, date, date), nil
	}
	var lessons []schedule.Lesson
	if err := json.Unmarshal([]byte(raw), &lessons); err != nil {
		return nil, err
	}
	return lessons, nil
}

// PurgeChangedDays выбрасывает снимки, которые уже некому смотреть:
// протухшие по возрасту и относящиеся к прошедшим дням.
func (db *DB) PurgeChangedDays(ctx context.Context, olderThan time.Duration, today string) error {
	if olderThan <= 0 {
		olderThan = changeDayTTL
	}
	cutoff := nowFunc().Add(-olderThan).Unix()
	return db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_revisions WHERE created_at < ?`, nowFunc().Add(-RevisionTTL).Unix()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE (key LIKE 'change_event:%' OR key LIKE 'month_snapshot:%') AND substr(key,-7) < substr(?,1,7)`, today); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM change_days WHERE created_at < ? OR date < ?`, cutoff, today)
		return err
	})
}
