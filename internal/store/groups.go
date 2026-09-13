package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

// ErrNotFound возвращается, когда запрошенной записи нет.
var ErrNotFound = errors.New("store: не найдено")

// Group — группа в том виде, в каком её отдаёт хранилище.
type Group struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	DepartmentID int64  `json:"department_id"`
	Department   string `json:"department,omitempty"`
	Course       int    `json:"course,omitempty"`
	GradYear     int    `json:"grad_year,omitempty"`
	Active       bool   `json:"active"`
}

// Twin — одноимённая запись каталога с признаками, по которым человек может
// узнать свою. Для части групп вуз всё ещё держит записи нынешнего и следующего
// учебного года одновременно (см. shadow.go), и выбор между ними бот делает
// сам; Twin нужен там, где выбор отдаётся человеку.
type Twin struct {
	Group
	// Shadowed — запись спрятана из поиска как дубликат.
	Shadowed bool `json:"shadowed"`
	// HasCurrent — есть занятия начиная с текущего месяца.
	HasCurrent bool `json:"has_current"`
	// LastDate — последний день с занятием, ISO. Пусто, если расписания нет
	// вовсе: такая запись либо ещё не ожила, либо уже отжила своё.
	LastDate string `json:"last_date,omitempty"`
}

// Subgroup — подгруппа группы.
type Subgroup struct {
	ID      int64  `json:"id"`
	GroupID int64  `json:"group_id"`
	Name    string `json:"name"`
}

// Department — подразделение вуза.
type Department struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// NormalizeName приводит название группы к виду, пригодному для поиска:
// нижний регистр без дефисов, пробелов и точек.
//
// Студент помнит свою группу как «Б-Арх-11», но напечатать может «барх11»,
// «б арх 11» или «Б-АРХ-11». Все эти варианты обязаны найти одну и ту же
// строку, поэтому в индекс кладётся уже нормализованная форма.
func NormalizeName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SaveGroupTree перезаписывает каталог групп целиком.
//
// Группы не удаляются, даже если исчезли из ответа: на них могут ссылаться
// пользователи и уже загруженные занятия. Вместо этого пропавшая группа
// помечается неактивной.
func (db *DB) SaveGroupTree(ctx context.Context, tree importdata.GroupTree) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		for _, d := range tree.Departments {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO departments(id, name) VALUES(?, ?)
				 ON CONFLICT(id) DO UPDATE SET name = excluded.name`, d.ID, d.Name); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx, `UPDATE groups SET is_active = 0`); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO groups(id, name, department_id, course, grad_year, name_norm, is_active)
			 VALUES(?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
			   name          = excluded.name,
			   department_id = excluded.department_id,
			   course        = excluded.course,
			   grad_year     = excluded.grad_year,
			   name_norm     = excluded.name_norm,
			   is_active     = excluded.is_active`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, g := range tree.Groups {
			active := 0
			if g.Active {
				active = 1
			}
			if _, err := stmt.ExecContext(ctx, g.ID, g.Name, g.DepartmentID,
				g.Course, g.GradYear, NormalizeName(g.Name), active); err != nil {
				return err
			}
		}
		for _, g := range tree.Groups {
			if _, err := tx.ExecContext(ctx, `UPDATE groups SET shadowed=? WHERE id=?`, g.Hidden, g.ID); err != nil {
				return err
			}
		}
		if err := saveGroupReplacements(ctx, tx, tree.Replacements); err != nil {
			return err
		}
		if err := applyGroupReplacements(ctx, tx); err != nil {
			return err
		}
		return nil
	})
}

