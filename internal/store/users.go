package store

import (
	"context"
	"database/sql"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"time"
)

// Значения настроек уведомлений по умолчанию.
//
// Утро включено вместе с главным выключателем, вечер — нет: расписание на
// завтра нужно не всем, а два сообщения в день от бота, которого человек
// только что завёл, читаются как спам.
const (
	DefaultMorningAt = 7 * 60     // 07:00
	DefaultEveningAt = 18*60 + 30 // 18:30
)

// Чего бот ждёт от человека текстом.
//
// Это единственное состояние диалога во всей системе, и живёт оно в записи
// пользователя, а не в памяти процесса: перезапуск botd не должен ронять
// человека на середине ввода. Всё остальное по-прежнему кодируется в
// callback-данных кнопок — время просто нельзя выбрать кнопкой, не превратив
// клавиатуру в циферблат из тридцати штук.
const (
	AwaitNothing     = ""
	AwaitMorningTime = "tm" // ждём время утренней рассылки
	AwaitEveningTime = "te" // ждём время вечерней рассылки
	// AwaitFeedback — ждём текст обращения к автору.
	AwaitFeedback = "fb"
	// AwaitAnswer — ждём ответ автора на обращение; хвостом едет его номер:
	// «an:42». Второе состояние с параметром и единственное, которое видит
	// один человек на всю установку.
	AwaitAnswer = "an"
)

// User — привязка пользователя платформы к группе и его настройки.
type User struct {
	Platform   string `json:"platform"`
	ExtID      string `json:"ext_id"`
	GroupID    int64  `json:"group_id"`
	SubgroupID int64  `json:"subgroup_id"`
	// TZOffset — смещение часового пояса в минутах от UTC. Нужно для
	// «что сейчас» и для рассылок.
	TZOffset int    `json:"tz_offset"`
	Timezone string `json:"timezone,omitempty"`

	// Notify — главный выключатель: выключен, значит бот молчит совсем.
	// Раньше эту роль играло notify_at = NULL, и выключить одну только
	// утреннюю рассылку, оставив новости о правках, было нельзя.
	Notify bool `json:"notify"`
	// Morning — слать расписание на сегодня; MorningAt — во сколько,
	// в минутах от полуночи по местному времени человека.
	Morning   bool `json:"morning"`
	MorningAt int  `json:"morning_at"`
	// Evening — слать расписание на следующий учебный день.
	Evening   bool `json:"evening"`
	EveningAt int  `json:"evening_at"`
	// EmptyDays — писать и в те дни, когда занятий нет.
	EmptyDays bool `json:"empty_days"`
	// Changes — сообщать, что вуз поправил расписание группы.
	Changes bool `json:"changes"`

	// EmptyHinted — человеку уже показали разовую подсказку о том, что
	// уведомления о пустых днях выключены. Ровно один раз за всю жизнь
	// записи: смысл подсказки в том, чтобы молчание бота не выглядело
	// поломкой, а не в том, чтобы напоминать о настройке каждую субботу.
	EmptyHinted bool `json:"empty_hinted,omitempty"`
	// NotifiedOn и NotifiedEveOn — локальные даты последних рассылок, ISO.
	NotifiedOn    string `json:"notified_on,omitempty"`
	NotifiedEveOn string `json:"notified_eve_on,omitempty"`
	// Await — чего бот ждёт от человека текстом; см. Await*-константы.
	Await string `json:"await,omitempty"`
	// MenuVersion — версия успешно доставленной постоянной клавиатуры.
	MenuVersion string `json:"menu_version,omitempty"`
	// MenuSent — прежний флаг показа, сохраняется для совместимости.
	MenuSent  bool      `json:"menu_sent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

// Configured сообщает, довёл ли пользователь настройку до конца.
func (u User) Configured() bool { return u.GroupID > 0 }

// WithDefaults проставляет настройки уведомлений незнакомому человеку.
//
// Нужна потому, что нулевое значение структуры значит «всё выключено», а
// новичок должен получать утреннее расписание, ничего не настраивая. В базе
// те же значения стоят как DEFAULT колонок — здесь они повторены для записи,
// которой в базе ещё нет.
func (u User) WithDefaults(tzOffset int) User {
	u.TZOffset = tzOffset
	if tzOffset == profile.Current().Offset() {
		u.Timezone = profile.Current().Timezone
	}
	u.Notify = true
	u.Morning = true
	u.MorningAt = DefaultMorningAt
	u.Evening = false
	u.EveningAt = DefaultEveningAt
	u.EmptyDays = false
	u.Changes = true
	return u
}

// userColumns — порядок колонок, который ждёт scanUser.
const userColumns = `platform, ext_id, group_id, subgroup_id, tz_offset, tz_name,
	        notify_on, morning_on, morning_at, evening_on, evening_at,
	        empty_on, changes_on, empty_hinted, notified_on, notified_eve_on,
	        await, menu_sent, menu_version, created_at, last_seen`

// rowScanner — общее у *sql.Row и *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanUser читает строку в порядке userColumns; extra дочитывает колонки,
// дописанные запросом после них.
func scanUser(s rowScanner, u *User, extra ...any) error {
	var notify, morning, evening, empty, changes, hinted, menuSent int
	var created, seen int64
	dest := []any{
		&u.Platform, &u.ExtID, &u.GroupID, &u.SubgroupID, &u.TZOffset, &u.Timezone,
		&notify, &morning, &u.MorningAt, &evening, &u.EveningAt,
		&empty, &changes, &hinted, &u.NotifiedOn, &u.NotifiedEveOn,
		&u.Await, &menuSent, &u.MenuVersion, &created, &seen,
	}
	if err := s.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	if u.Timezone != "" {
		if loc, e := time.LoadLocation(u.Timezone); e == nil {
			_, off := time.Now().In(loc).Zone()
			u.TZOffset = off / 60
		}
	}
	u.Notify = notify == 1
	u.Morning = morning == 1
	u.Evening = evening == 1
	u.EmptyDays = empty == 1
	u.Changes = changes == 1
	u.EmptyHinted = hinted == 1
	u.MenuSent = menuSent == 1
	u.CreatedAt = time.Unix(created, 0)
	u.LastSeen = time.Unix(seen, 0)
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// User возвращает пользователя; отсутствие записи — ErrNotFound.
func (db *DB) User(ctx context.Context, platform, extID string) (User, error) {
	var u User
	row := db.r.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE platform = ? AND ext_id = ?`, platform, extID)
	err := scanUser(row, &u)
	if err == sql.ErrNoRows {
		return u, ErrNotFound
	}
	return u, err
}

