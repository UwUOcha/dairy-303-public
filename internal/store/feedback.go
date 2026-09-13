package store

import (
	"context"
	"database/sql"
	"time"
)

// FeedbackCooldown — сколько человек ждёт между двумя обращениями.
//
// Ограничение не про нагрузку: обращения приходят в личку одному человеку, и
// без паузы любой скучающий первокурсник высыпает туда простыню быстрее, чем
// автор успевает прочитать первое сообщение. Пять минут не мешают дописать
// забытую подробность и делают поток сообщений бессмысленным.
const FeedbackCooldown = 5 * time.Minute

// Feedback — обращение из кнопки «Написать автору».
type Feedback struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	ExtID    string `json:"ext_id"`
	// GroupName — группа автора обращения на момент написания. Пусто, если
	// группы у него ещё нет: как раз такие обращения самые ценные — человек
	// не смог найти себя в каталоге.
	GroupName string    `json:"group_name,omitempty"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	// AnsweredAt — когда автор ответил; нулевое время означает «ещё нет».
	AnsweredAt time.Time `json:"answered_at,omitempty"`
}

// Answered сообщает, отвечено ли на обращение.
func (f Feedback) Answered() bool { return !f.AnsweredAt.IsZero() }

// AddFeedback принимает обращение.
//
// Возвращает сколько ждать, если человек пишет слишком часто: в этом случае
// обращение не сохраняется и id остаётся нулевым. Решение принимается здесь,
// а не в боте, потому что только у базы есть время прошлого обращения —
// botd не хранит состояния между сообщениями вовсе.
func (db *DB) AddFeedback(ctx context.Context, f Feedback) (int64, time.Duration, error) {
	now := time.Now()

	var last int64
	err := db.r.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(created_at), 0) FROM feedback WHERE platform = ? AND ext_id = ?`,
		f.Platform, f.ExtID).Scan(&last)
	if err != nil {
		return 0, 0, err
	}
	if wait := FeedbackCooldown - now.Sub(time.Unix(last, 0)); last > 0 && wait > 0 {
		return 0, wait, nil
	}

	var id int64
	err = db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO feedback (platform, ext_id, group_name, text, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			f.Platform, f.ExtID, f.GroupName, f.Text, now.Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, 0, err
}

// Feedback возвращает обращение по номеру; отсутствие строки — ErrNotFound.
//
// Нужна ради ответа: кнопка «Ответить» живёт в переписке автора сколько
// угодно, а в её callback-данных помещается только номер — пара «платформа +
// ext_id» вместе с префиксом туда уже не влезает.
func (db *DB) Feedback(ctx context.Context, id int64) (Feedback, error) {
	var f Feedback
	var created, answered int64
	err := db.r.QueryRowContext(ctx,
		`SELECT id, platform, ext_id, group_name, text, created_at, answered_at
		   FROM feedback WHERE id = ?`, id).
		Scan(&f.ID, &f.Platform, &f.ExtID, &f.GroupName, &f.Text, &created, &answered)
	if err != nil {
		return f, err
	}
	f.CreatedAt = time.Unix(created, 0)
	if answered > 0 {
		f.AnsweredAt = time.Unix(answered, 0)
	}
	return f, nil
}

// MarkAnswered отмечает, что на обращение ответили.
func (db *DB) MarkAnswered(ctx context.Context, id int64) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE feedback SET answered_at = ? WHERE id = ?`, time.Now().Unix(), id)
		return err
	})
}

// CountUsers считает тех, кто довёл настройку до конца.
//
// Отдельно от Stats: та собирает два десятка срезов по четырём таблицам ради
// панели наблюдения, а здесь нужна одна цифра для экрана «О проекте», куда
// человек может зайти когда угодно.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE group_id > 0`).Scan(&n)
	return n, err
}