// SaveSubgroups обновляет подгруппы одной группы.
func (db *DB) SaveSubgroups(ctx context.Context, groupID int64, subs []importdata.Subgroup) error {
	if len(subs) == 0 {
		return nil
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		for _, s := range subs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO subgroups(id, group_id, name) VALUES(?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET group_id = excluded.group_id, name = excluded.name`,
				s.ID, groupID, s.Name); err != nil {
				return err
			}
		}
		return nil
	})
}

const groupSelect = `
	SELECT g.id, g.name, g.department_id, COALESCE(d.name, ''), g.course, g.grad_year, g.is_active
	FROM groups g LEFT JOIN departments d ON d.id = g.department_id`

func scanGroups(rows *sql.Rows) ([]Group, error) {
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var active int
		if err := rows.Scan(&g.ID, &g.Name, &g.DepartmentID, &g.Department, &g.Course, &g.GradYear, &active); err != nil {
			return nil, err
		}
		g.Active = active == 1
		out = append(out, g)
	}
	return out, rows.Err()
}

// Group возвращает одну группу по id.
func (db *DB) Group(ctx context.Context, id int64) (Group, error) {
	rows, err := db.r.QueryContext(ctx, groupSelect+` WHERE g.id = ?`, id)
	if err != nil {
		return Group{}, err
	}
	gs, err := scanGroups(rows)
	if err != nil {
		return Group{}, err
	}
	if len(gs) == 0 {
		return Group{}, ErrNotFound
	}
	return gs[0], nil
}

// SearchGroups ищет действующие группы по названию.
//
// Совпадения с начала названия идут первыми: набрав «с-лд», студент ждёт
// увидеть «ГР-11», а не группу, у которой эти буквы где-то в середине.
func (db *DB) SearchGroups(ctx context.Context, query string, limit int) ([]Group, error) {
	norm := NormalizeName(query)
	if norm == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.r.QueryContext(ctx, groupSelect+`
		WHERE g.is_active = 1 AND g.shadowed = 0 AND g.name_norm LIKE '%' || ? || '%'
		ORDER BY
		  CASE WHEN g.name_norm LIKE ? || '%' THEN 0 ELSE 1 END,
		  length(g.name_norm),
		  g.name
		LIMIT ?`, norm, norm, limit)
	if err != nil {
		return nil, err
	}
	return scanGroups(rows)
}

// Departments возвращает подразделения, у которых есть действующие группы.
func (db *DB) Departments(ctx context.Context) ([]Department, error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT d.id, d.name FROM departments d
		WHERE EXISTS (SELECT 1 FROM groups g
		              WHERE g.department_id = d.id AND g.is_active = 1 AND g.shadowed = 0)
		ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Department
	for rows.Next() {
		var d Department
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Courses возвращает номера курсов, на которых есть группы подразделения.
func (db *DB) Courses(ctx context.Context, departmentID int64) ([]int, error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT DISTINCT course FROM groups
		WHERE department_id = ? AND is_active = 1 AND shadowed = 0 AND course > 0
		ORDER BY course`, departmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var c int
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GroupsOf возвращает действующие группы подразделения на указанном курсе.
func (db *DB) GroupsOf(ctx context.Context, departmentID int64, course int) ([]Group, error) {
	rows, err := db.r.QueryContext(ctx, groupSelect+`
		WHERE g.department_id = ? AND g.course = ? AND g.is_active = 1 AND g.shadowed = 0
		ORDER BY g.name`, departmentID, course)
	if err != nil {
		return nil, err
	}
	return scanGroups(rows)
}

// Subgroups возвращает подгруппы группы.
func (db *DB) Subgroups(ctx context.Context, groupID int64) ([]Subgroup, error) {
	rows, err := db.r.QueryContext(ctx,
		`SELECT id, group_id, name FROM subgroups WHERE group_id = ?
 AND (NOT EXISTS(SELECT 1 FROM meta WHERE key='active_subgroups:'||subgroups.group_id)
 OR id IN(SELECT value FROM json_each((SELECT value FROM meta WHERE key='active_subgroups:'||subgroups.group_id))))
 ORDER BY name`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subgroup
	for rows.Next() {
		var s Subgroup
		if err := rows.Scan(&s.ID, &s.GroupID, &s.Name); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// FindSubgroup ищет подгруппу по написанному от руки названию.
//
// Студент, которого попросили выбрать подгруппу, пишет «ГР-22/2» — так она
// называется в расписании вуза. Поиск по группам такой запрос не находит
// никогда: нормализация выбрасывает слэш, и от «ГР-22/2» остаётся «гр222»,
// чего нет ни в одном названии группы. Человек при этом всё написал верно.
//
// Отдельного индекса по названиям подгрупп нет и не нужно: имя подгруппы
// всегда начинается с имени своей группы либо состоит из одного номера. Обе
// формы проверяются в Go, а SQL отбирает лишь группы, чьё название оказалось
// началом запроса, — таких единицы.
//
// Порядок кандидатов — от самого длинного совпадения: у «гр222» название
// «гр22» точнее, чем «гр2», если в каталоге найдутся обе группы.
func (db *DB) FindSubgroup(ctx context.Context, query string) (Subgroup, error) {
	norm := NormalizeName(query)
	if norm == "" {
		return Subgroup{}, ErrNotFound
	}
	rows, err := db.r.QueryContext(ctx, `
		SELECT s.id, s.group_id, s.name, g.name_norm
		FROM subgroups s
		JOIN groups g ON g.id = s.group_id
		WHERE g.is_active = 1 AND g.shadowed = 0 AND g.name_norm <> ''
		  AND ? LIKE g.name_norm || '%'
		ORDER BY length(g.name_norm) DESC, s.name`, norm)
	if err != nil {
		return Subgroup{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var s Subgroup
		var groupNorm string
		if err := rows.Scan(&s.ID, &s.GroupID, &s.Name, &groupNorm); err != nil {
			return Subgroup{}, err
		}
		// Название подгруппы вуз пишет и целиком («ГР-22/2»), и одним
		// номером («2»). Во втором случае запрос человека — это название
		// группы с номером на конце, и сравнивать надо со склейкой.
		sub := NormalizeName(s.Name)
		if sub == norm || groupNorm+sub == norm {
			return s, nil
		}
	}
	if err := rows.Err(); err != nil {
		return Subgroup{}, err
	}
	return Subgroup{}, ErrNotFound
}

// GroupTwins возвращает все действующие записи каталога с тем же названием,
// что у указанной группы, — её саму в том числе. Первой идёт та, которую бот
// считает настоящей.
//
// Занятия здесь не «есть или нет», а с датами: человеку, который пришёл
// разбираться, почему расписание пустое, нужно видеть, у какой из копий год
// уже начался, а у какой расписание кончилось прошлой весной.
func (db *DB) GroupTwins(ctx context.Context, groupID int64) ([]Twin, error) {
	since := activeSince(nowFunc())
	rows, err := db.r.QueryContext(ctx, `
		SELECT g.id, g.name, g.department_id, COALESCE(d.name, ''), g.course,
		       g.grad_year, g.is_active, g.shadowed,
		       EXISTS(SELECT 1 FROM group_lessons gl
		              WHERE gl.group_id = g.id AND gl.date >= ?),
		       COALESCE((SELECT MAX(gl.date) FROM group_lessons gl
		                 WHERE gl.group_id = g.id), '')
		FROM groups g LEFT JOIN departments d ON d.id = g.department_id
		WHERE g.is_active = 1
		  AND g.name = (SELECT name FROM groups WHERE id = ?)
		ORDER BY g.shadowed, g.course DESC, g.id`, since, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Twin
	for rows.Next() {
		var t Twin
		var active, shadowed, hasCurrent int
		if err := rows.Scan(&t.ID, &t.Name, &t.DepartmentID, &t.Department, &t.Course,
			&t.GradYear, &active, &shadowed, &hasCurrent, &t.LastDate); err != nil {
			return nil, err
		}
		t.Active = active == 1
		t.Shadowed = shadowed == 1
		t.HasCurrent = hasCurrent == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountGroupTwins считает записи каталога, одноимённые с указанной группой.
// Единица означает, что копий нет и предлагать переключение не о чем.
func (db *DB) CountGroupTwins(ctx context.Context, groupID int64) (int, error) {
	var n int
	err := db.r.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM groups
		WHERE is_active = 1 AND name = (SELECT name FROM groups WHERE id = ?)`,
		groupID).Scan(&n)
	return n, err
}

// ActiveGroupIDs возвращает id всех действующих групп — список для ночного
// полного обхода.
//
// Спрятанные дубликаты сюда намеренно попадают: только обойдя их, мы узнаём,
// что занятий у них нет, — а это и есть свидетельство, на котором держится
// решение прятать (см. shadow.go).
func (db *DB) ActiveGroupIDs(ctx context.Context) ([]int64, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id FROM groups WHERE is_active = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// HotGroupIDs возвращает группы, к которым привязан хотя бы один пользователь.
//
// Это главная оптимизация синка: из 675 групп вуза реальных пользователей
// соберут в лучшем случае несколько десятков, и только их нужно обновлять
// часто.
func (db *DB) HotGroupIDs(ctx context.Context) ([]int64, error) {
	rows, err := db.r.QueryContext(ctx,
		`SELECT DISTINCT group_id FROM users WHERE group_id > 0 ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
