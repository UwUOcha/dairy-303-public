package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

// Виды сообщений в очереди.
const (
	// OutboxChange — расписание группы поправили, надо сказать подписчикам.
	OutboxChange = "change"
	// OutboxFeedback — человек написал автору; сообщение едет автору.
	OutboxFeedback = "feedback"
	// OutboxAnswer — автор ответил; сообщение едет обратно человеку.
	OutboxAnswer = "answer"
)

// OutboxItem — одно сообщение очереди.
type OutboxItem struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	ExtID    string `json:"ext_id"`
	Kind     string `json:"kind"`
	// Payload — JSON с деталями события; для 'change' это ChangePayload.
	Payload  string `json:"payload,omitempty"`
	Attempts int    `json:"attempts"`
}

// EnqueueChange ставит в очередь сообщение об изменении расписания месяца
// всем, кто на эту группу подписан.
//
// Подписка — это главный выключатель уведомлений плюс отдельная галочка
// «сообщать о правках»: человек мог оставить утреннее расписание, но не
// хотеть новостей о каждой перестановке пары. Слать правки тем, кто ничего не
// включал, — верный способ собрать блокировки вместо благодарностей.
//
// Дедупликация через dedup_key: за сутки месяц могут поправить трижды, а
// человеку, который ещё не получил первое сообщение, второе и третье не
// нужны. INSERT OR IGNORE схлопывает их в одну строку — но список задетых дней
// у уже стоящего в очереди сообщения обновляется: схлопывать надо повторы, а
// не новости.
func (db *DB) EnqueueChange(ctx context.Context, groupID int64, year, month int) (int, error) {
	now := time.Now().Unix()
	key := fmt.Sprintf("%d:%04d-%02d", groupID, year, month)

	days, err := db.changedDaysOfMonth(ctx, groupID, year, month)
	if err != nil {
		return 0, err
	}
	p := ChangePayload{GroupID: groupID, Year: year, Month: month, Days: days}
	event, err := db.Meta(ctx, changeEventKey(groupID, year, month))
	if err != nil {
		return 0, err
	}
	if event != "" {
		if strings.HasPrefix(event, "{") {
			p.Before, err = db.changeBeforeFromRevisions(ctx, groupID, event)
			if err != nil {
				return 0, err
			}
		} else if err := json.Unmarshal([]byte(event), &p.Before); err != nil {
			return 0, err // Legacy queued event from before migration.
		}
		p.Personal = true
		st, err := db.MonthState(ctx, groupID, year, month)
		if err != nil {
			return 0, err
		}
		p.Revision = st.Hash
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}

	var n int
	err = db.tx(ctx, func(tx *sql.Tx) error {
		// Сначала — уже стоящим в очереди: человек, не успевший прочитать
		// первое сообщение, должен получить его со всеми днями, а не с теми,
		// что были известны час назад. Попытки и время повтора не трогаем.
		rows, err := tx.QueryContext(ctx, `SELECT id,payload FROM outbox WHERE kind=? AND dedup_key=?`, OutboxChange, key)
		if err != nil {
			return err
		}
		type pending struct {
			id      int64
			payload string
		}
		var updates []pending
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var old ChangePayload
			if err := json.Unmarshal([]byte(raw), &old); err != nil {
				rows.Close()
				return err
			}
			merged := p
			if old.Personal && p.Personal {
				merged.Before = mergeChangeBefore(old.Before, p.Before)
			}
			data, err := json.Marshal(merged)
			if err != nil {
				rows.Close()
				return err
			}
			updates = append(updates, pending{id, string(data)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, u := range updates {
			if _, err := tx.ExecContext(ctx, `UPDATE outbox SET payload=? WHERE id=?`, u.payload, u.id); err != nil {
				return err
			}
		}

		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO outbox(platform, ext_id, kind, dedup_key, payload, next_try_at, created_at)
			SELECT platform, ext_id, ?, ?, ?, ?, ?
			FROM users
			WHERE group_id = ? AND notify_on = 1 AND changes_on = 1`,
			OutboxChange, key, string(payload), now, now, groupID)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		n = int(affected)
		return err
	})
	return n, err
}

// Enqueue кладёт в очередь одно сообщение конкретному адресату.
//
// Обратная связь ходит через ту же очередь, что и новости о правках, по той же
// причине: доставку делает botd, а живёт сообщение в базе raspd, и перезапуск
// любого из двух не должен его терять. Плюс бесплатно достаются ретраи и
// лимиты платформы, которые доставщик и так уважает.
//
// Адресат приезжает параметром, а не вычисляется здесь: кто в этой установке
// автор, знает только botd — у raspd в конфигурации нет ни слова про людей.
func (db *DB) Enqueue(ctx context.Context, platform, extID, kind, dedupKey, payload string) error {
	now := time.Now().Unix()
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO outbox(platform, ext_id, kind, dedup_key, payload, next_try_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			platform, extID, kind, dedupKey, payload, now, now)
		return err
	})
}

// changedDaysOfMonth отдаёт числа месяца, в которых правка что-то поменяла.
//
// Числами, а не датами: год и месяц уже есть в сообщении, а payload лежит в
// очереди отдельной копией у каждого адресата — на пяти тысячах подписчиков
// разница между «15» и «2026-09-15» превращается в мегабайты записи.
//
// Прошедшие дни отсекаются здесь ещё раз: снимок мог пережить свой день, если
// уборщик до него пока не дошёл.
func (db *DB) changedDaysOfMonth(ctx context.Context, groupID int64, year, month int) ([]int, error) {
	from, to := MonthBounds(year, month)
	if today := schedule.FormatDate(nowFunc()); today > from {
		from = today
	}
	rows, err := db.r.QueryContext(ctx,
		`SELECT date FROM change_days
		 WHERE group_id = ? AND date BETWEEN ? AND ? ORDER BY date`, groupID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return nil, err
		}
		t, err := schedule.ParseDate(date)
		if err != nil {
			continue
		}
		out = append(out, t.Day())
	}
	return out, rows.Err()
}

// TakeOutbox отдаёт готовые к отправке сообщения одной платформы.
//
// Отметки «взято в работу» здесь нет намеренно: доставщик на платформу ровно
// один, и повторная выдача возможна только после его падения — а это как раз
// тот случай, когда сообщение надо отправить ещё раз, а не потерять.
func (db *DB) TakeOutbox(ctx context.Context, platform string, limit int) ([]OutboxItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.r.QueryContext(ctx, `
		SELECT id, platform, ext_id, kind, payload, attempts
		FROM outbox
		WHERE platform = ? AND next_try_at <= ?
		ORDER BY next_try_at, id
		LIMIT ?`, platform, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		if err := rows.Scan(&it.ID, &it.Platform, &it.ExtID, &it.Kind, &it.Payload, &it.Attempts); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// OutboxDone убирает доставленное сообщение из очереди.
func (db *DB) OutboxDone(ctx context.Context, id int64, expected ...string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		query := `DELETE FROM outbox WHERE id = ?`
		args := []any{id}
		if len(expected) > 0 {
			query += ` AND payload = ?`
			args = append(args, expected[0])
		}
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// OutboxMaxAttempts — после скольких неудач сообщение выбрасывается.
//
// Новость об изменении расписания живёт недолго: сообщить о ней через неделю
// упорных ретраев хуже, чем не сообщать вовсе.
const OutboxMaxAttempts = 5

// OutboxFail откладывает повторную попытку, а исчерпав их — выбрасывает
// сообщение. Возвращает, осталось ли оно в очереди.
func (db *DB) OutboxFail(ctx context.Context, id int64, after time.Duration) (bool, error) {
	if after <= 0 {
		after = time.Minute
	}
	kept := false
	err := db.tx(ctx, func(tx *sql.Tx) error {
		var attempts int
		err := tx.QueryRowContext(ctx, `SELECT attempts FROM outbox WHERE id = ?`, id).Scan(&attempts)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if attempts+1 >= OutboxMaxAttempts {
			_, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id)
			return err
		}
		kept = true
		_, err = tx.ExecContext(ctx,
			`UPDATE outbox SET attempts = attempts + 1, next_try_at = ? WHERE id = ?`,
			time.Now().Add(after).Unix(), id)
		return err
	})
	return kept, err
}

// PurgeOutbox выбрасывает сообщения, которые уже некому читать: адресат
// отписался, отвязал группу или просто протух вместе с новостью.
func (db *DB) PurgeOutbox(ctx context.Context, olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan).Unix()
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE created_at < ?`, cutoff)
		return err
	})
}

// DropOutboxFor убирает из очереди то, что адресовано конкретному человеку.
//
// Без kinds — всё: так вызывают, когда человек заблокировал бота и следующие
// сообщения ему не дойдут тоже. С перечислением видов — только их: отписка от
// новостей о правках не должна проглатывать личный ответ автора, который в
// той же очереди ждёт своей минуты.
func (db *DB) DropOutboxFor(ctx context.Context, platform, extID string, kinds ...string) error {
	query := `DELETE FROM outbox WHERE platform = ? AND ext_id = ?`
	args := []any{platform, extID}
	if len(kinds) > 0 {
		query += ` AND kind IN (?` + strings.Repeat(`, ?`, len(kinds)-1) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// ChangePayload — тело сообщения об изменении расписания.
type ChangePayload struct {
	Revision string       `json:"revision,omitempty"`
	Personal bool         `json:"personal,omitempty"`
	Before   []ChangedDay `json:"before,omitempty"`
	GroupID  int64        `json:"group_id"`
	Year     int          `json:"year"`
	Month    int          `json:"month"`
	// Days — числа месяца, в которых что-то поменялось. Пусто, если разобрать
	// правку по дням не удалось: сообщение тогда остаётся прежним — «загляни,
	// расписание правили».
	Days []int `json:"days,omitempty"`
}

// Date собирает ISO-дату по числу месяца из этого сообщения.
func (p ChangePayload) Date(day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", p.Year, p.Month, day)
}

// FeedbackPayload — обращение, едущее автору.
//
// Текст лежит прямо здесь, а не читается из таблицы в момент отправки: очередь
// разгребается через минуту после записи, и лишний поход в базу на каждое
// сообщение ничего не экономит. Номер нужен кнопке «Ответить»: она живёт в
// переписке автора вечно и обязана работать спустя месяц.
type FeedbackPayload struct {
	ID        int64  `json:"id"`
	Platform  string `json:"platform"`
	GroupName string `json:"group_name,omitempty"`
	Text      string `json:"text"`
}

// AnswerPayload — ответ автора, едущий обратно человеку.
type AnswerPayload struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

func changeEventKey(group int64, year, month int) string {
	return fmt.Sprintf("change_event:%d:%04d-%02d", group, year, month)
}

// Для ожидающего сообщения сохраняем состояние до самой первой правки.
func mergeChangeBefore(old, next []ChangedDay) []ChangedDay {
	out := append([]ChangedDay(nil), old...)
	seen := map[string]bool{}
	for _, d := range old {
		seen[d.Date] = true
	}
	for _, d := range next {
		if !seen[d.Date] {
			out = append(out, d)
		}
	}
	return out
}