// SaveUser создаёт или обновляет пользователя, сохраняя исходный created_at.
//
// Отметки о состоявшихся рассылках (notified_on, notified_eve_on,
// empty_hinted) при обновлении не трогаются намеренно: их ставит рассыльщик,
// а сюда приезжает снимок записи, прочитанный до его работы. Перезапись
// снимком заставила бы бота написать человеку второй раз. Версию доставленного
// меню также меняет только MarkMenuSent, а не сохранение настроек.
func (db *DB) SaveUser(ctx context.Context, u User) error {
	now := time.Now().Unix()
	return db.tx(ctx, func(tx *sql.Tx) error {
		// Новости о правках адресованы группе, которая была выбрана в момент
		// постановки сообщения в очередь. После переезда они становятся
		// ложными, поэтому смену группы и очистку очереди делаем одной
		// транзакцией.
		var previousGroupID int64
		err := tx.QueryRowContext(ctx,
			`SELECT group_id FROM users WHERE platform = ? AND ext_id = ?`,
			u.Platform, u.ExtID).Scan(&previousGroupID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && previousGroupID != u.GroupID {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM outbox WHERE platform = ? AND ext_id = ?`, u.Platform, u.ExtID); err != nil {
				return err
			}
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO users(platform, ext_id, group_id, subgroup_id, tz_offset, tz_name,
			                   notify_on, morning_on, morning_at, evening_on, evening_at,
			                   empty_on, changes_on, empty_hinted, await, menu_sent,
			                   created_at, last_seen)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(platform, ext_id) DO UPDATE SET
			   group_id    = excluded.group_id,
			   subgroup_id = excluded.subgroup_id,
			   tz_offset   = excluded.tz_offset,
 tz_name = excluded.tz_name,
			   notify_on   = excluded.notify_on,
			   morning_on  = excluded.morning_on,
			   morning_at  = excluded.morning_at,
			   evening_on  = excluded.evening_on,
			   evening_at  = excluded.evening_at,
			   empty_on    = excluded.empty_on,
			   changes_on  = excluded.changes_on,
			   await       = excluded.await,
			   last_seen   = excluded.last_seen`,
			u.Platform, u.ExtID, u.GroupID, u.SubgroupID, u.TZOffset, u.Timezone,
			b2i(u.Notify), b2i(u.Morning), u.MorningAt, b2i(u.Evening), u.EveningAt,
			b2i(u.EmptyDays), b2i(u.Changes), b2i(u.EmptyHinted), u.Await, b2i(u.MenuSent),
			now, now)
		return err
	})
}

// NotifyKind — какая из двух рассылок наступила.
type NotifyKind string

const (
	// NotifyMorning — расписание на сегодня.
	NotifyMorning NotifyKind = "morning"
	// NotifyEvening — расписание на следующий учебный день.
	NotifyEvening NotifyKind = "evening"
)

// NotifyTarget — кому и что пора отправить.
type NotifyTarget struct {
	User
	Kind NotifyKind `json:"kind"`
}

// Done сообщает, что за этот локальный день человеку уже писали.
func (t NotifyTarget) Done(localDate string) bool {
	if t.Kind == NotifyEvening {
		return t.NotifiedEveOn == localDate
	}
	return t.NotifiedOn == localDate
}

// NotifyCatchUpMinutes — короткое окно восстановления после рестарта или
// занятого доставщика. Поздние сообщения за прошлый день не догоняем.
const NotifyCatchUpMinutes = 10

// UsersToNotify возвращает тех, кому пора отправить рассылку, включая короткое опоздание.
//
// utcMinute — минута суток по UTC. Время рассылки хранится в локальных
// минутах пользователя, поэтому текущая минута приводится к его часовому поясу прямо в
// запросе: так один тик покрывает все часовые пояса разом, и не нужно
// заранее знать, какие из них встречаются среди пользователей.
//
// Утро и вечер — две ветки одного запроса, а не два запроса: рассыльщику
// нужен один список на минуту, а человек с совпавшими временами (утро в 8:30
// и вечер в 8:30 невозможны по границам диапазонов, но настройки живут в
// базе, а не в валидаторе) получит обе строки честно.
//
// Отметки notified_on здесь только отдаются наружу, но не фильтруются:
// «сегодня» у каждого адресата своё, и сравнивать его надо в его часовом
// поясе. Кандидатов на одну минуту единицы, так что отсев на стороне
// вызывающего ничего не стоит.
func (db *DB) UsersToNotify(ctx context.Context, utcMinute int) ([]NotifyTarget, error) {
	if e := db.RefreshUserTimezones(ctx, time.Now()); e != nil {
		return nil, e
	}
	rows, err := db.r.QueryContext(ctx,
		`SELECT `+userColumns+`, ? AS kind
		 FROM users
		 WHERE notify_on = 1 AND morning_on = 1 AND group_id > 0
		   AND ((? + tz_offset) % 1440 + 1440) % 1440 BETWEEN morning_at AND morning_at + ?
		 UNION ALL
		 SELECT `+userColumns+`, ? AS kind
		 FROM users
		 WHERE notify_on = 1 AND evening_on = 1 AND group_id > 0
		   AND ((? + tz_offset) % 1440 + 1440) % 1440 BETWEEN evening_at AND evening_at + ?`,
		string(NotifyMorning), utcMinute, NotifyCatchUpMinutes, string(NotifyEvening), utcMinute, NotifyCatchUpMinutes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NotifyTarget
	for rows.Next() {
		var t NotifyTarget
		if err := scanUser(rows, &t.User, &t.Kind); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkMenuSent отмечает, что человеку показали нижнее меню.
//
// Отдельным UPDATE, а не через SaveUser: отметку ставит обработчик, который в
// этот же момент мог поменять группу или подгруппу, и перезапись всей записи
// снимком, прочитанным до его работы, откатила бы выбор человека. Строки нет
// — значит, и меню ему показывать было незачем.
// Пустая версия от старого клиента не должна стирать новую отметку.
func (db *DB) MarkMenuSent(ctx context.Context, platform, extID string, version ...string) error {
	v := ""
	if len(version) > 0 {
		v = version[0]
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET menu_sent = 1,
             menu_version = CASE WHEN ? = '' THEN menu_version ELSE ? END
             WHERE platform = ? AND ext_id = ?`, v, v, platform, extID)
		return err
	})
}

// MarkNotified отмечает, что за этот локальный день человеку уже написали.
func (db *DB) MarkNotified(ctx context.Context, platform, extID string, kind NotifyKind, localDate string) error {
	column := "notified_on"
	if kind == NotifyEvening {
		column = "notified_eve_on"
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET `+column+` = ? WHERE platform = ? AND ext_id = ?`,
			localDate, platform, extID)
		return err
	})
}

// MarkEmptyHinted отмечает, что разовую подсказку про пустые дни человек уже
// получил.
func (db *DB) MarkEmptyHinted(ctx context.Context, platform, extID string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET empty_hinted = 1 WHERE platform = ? AND ext_id = ?`, platform, extID)
		return err
	})
}

// DisableNotify выключает уведомления целиком и отмечает блокировку.
//
// Сюда приходят из одного места — рассыльщика, получившего от площадки отказ
// «этому адресату больше нельзя». Поэтому вместе с выключателем ставится и
// blocked_at: человек, выключивший рассылку сам, идёт обычным SaveUser и этой
// отметки не получает — на том разница между «не пиши мне» и «выброшен из
// диалога» и держится (см. blocked.go).
//
// Первая дата не перезаписывается: важно, когда человек ушёл, а не когда мы
// в очередной раз в это упёрлись.
func (db *DB) DisableNotify(ctx context.Context, platform, extID string) error {
	now := time.Now().Unix()
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET notify_on = 0, blocked_at = COALESCE(NULLIF(blocked_at, 0), ?)
			 WHERE platform = ? AND ext_id = ?`, now, platform, extID)
		return err
	})
}
