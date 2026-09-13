package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// Discipline — предмет из справочника локальной копии.
type Discipline struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Discipline находит предмет по идентификатору справочника.
func (db *DB) Discipline(ctx context.Context, id int64) (Discipline, error) {
	var d Discipline
	err := db.r.QueryRowContext(ctx,
		`SELECT id, name FROM disciplines WHERE id = ?`, id).Scan(&d.ID, &d.Name)
	return d, err
}

// DisciplineByName находит предмет по названию.
//
// Занятие несёт название предмета, но не идентификатор справочника: наш
// собственный ключ дисциплины никому за пределами базы не нужен, и тащить его
// в контракт занятия ради одной ссылки дороже, чем один поиск по уникальному
// имени. Ссылка, которой делятся, уже несёт найденный здесь id.
func (db *DB) DisciplineByName(ctx context.Context, name string) (Discipline, error) {
	var d Discipline
	err := db.r.QueryRowContext(ctx,
		`SELECT id, name FROM disciplines WHERE name = ?`, strings.TrimSpace(name)).Scan(&d.ID, &d.Name)
	return d, err
}

// DisciplineLessons отдаёт занятия одного предмета в ленте группы за диапазон.
//
// Отдельный запрос, а не фильтр поверх Lessons: полугодие — это пять месяцев
// ленты, и вычитывать их целиком вместе с преподавателями каждой пары ради
// одного предмета значит прочитать в двадцать раз больше строк, чем нужно.
func (db *DB) DisciplineLessons(ctx context.Context, groupID, disciplineID int64, from, to string) ([]schedule.Lesson, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT l.id, l.date, l.lesson_time_id,
		COALESCE(lt.minute_from,0), COALESCE(lt.minute_to,0), COALESCE(lt.label,''),
		COALESCE(di.name,''), COALESCE(ct.name,''), COALESCE(cr.name,''),
		l.audience, l.subgroup_id, l.audience_label, l.flags, l.comments, l.topic,
		(SELECT json_group_array(name) FROM (SELECT s.name FROM lesson_staff ls
		 JOIN staff s ON s.id=ls.staff_id WHERE ls.lesson_id=l.id ORDER BY ls.pos))
		FROM group_lessons gl
		JOIN lessons l ON l.id = gl.lesson_id
		LEFT JOIN lesson_times lt ON lt.id = l.lesson_time_id
		LEFT JOIN disciplines di ON di.id = l.discipline_id
		LEFT JOIN class_types ct ON ct.id = l.class_type_id
		LEFT JOIN classrooms  cr ON cr.id = l.classroom_id
		WHERE gl.group_id = ? AND gl.date BETWEEN ? AND ? AND l.discipline_id = ?
		ORDER BY gl.date, lt.minute_from, l.id`, groupID, from, to, disciplineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedule.Lesson{}
	for rows.Next() {
		var l schedule.Lesson
		var staff string
		if err := rows.Scan(&l.ID, &l.Date, &l.TimeID, &l.MinuteFrom, &l.MinuteTo, &l.TimeLabel,
			&l.Discipline, &l.ClassType, &l.Classroom, &l.Audience, &l.SubgroupID,
			&l.AudienceLabel, &l.Flags, &l.Comments, &l.Topic, &staff); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(staff), &l.Staff); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
