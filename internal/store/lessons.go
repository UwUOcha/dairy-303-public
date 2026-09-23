package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// MonthState — что мы знаем о состоянии одного месяца одной группы.
type MonthState struct {
	GroupID     int64 `json:"group_id"`
	Year        int   `json:"year"`
	Month       int   `json:"month"`
	Hash        string
	FetchedAt   time.Time `json:"fetched_at"`
	ChangedAt   time.Time `json:"changed_at"`
	LessonCount int       `json:"lesson_count"`
}

// MonthBounds возвращает ISO-границы месяца.
func MonthBounds(year, month int) (from, to string) {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, -1)
	return schedule.FormatDate(start), schedule.FormatDate(end)
}

// ContentHash считает отпечаток месяца по значимым полям занятий.
//
// Отпечаток решает сразу две задачи: не переписывать базу, когда ничего не
// изменилось, и инвалидировать кэш будущих картинок. Поля берутся только те,
// что видит пользователь, — иначе служебные изменения на стороне вуза давали
// бы ложные срабатывания.
//
// Третью задачу — «сказать ли подписчикам, что расписание правили» — решает
// отдельный отпечаток по будущей части месяца, см. lessonsFrom и SaveMonth.
func ContentHash(lessons []importdata.Lesson) string {
	rows := make([]string, 0, len(lessons))
	for _, l := range lessons {
		rows = append(rows, importRow(l))
	}
	// Порядок занятий в ответе upstream не гарантирован, а отпечаток обязан
	// быть устойчивым.
	sort.Strings(rows)

	h := sha256.New()
	for _, r := range rows {
		h.Write([]byte(r))
		h.Write([]byte{'\x1e'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func flagsOf(l importdata.Lesson) schedule.Flags {
	var f schedule.Flags
	if l.IsEmpty {
		f |= schedule.FlagEmpty
	}
	if l.SelfWork {
		f |= schedule.FlagSelfWork
	}
	if l.Remote {
		f |= schedule.FlagRemote
	}
	if l.NonStudy {
		f |= schedule.FlagNonStudy
	}
	return f
}

// lessonsFrom оставляет занятия с даты from включительно. Пустая from —
// «все», такой границы не было.
func lessonsFrom(lessons []importdata.Lesson, from string) []importdata.Lesson {
	if from == "" {
		return lessons
	}
	out := make([]importdata.Lesson, 0, len(lessons))
	for _, l := range lessons {
		// ISO-даты сравниваются как строки — это и есть их порядок.
		if l.Date >= from {
			out = append(out, l)
		}
	}
	return out
}

// SaveMonth записывает расписание группы за месяц одной транзакцией и
// сообщает, стоит ли рассказать подписчикам о правке.
//
// Транзакция на (группа, месяц) — не перестраховка: без неё возможен месяц,
// у которого старые занятия уже удалены, а новые ещё не вставлены, и
// пользователь в этот момент увидит пустой день.
func (db *DB) SaveMonth(ctx context.Context, ms importdata.MonthSchedule) (changed bool, err error) {
	hash := ContentHash(ms.Lessons)
	now := time.Now().Unix()
	if ms.FetchedAt != "" {
		stamp, err := time.Parse(time.RFC3339, ms.FetchedAt)
		if err != nil {
			return false, err
		}
		now = stamp.Unix()
	}

	// Граница, за которой правка ещё чего-то стоит. Вуз постоянно правит
	// прошедшие дни — проставляет тему, меняет аудиторию задним числом, — и
	// сообщать об этом человеку незачем: он не пойдёт на пару, которая была
	// две недели назад.
	//
	// Часовой пояс здесь серверный, а не вузовский, и это осознанно: UTC
	// отстаёт от вуза, поэтому граница может оказаться на день раньше нужной,
	// но никогда не позже. Ошибка в эту сторону безобидна — лишнее сообщение
	// в узком окне у полуночи, — а в обратную мы бы молча съедали правки
	// сегодняшнего дня.
	today := schedule.FormatDate(nowFunc())

	var prevHash, prevFuture, prevFrom string
	var previousFetched int64
	err = db.r.QueryRowContext(ctx,
		`SELECT content_hash, future_hash, future_from, fetched_at
		 FROM month_state WHERE group_id = ? AND year = ? AND month = ?`,
		ms.GroupID, ms.Year, ms.Month).Scan(&prevHash, &prevFuture, &prevFrom, &previousFetched)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	if ms.FetchedAt != "" && previousFetched > now {
		return false, fmt.Errorf("provider snapshot is older than saved data")
	}
	// Прошлый отпечаток будущего считался по своей границе, и сравнивать с ним
	// надо по ней же. Иначе съехавшее за ночь окно само по себе выглядело бы
	// изменением, и каждое утро вся база получала бы ложную новость о правке.
	notify := prevHash != "" && prevFuture != ContentHash(lessonsFrom(ms.Lessons, prevFrom))
	if ms.BaselineKey != "" {
		var marker string
		e := db.r.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, ms.BaselineKey).Scan(&marker)
		if e == sql.ErrNoRows {
			notify = false
		} else if e != nil {
			return false, e
		}
	}
	// А записываем уже по сегодняшней границе: окно едет вперёд с каждым
	// успешным запросом, а не только с правкой.
	future := ContentHash(lessonsFrom(ms.Lessons, today))

	if prevHash == hash {
		// Содержимое занятий то же — трогаем отметку о свежести и двигаем
		// окно. Сетку звонков всё равно сохраняем: вуз может поменять время,
		// оставив прежними идентификаторы слотов и сами занятия.
		return false, db.tx(ctx, func(tx *sql.Tx) error {
			// Rebind identities even when the schedule hash is unchanged. This
			// repairs legacy homonyms on the next ordinary scheduled fetch.
			d := newDicts(ctx, tx)
			for _, l := range ms.Lessons {
				if err := saveLessonStaff(ctx, tx, d, l.ID, l.Staff); err != nil {
					return err
				}
			}
			if err := saveLessonTimes(ctx, tx, ms.LessonTimes); err != nil {
				return err
			}
			if err := saveMonthSnapshot(ctx, tx, ms, nil, false); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`UPDATE month_state SET fetched_at = ?, future_hash = ?, future_from = ?
				 WHERE group_id = ? AND year = ? AND month = ?`,
				now, future, today, ms.GroupID, ms.Year, ms.Month)
			return err
		})
	}

	from, to := MonthBounds(ms.Year, ms.Month)

	// Что именно поправили. Отпечаток на этот вопрос не отвечает — он
	// необратим, — поэтому дни сравниваются поимённо, а от задетых
	// откладывается снимок «до»: из него собирается «подробнее» под новостью.
	//
	// Лишний запрос здесь только на настоящей правке, то есть на месяце,
	// который всё равно переписывается целиком. Читать надо до транзакции:
	// внутри неё старых занятий уже нет.
	var touched []ChangedDay
	if notify {
		// Граница прошлого синка могла лечь раньше самого месяца: сентябрь
		// первый раз загружают в августе. Тогда выборка «до» захватила бы
		// августовские занятия, которых в сентябрьском ответе вуза нет по
		// определению, и весь конец августа объявился бы отменённым.
		window := prevFrom
		if window < from {
			window = from
		}
		before, known, err := db.monthSnapshot(ctx, ms.GroupID, ms.Year, ms.Month)
		if err != nil {
			return false, err
		}
		if !known {
			before, err = db.Lessons(ctx, ms.GroupID, window, to)
			if err != nil {
				return false, err
			}
		}
		visible := before[:0]
		for _, l := range before {
			if l.Date >= window && l.Date <= to {
				visible = append(visible, l)
			}
		}
		before = visible
		touched = changedDays(before, snapshotLessons(ms), today)
		// Граница предыдущего отпечатка могла устареть за ночь. Сообщать
		// стоит только о правках, которые ещё затрагивают сегодня или будущее.
		notify = len(touched) > 0
	}

	err = db.tx(ctx, func(tx *sql.Tx) error {
		if err := saveLessonTimes(ctx, tx, ms.LessonTimes); err != nil {
			return err
		}

		// Занятия, которые лента группы содержала до этого обновления: часть
		// из них может остаться без единой ссылки и подлежит уборке.
		orphanCandidates, err := lessonIDsInRange(ctx, tx, ms.GroupID, from, to)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM group_lessons WHERE group_id = ? AND date BETWEEN ? AND ?`,
			ms.GroupID, from, to); err != nil {
			return err
		}

		d := newDicts(ctx, tx)
		for _, l := range ms.Lessons {
			if err := insertLesson(ctx, tx, d, ms.GroupID, l); err != nil {
				return fmt.Errorf("занятие %d: %w", l.ID, err)
			}
		}

		if err := collectGarbage(ctx, tx, orphanCandidates); err != nil {
			return err
		}
		// Появление или пропажа занятий меняет расклад между одноимёнными
		// группами: пустышка, у которой начался учебный год, должна перестать
		// быть спрятанной.
		if err := saveMonthSnapshot(ctx, tx, ms, touched, notify); err != nil {
			return err
		}
		if err := saveChangedDays(ctx, tx, ms.GroupID, touched, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO month_state(group_id, year, month, content_hash, future_hash, future_from,
			                         fetched_at, changed_at, lesson_count)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(group_id, year, month) DO UPDATE SET
			   content_hash = excluded.content_hash,
			   future_hash  = excluded.future_hash,
			   future_from  = excluded.future_from,
			   fetched_at   = excluded.fetched_at,
			   changed_at   = excluded.changed_at,
			   lesson_count = excluded.lesson_count`,
			ms.GroupID, ms.Year, ms.Month, hash, future, today, now, now, len(ms.Lessons))
		return err
	})
	if err != nil {
		return false, err
	}
	// Первая загрузка месяца — не «изменение расписания»: подписчикам о ней
	// сообщать нечего. Правка целиком в прошедших днях — тоже.
	return notify, nil
}

func saveLessonTimes(ctx context.Context, tx *sql.Tx, times []importdata.LessonTime) error {
	for _, t := range times {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO lesson_times(id, minute_from, minute_to, label) VALUES(?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
			   minute_from = excluded.minute_from,
			   minute_to   = excluded.minute_to,
			   label       = excluded.label`,
			t.ID, t.MinuteFrom, t.MinuteTo, t.Label); err != nil {
			return err
		}
	}
	return nil
}

func lessonIDsInRange(ctx context.Context, tx *sql.Tx, groupID int64, from, to string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT lesson_id FROM group_lessons WHERE group_id = ? AND date BETWEEN ? AND ?`,
		groupID, from, to)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func insertLesson(ctx context.Context, tx *sql.Tx, d *dicts, groupID int64, l importdata.Lesson) error {
	discipline, err := d.id("disciplines", l.Discipline)
	if err != nil {
		return err
	}
	classType, err := d.id("class_types", l.ClassType)
	if err != nil {
		return err
	}
	classroom, err := d.id("classrooms", l.Classroom)
	if err != nil {
		return err
	}

	superflow := ""
	if len(l.SuperflowGroupIDs) > 0 || len(l.SuperflowSubgroupIDs) > 0 {
		b, err := json.Marshal(map[string][]int64{
			"groups":    l.SuperflowGroupIDs,
			"subgroups": l.SuperflowSubgroupIDs,
		})
		if err != nil {
			return err
		}
		superflow = string(b)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO lessons(id, date, lesson_time_id, discipline_id, class_type_id, classroom_id,
		                     audience, anchor_group_id, subgroup_id, flow_number, audience_label,
		                     flags, comments, topic, superflow)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   date            = excluded.date,
		   lesson_time_id  = excluded.lesson_time_id,
		   discipline_id   = excluded.discipline_id,
		   class_type_id   = excluded.class_type_id,
		   classroom_id    = excluded.classroom_id,
		   audience        = excluded.audience,
		   anchor_group_id = excluded.anchor_group_id,
		   subgroup_id     = excluded.subgroup_id,
		   flow_number     = excluded.flow_number,
		   audience_label  = excluded.audience_label,
		   flags           = excluded.flags,
		   comments        = excluded.comments,
		   topic           = excluded.topic,
		   superflow       = excluded.superflow`,
		l.ID, l.Date, l.LessonTimeID, discipline, classType, classroom,
		int(l.Audience), l.GroupID, l.SubgroupID, l.FlowNumber, l.AudienceLabel,
		int(flagsOf(l)), l.Comments, l.Topic, superflow); err != nil {
		return err
	}

	// Дата занятия одна на все ленты, в которых оно лежит. Потоковую пару
	// видят несколько групп, а синхронизируются они порознь: если пару
	// перенесли, у ещё не обновлённых групп в group_lessons осталась бы старая
	// дата. Выборка идёт по ней, а BuildDay сверяет её с датой самого занятия
	// и расхождение отбрасывает — пара пропала бы у них со старого дня и не
	// появилась на новом. Поэтому дату правим сразу во всех лентах.
	if _, err := tx.ExecContext(ctx,
		`UPDATE group_lessons SET date = ? WHERE lesson_id = ? AND date <> ?`,
		l.Date, l.ID, l.Date); err != nil {
		return err
	}

	if err := saveLessonStaff(ctx, tx, d, l.ID, l.Staff); err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO group_lessons(group_id, lesson_id, date) VALUES(?, ?, ?)
		 ON CONFLICT(group_id, lesson_id) DO UPDATE SET date = excluded.date`,
		groupID, l.ID, l.Date)
	return err
}

func saveLessonStaff(ctx context.Context, tx *sql.Tx, d *dicts, lessonID int64, staff []importdata.Staff) error {
	// Состав преподавателей мог измениться — переписываем целиком.
	if _, err := tx.ExecContext(ctx, `DELETE FROM lesson_staff WHERE lesson_id = ?`, lessonID); err != nil {
		return err
	}
	for i, s := range staff {
		staffID, err := d.staffID(s, lessonID, i)
		if err != nil {
			return err
		}
		if !staffID.Valid {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO lesson_staff(lesson_id, staff_id, pos) VALUES(?, ?, ?)
			 ON CONFLICT(lesson_id, staff_id) DO UPDATE SET pos = MIN(pos, excluded.pos)`,
			lessonID, staffID.Int64, i); err != nil {
			return err
		}
	}

	return nil
}

// collectGarbage удаляет занятия, на которые больше не ссылается ни одна лента.
//
// Проверять всю таблицу занятий не нужно и дорого: осиротеть могли только те
// строки, что мы сами только что отцепили от группы.
func collectGarbage(ctx context.Context, tx *sql.Tx, candidates []int64) error {
	const chunk = 400
	for start := 0; start < len(candidates); start += chunk {
		end := min(start+chunk, len(candidates))
		batch := candidates[start:end]

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM lessons WHERE id IN (%s)
			 AND NOT EXISTS (SELECT 1 FROM group_lessons gl WHERE gl.lesson_id = lessons.id)`,
			placeholders), args...); err != nil {
			return err
		}
	}
	return nil
}

// dicts интернирует строки справочников в рамках одной транзакции.
type dicts struct {
	ctx   context.Context
	tx    *sql.Tx
	cache map[string]int64
}

func newDicts(ctx context.Context, tx *sql.Tx) *dicts {
	return &dicts{ctx: ctx, tx: tx, cache: make(map[string]int64)}
}

// id возвращает идентификатор строки справочника, добавляя её при
// необходимости. Пустое имя даёт NULL: «аудитория не указана» — это
// отсутствие значения, а не справочник с пустой строкой.
func (d *dicts) id(table, name string) (sql.NullInt64, error) {
	if name == "" {
		return sql.NullInt64{}, nil
	}
	key := table + "\x00" + name
	if id, ok := d.cache[key]; ok {
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	// Имя таблицы — константа из кода, а не пользовательский ввод.
	if _, err := d.tx.ExecContext(d.ctx,
		fmt.Sprintf(`INSERT OR IGNORE INTO %s(name) VALUES(?)`, table), name); err != nil {
		return sql.NullInt64{}, err
	}
	var id int64
	if err := d.tx.QueryRowContext(d.ctx,
		fmt.Sprintf(`SELECT id FROM %s WHERE name = ?`, table), name).Scan(&id); err != nil {
		return sql.NullInt64{}, err
	}
	d.cache[key] = id
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

// Lessons отдаёт занятия ленты группы за диапазон дат включительно.
func (db *DB) Lessons(ctx context.Context, groupID int64, from, to string) ([]schedule.Lesson, error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT l.id, l.date, l.lesson_time_id,
		       COALESCE(lt.minute_from, 0), COALESCE(lt.minute_to, 0), COALESCE(lt.label, ''),
		       COALESCE(di.name, ''), COALESCE(ct.name, ''), COALESCE(cr.name, ''),
		       l.audience, l.subgroup_id, l.audience_label, l.flags, l.comments, l.topic
		FROM group_lessons gl
		JOIN lessons l           ON l.id  = gl.lesson_id
		LEFT JOIN lesson_times lt ON lt.id = l.lesson_time_id
		LEFT JOIN disciplines di  ON di.id = l.discipline_id
		LEFT JOIN class_types ct  ON ct.id = l.class_type_id
		LEFT JOIN classrooms  cr  ON cr.id = l.classroom_id
		WHERE gl.group_id = ? AND gl.date BETWEEN ? AND ?
		ORDER BY gl.date, lt.minute_from, l.id`, groupID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []schedule.Lesson
	index := make(map[int64]int)
	for rows.Next() {
		var l schedule.Lesson
		var audience int
		var flags int64
		if err := rows.Scan(&l.ID, &l.Date, &l.TimeID,
			&l.MinuteFrom, &l.MinuteTo, &l.TimeLabel,
			&l.Discipline, &l.ClassType, &l.Classroom,
			&audience, &l.SubgroupID, &l.AudienceLabel, &flags, &l.Comments, &l.Topic); err != nil {
			return nil, err
		}
		l.Audience = schedule.Audience(audience)
		l.Flags = schedule.Flags(flags)
		index[l.ID] = len(out)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, db.attachStaff(ctx, groupID, from, to, out, index)
}

// attachStaff доклеивает преподавателей отдельным запросом.
//
// Через group_concat это был бы один запрос, но порядок внутри склейки SQLite
// не гарантирует, а у пары с четырьмя преподавателями перестановка фамилий
// между обновлениями выглядела бы как изменение расписания.
func (db *DB) attachStaff(ctx context.Context, groupID int64, from, to string, lessons []schedule.Lesson, index map[int64]int) error {
	rows, err := db.r.QueryContext(ctx, `
		SELECT ls.lesson_id, s.name
		FROM group_lessons gl
		JOIN lesson_staff ls ON ls.lesson_id = gl.lesson_id
		JOIN staff s         ON s.id = ls.staff_id
		WHERE gl.group_id = ? AND gl.date BETWEEN ? AND ?
		ORDER BY ls.lesson_id, ls.pos`, groupID, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		if i, ok := index[id]; ok {
			lessons[i].Staff = append(lessons[i].Staff, name)
		}
	}
	return rows.Err()
}

// Grid отдаёт сетку звонков вуза и признак учебных дней недели.
func (db *DB) Grid(ctx context.Context) (schedule.Grid, error) {
	rows, err := db.r.QueryContext(ctx,
		`SELECT t.id, minute_from, minute_to, label, m.value FROM lesson_times t LEFT JOIN meta m ON m.key='slot_number:'||t.id ORDER BY minute_from,t.id`)
	if err != nil {
		return schedule.Grid{}, err
	}
	defer rows.Close()

	g := schedule.Grid{}
	for rows.Next() {
		var t schedule.GridTime
		var number sql.NullString
		if err := rows.Scan(&t.ID, &t.MinuteFrom, &t.MinuteTo, &t.Label, &number); err != nil {
			return g, err
		}
		t.Number = len(g.Times) + 1
		if number.Valid {
			t.Number, _ = strconv.Atoi(number.String)
		}
		g.Times = append(g.Times, t)
	}
	if err := rows.Err(); err != nil {
		return g, err
	}

	// Учебные дни недели upstream отдаёт в каждом ответе по расписанию и они
	// одинаковы для всего вуза, поэтому хранятся в meta одной строкой.
	raw, err := db.Meta(ctx, MetaWorkdays)
	if err != nil {
		return g, err
	}
	if raw != "" {
		g.Workdays = map[int]bool{}
		for _, part := range strings.Split(raw, ",") {
			if wd, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && wd >= 1 && wd <= 7 {
				g.Workdays[wd] = true
			}
		}
	}
	return g, nil
}

// Ключи таблицы meta.
const (
	// MetaWorkdays — учебные дни недели, "1,2,3,4,5,6".
	MetaWorkdays = "workdays"
	// MetaGroupsSyncedAt — unix ts последнего обновления дерева групп.
	MetaGroupsSyncedAt = "groups_synced_at"
	// MetaFullSyncAt — unix ts последнего завершённого полного обхода.
	MetaFullSyncAt = "full_sync_at"
	// MetaHotSyncAt — unix ts последнего прохода по «горячим» группам.
	MetaHotSyncAt = "hot_sync_at"
)

// SaveWorkdays запоминает учебные дни недели.
func (db *DB) SaveWorkdays(ctx context.Context, workdays map[int][]int64) error {
	if len(workdays) == 0 {
		return nil
	}
	days := make([]string, 0, len(workdays))
	for wd := range workdays {
		days = append(days, strconv.Itoa(wd))
	}
	sort.Strings(days)
	return db.SetMeta(ctx, MetaWorkdays, strings.Join(days, ","))
}

// MonthState возвращает состояние месяца; отсутствие записи — ErrNotFound.
func (db *DB) MonthState(ctx context.Context, groupID int64, year, month int) (MonthState, error) {
	var s MonthState
	var fetched, changed int64
	err := db.r.QueryRowContext(ctx,
		`SELECT group_id, year, month, content_hash, fetched_at, changed_at, lesson_count
		 FROM month_state WHERE group_id = ? AND year = ? AND month = ?`,
		groupID, year, month).Scan(&s.GroupID, &s.Year, &s.Month, &s.Hash, &fetched, &changed, &s.LessonCount)
	if err == sql.ErrNoRows {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	s.FetchedAt = time.Unix(fetched, 0)
	s.ChangedAt = time.Unix(changed, 0)
	return s, nil
}

// FreshnessOf возвращает время самой старой загрузки среди перечисленных
// месяцев. Нулевое время означает, что хотя бы один месяц не загружен.
func (db *DB) FreshnessOf(ctx context.Context, groupID int64, months [][2]int) (time.Time, error) {
	var oldest time.Time
	for _, ym := range months {
		st, err := db.MonthState(ctx, groupID, ym[0], ym[1])
		if err == ErrNotFound {
			return time.Time{}, nil
		}
		if err != nil {
			return oldest, err
		}
		if oldest.IsZero() || st.FetchedAt.Before(oldest) {
			oldest = st.FetchedAt
		}
	}
	return oldest, nil
}

// GridForGroup avoids applying another group's workdays to this schedule.
func (db *DB) GridForGroup(ctx context.Context, group int64) (schedule.Grid, error) {
	g, e := db.Grid(ctx)
	if e != nil {
		return g, e
	}
	raw, e := db.Meta(ctx, fmt.Sprintf("group_workdays:%d", group))
	if e != nil {
		return g, e
	}
	if raw != "" {
		var days map[int][]int64
		if e = json.Unmarshal([]byte(raw), &days); e != nil {
			return g, e
		}
		g.Workdays = map[int]bool{}
		for d := range days {
			g.Workdays[d] = true
		}
	}
	return g, nil
}
