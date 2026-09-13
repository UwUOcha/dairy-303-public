package store

import (
	"context"
	"database/sql"
	"time"
)

// Учёт тех, кто выбросил бота из диалога.
//
// Блокировку видно двумя путями. Первый — сам собой: рассыльщик получает от
// площадки отказ «этому адресату больше нельзя» и выключает человеку
// уведомления (см. DisableNotify). Путь честный, но однобокий: он срабатывает
// только там, где бот и так собирался написать. Человек, отключивший рассылку
// и потом заблокировавший бота, так и остался бы в базе живым навсегда.
//
// Второй путь — этот: раз в неделю каждого адресата спрашивают у площадки
// напрямую. Отсюда probed_at у каждой записи и выборка по нему.
//
// Почему не одна дата обхода на всех. Тысячи проверок в одну минуту упираются
// в лимиты площадки, а перезапуск botd посреди обхода начинал бы круг заново —
// и при достаточно частых перезапусках хвост базы не проверялся бы никогда.
// Дата у каждого превращает обход в очередь: берём самых давно проверенных,
// сколько влезает в порцию, и следующий заход продолжает с того же места.

// ProbeInterval — как часто перепроверять одного человека.
//
// Неделя — компромисс между свежестью цифры и назойливостью: проверка в
// телеграме стоит адресату мелькнувшего «печатает», и делать это чаще ради
// показателя, на который никто не смотрит ежедневно, незачем.
const ProbeInterval = 7 * 24 * time.Hour

// ProbeTarget — кого проверять и кем он числился до проверки.
//
// Blocked едет вместе с адресатом, чтобы результат проверки можно было
// сравнить с тем, что записано: разблокировался человек или заблокировался —
// это разные события, и узнаются они только в паре «было — стало».
type ProbeTarget struct {
	Platform string `json:"platform"`
	ExtID    string `json:"ext_id"`
	Blocked  bool   `json:"blocked"`
}

// UsersToProbe отдаёт порцию адресатов площадки, которых пора проверить:
// самых давно проверенных первыми.
//
// Только тех, кто довёл настройку до группы. Нажавший «старт» и ушедший в тот
// же день ничего не рассказывает о боте — ни своим уходом, ни своей группой,
// которой у него нет, — а обход из-за таких кратно длиннее.
func (db *DB) UsersToProbe(ctx context.Context, platform string, limit int, now time.Time) ([]ProbeTarget, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	cutoff := now.Add(-ProbeInterval).Unix()
	rows, err := db.r.QueryContext(ctx, `
		SELECT platform, ext_id, blocked_at
		FROM users
		WHERE platform = ? AND group_id > 0 AND probed_at < ?
		ORDER BY probed_at, ext_id
		LIMIT ?`, platform, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProbeTarget
	for rows.Next() {
		var t ProbeTarget
		var blockedAt int64
		if err := rows.Scan(&t.Platform, &t.ExtID, &blockedAt); err != nil {
			return nil, err
		}
		t.Blocked = blockedAt > 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkProbed записывает исход проверки.
//
// Недостижимый адресат получает то же обращение, что и при отказе в рассылке:
// признак блокировки, выключенные уведомления и очищенную очередь. Копить
// сообщения тому, кто их не увидит, незачем.
//
// Достижимый снимает признак — и вместе с ним возвращает себе рассылку. Это
// не самоуправство: выключали её не человек, а мы, ровно из-за блокировки, и
// оставить его молча отписанным после возвращения значило бы наказать за то,
// что он вернулся. Настройки, выключенные им самим (утро, вечер, правки),
// остаются как были — трогается только главный выключатель.
func (db *DB) MarkProbed(ctx context.Context, platform, extID string, reachable bool) error {
	now := time.Now().Unix()
	return db.tx(ctx, func(tx *sql.Tx) error {
		var blockedAt int64
		err := tx.QueryRowContext(ctx,
			`SELECT blocked_at FROM users WHERE platform = ? AND ext_id = ?`,
			platform, extID).Scan(&blockedAt)
		if err == sql.ErrNoRows {
			// Человека успели удалить между выдачей порции и её разбором.
			return nil
		}
		if err != nil {
			return err
		}

		switch {
		case !reachable && blockedAt == 0:
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET blocked_at = ?, notify_on = 0, probed_at = ?
				 WHERE platform = ? AND ext_id = ?`, now, now, platform, extID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx,
				`DELETE FROM outbox WHERE platform = ? AND ext_id = ?`, platform, extID)
			return err

		case reachable && blockedAt > 0:
			_, err = tx.ExecContext(ctx,
				`UPDATE users SET blocked_at = 0, notify_on = 1, probed_at = ?
				 WHERE platform = ? AND ext_id = ?`, now, platform, extID)
			return err

		default:
			_, err = tx.ExecContext(ctx,
				`UPDATE users SET probed_at = ? WHERE platform = ? AND ext_id = ?`,
				now, platform, extID)
			return err
		}
	})
}

// blockedTopLimit — сколько групп показывать в разрезе ушедших.
//
// Меньше, чем в остальных топах: это подпись под одной строкой панели, а не
// самостоятельный рейтинг.
const blockedTopLimit = 5

// blockedStats дополняет статистику пользователей разрезом по заблокировавшим.
//
// Группа берётся текущая, а не запомненная в момент блокировки: заблокировав
// бота, человек больше её не меняет — менять её можно только через диалог,
// которого у него уже нет.
func (db *DB) blockedStats(ctx context.Context, u *UserStats) error {
	// Probed рядом с Blocked нужен, чтобы ноль читался правильно: пока обход
	// не прошёл базу целиком, «никто не заблокировал» и «мы ещё не спрашивали»
	// выглядят одинаково, а это далеко не одно и то же.
	if err := db.r.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(blocked_at > 0), 0),
		        COALESCE(SUM(probed_at > 0 AND group_id > 0), 0)
		 FROM users`).
		Scan(&u.Blocked, &u.Probed); err != nil {
		return err
	}

	var err error
	u.BlockedGroups, err = db.groupCounts(ctx,
		`SELECT g.id, g.name, COALESCE(d.name, ''), g.course, COUNT(*) AS n
		 FROM users u
		 JOIN groups g ON g.id = u.group_id
		 LEFT JOIN departments d ON d.id = g.department_id
		 WHERE u.blocked_at > 0 AND u.group_id > 0
		 GROUP BY g.id ORDER BY n DESC, g.name LIMIT ?`, blockedTopLimit)
	return err
}

// groupCounts читает строки вида «группа — сколько человек».
func (db *DB) groupCounts(ctx context.Context, query string, args ...any) ([]GroupCount, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GroupCount
	for rows.Next() {
		var g GroupCount
		if err := rows.Scan(&g.GroupID, &g.Name, &g.Department, &g.Course, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
