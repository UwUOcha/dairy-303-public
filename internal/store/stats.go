package store

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/profile"
)

// Здесь живут агрегаты для админ-панели: единственное место, где база
// опрашивается «поперёк» — не про одну группу и не про одного человека, а про
// систему целиком.
//
// Почему это в store, а не в admind. Правило проекта — владелец базы один, и
// открывать файл вторым процессом ради красивых цифр значит разменять это
// правило на удобство. Панель получает готовые числа из raspd по тому же
// сокету, что и бот.
//
// Все запросы — COUNT по таблицам в десятки тысяч строк либо группировки по
// существующим индексам: весь снимок собирается за единицы миллисекунд, и
// опрашивать его раз в десять секунд можно без оглядки.

// Stats — снимок системы для админ-панели.
type Stats struct {
	Users   UserStats    `json:"users"`
	Web     WebStats     `json:"web"`
	Content ContentStats `json:"content"`
	Sync    SyncStats    `json:"sync"`
	Outbox  OutboxStats  `json:"outbox"`
}

// WebStats counts accounts with at least one valid website session, not live
// visitors. An identity is (platform, ext_id); multiple browsers count once.
type WebStats struct {
	SignedIn int `json:"signed_in"`
	Sessions int `json:"sessions"`
	Allowed  int `json:"allowed"`
	Telegram int `json:"telegram"`
	VK       int `json:"vk"`
}

// UserStats — кто пользуется ботом.
type UserStats struct {
	Total int `json:"total"`
	// Configured — довели настройку до выбора группы. Разница с Total — это
	// те, кто нажал «старт» и ушёл: воронка входа в одной цифре.
	Configured int          `json:"configured"`
	ByPlatform []LabelCount `json:"by_platform"`
	Active1d   int          `json:"active_1d"`
	Active7d   int          `json:"active_7d"`
	Active30d  int          `json:"active_30d"`
	NewByDay   []DayCount   `json:"new_by_day"`
	// Blocked — сколько человек выбросили бота из диалога, а BlockedGroups —
	// из каких групп они уходили. Разрез по группам здесь ровно потому, что
	// одна цифра ничего не подсказывает: пять уходов из одной группы за месяц
	// — это про группу (сменили расписание, выпустились, завёлся свой чат), а
	// те же пять по всей базе — обычный фон.
	Blocked       int          `json:"blocked"`
	BlockedGroups []GroupCount `json:"blocked_groups,omitempty"`
	// Probed — скольких из настроенных вообще успели проверить живым
	// запросом. Без него ноль в Blocked нельзя прочитать: он одинаково значит
	// и «никто не ушёл», и «обход ещё идёт по базе первый раз».
	Probed    int          `json:"probed"`
	TopGroups []GroupCount `json:"top_groups"`
	TopDeps   []LabelCount `json:"top_departments"`
	ByCourse  []LabelCount `json:"by_course"`
	Notify    NotifyStats  `json:"notify"`
	// MorningAt и EveningAt — во сколько люди просят себя разбудить, по часам
	// суток. Ради них панель и рисует суточную ленту: это единственная цифра,
	// которая прямо говорит, когда бот разговаривает с людьми.
	MorningAt []MinuteBucket `json:"morning_at"`
	EveningAt []MinuteBucket `json:"evening_at"`
}

// NotifyStats — сколько человек оставили каждую рассылку включённой.
type NotifyStats struct {
	NotifyOn  int `json:"notify_on"`
	Morning   int `json:"morning"`
	Evening   int `json:"evening"`
	Changes   int `json:"changes"`
	EmptyDays int `json:"empty_days"`
}

// ContentStats — сколько данных вуза лежит в локальной копии.
type ContentStats struct {
	Groups       int `json:"groups"`
	GroupsActive int `json:"groups_active"`
	Subgroups    int `json:"subgroups"`
	Lessons      int `json:"lessons"`
	Disciplines  int `json:"disciplines"`
	Staff        int `json:"staff"`
	Classrooms   int `json:"classrooms"`
	// DBBytes — размер файла базы вместе с журналом: именно он растёт на диске.
	DBBytes int64 `json:"db_bytes"`
}

// SyncStats — здоровье фонового контура.
type SyncStats struct {
	FullSyncAt   int64 `json:"full_sync_at,omitempty"`
	HotSyncAt    int64 `json:"hot_sync_at,omitempty"`
	GroupsSyncAt int64 `json:"groups_sync_at,omitempty"`
	// Months — сколько пар «группа + месяц» вообще лежит в базе, вместе с
	// архивными группами и прошедшими месяцами.
	Months int `json:"months"`
	// Tracked — из них те, которые синк обязан держать свежими: месяцы от
	// текущего и дальше у действующих групп.
	//
	// Отдельно от Months, потому что иначе метрика «протухло» бессмысленна.
	// Прошлогодние месяцы выпущенной группы не обновляются никогда и не
	// должны — считать их протухшими значит держать на панели показатель,
	// который горит красным всегда. Индикатор, который всегда красный, учит
	// на себя не смотреть.
	Tracked int `json:"tracked"`
	// MonthsStale — из Tracked не обновлялись дольше суток. Ноль после
	// ночного обхода и растёт, когда обход не доходит до конца.
	MonthsStale int   `json:"months_stale"`
	OldestFetch int64 `json:"oldest_fetch,omitempty"`
	// ChangedDay — сколько месяцев вуз поправил за сутки. Это и есть мера
	// того, ради чего держится локальная копия.
	ChangedDay  int `json:"changed_day"`
	ChangedWeek int `json:"changed_week"`
	// ChangeDays — снимков «до правки» в работе (живут трое суток).
	ChangeDays int `json:"change_days"`
}

// OutboxStats — очередь новостей о правках.
type OutboxStats struct {
	Pending int `json:"pending"`
	// Stuck — сообщения, которые не удалось доставить трижды. Ненулевое
	// значение здесь значит, что площадка отвечает отказом, а не что бот занят.
	Stuck         int          `json:"stuck"`
	ByPlatform    []LabelCount `json:"by_platform"`
	OldestCreated int64        `json:"oldest_created,omitempty"`
}

// LabelCount — «метка: сколько». Метка уже пригодна к показу.
type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// DayCount — сколько за календарный день, ISO-дата.
type DayCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// GroupCount — группа и сколько человек её выбрали.
type GroupCount struct {
	GroupID    int64  `json:"group_id"`
	Name       string `json:"name"`
	Department string `json:"department,omitempty"`
	Course     int    `json:"course,omitempty"`
	Count      int    `json:"count"`
}

// MinuteBucket — сколько человек поставили рассылку на этот час суток.
type MinuteBucket struct {
	Hour  int `json:"hour"`
	Count int `json:"count"`
}

// statsTopLimit — сколько строк отдавать в топах. Больше десятка в панели уже
// не читается глазом, а хвост распределения ничего не объясняет.
const statsTopLimit = 10

// statsNewDays — глубина графика регистраций.
const statsNewDays = 30

// Stats собирает снимок системы.
//
// now передаётся, а не берётся из time.Now, ради тестируемости: «новые за 30
// дней» и «протухшие месяцы» иначе нельзя проверить, не подкручивая часы.
func (db *DB) Stats(ctx context.Context, now time.Time) (Stats, error) {
	var s Stats
	for _, step := range []func(context.Context, *Stats, time.Time) error{
		db.statsUsers,
		db.statsWeb,
		db.statsContent,
		db.statsSync,
		db.statsOutbox,
	} {
		if err := step(ctx, &s, now); err != nil {
			return s, err
		}
	}
	return s, nil
}

func (db *DB) statsUsers(ctx context.Context, s *Stats, now time.Time) error {
	u := &s.Users

	// Десяток срезов одним проходом по таблице: отдельный COUNT на каждый
	// вопрос был бы десятком сканов подряд по одним и тем же строкам.
	day := now.AddDate(0, 0, -1).Unix()
	week := now.AddDate(0, 0, -7).Unix()
	month := now.AddDate(0, 0, -30).Unix()
	err := db.r.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(group_id > 0), 0),
		        COALESCE(SUM(last_seen >= ?), 0),
		        COALESCE(SUM(last_seen >= ?), 0),
		        COALESCE(SUM(last_seen >= ?), 0),
		        COALESCE(SUM(notify_on = 1), 0),
		        COALESCE(SUM(notify_on = 1 AND morning_on = 1), 0),
		        COALESCE(SUM(notify_on = 1 AND evening_on = 1), 0),
		        COALESCE(SUM(notify_on = 1 AND changes_on = 1), 0),
		        COALESCE(SUM(notify_on = 1 AND empty_on = 1), 0)
		 FROM users`, day, week, month).
		Scan(&u.Total, &u.Configured, &u.Active1d, &u.Active7d, &u.Active30d,
			&u.Notify.NotifyOn, &u.Notify.Morning, &u.Notify.Evening,
			&u.Notify.Changes, &u.Notify.EmptyDays)
	if err != nil {
		return err
	}

	if u.ByPlatform, err = db.labelCounts(ctx,
		`SELECT platform, COUNT(*) FROM users GROUP BY platform ORDER BY COUNT(*) DESC`); err != nil {
		return err
	}

	// Время рассылки хранится в минутах от полуночи по местному времени
	// человека — по часам его и раскладываем, иначе получится гистограмма на
	// 1440 столбцов.
	if u.MorningAt, err = db.minuteBuckets(ctx,
		`SELECT morning_at / 60, COUNT(*) FROM users
		 WHERE notify_on = 1 AND morning_on = 1 AND group_id > 0
		 GROUP BY morning_at / 60 ORDER BY 1`); err != nil {
		return err
	}
	if u.EveningAt, err = db.minuteBuckets(ctx,
		`SELECT evening_at / 60, COUNT(*) FROM users
		 WHERE notify_on = 1 AND evening_on = 1 AND group_id > 0
		 GROUP BY evening_at / 60 ORDER BY 1`); err != nil {
		return err
	}

	if u.NewByDay, err = db.newByDay(ctx, now); err != nil {
		return err
	}

	if u.TopGroups, err = db.groupCounts(ctx,
		`SELECT g.id, g.name, COALESCE(d.name, ''), g.course, COUNT(*) AS n
		 FROM users u
		 JOIN groups g ON g.id = u.group_id
		 LEFT JOIN departments d ON d.id = g.department_id
		 WHERE u.group_id > 0
		 GROUP BY g.id ORDER BY n DESC, g.name LIMIT ?`, statsTopLimit); err != nil {
		return err
	}

	if err := db.blockedStats(ctx, u); err != nil {
		return err
	}

	if u.TopDeps, err = db.labelCounts(ctx,
		`SELECT COALESCE(d.name, 'без подразделения'), COUNT(*) AS n
		 FROM users u
		 JOIN groups g ON g.id = u.group_id
		 LEFT JOIN departments d ON d.id = g.department_id
		 WHERE u.group_id > 0
		 GROUP BY g.department_id ORDER BY n DESC LIMIT ?`, statsTopLimit); err != nil {
		return err
	}

	u.ByCourse, err = db.labelCounts(ctx,
		`SELECT CASE WHEN g.course > 0 THEN g.course || ' курс' ELSE 'без курса' END, COUNT(*) AS n
		 FROM users u
		 JOIN groups g ON g.id = u.group_id
		 WHERE u.group_id > 0
		 GROUP BY g.course ORDER BY g.course`)
	return err
}

// newByDay — регистрации за месяц, включая дни, когда никто не пришёл.
//
// Пустые дни дорисовываются здесь, а не в панели: график без них врёт формой —
// два соседних столбика оказываются рядом, хотя между ними неделя тишины.
func (db *DB) newByDay(ctx context.Context, now time.Time) ([]DayCount, error) {
	from := now.AddDate(0, 0, -(statsNewDays - 1))
	midnight := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	rows, err := db.r.QueryContext(ctx,
		`SELECT date(created_at, 'unixepoch'), COUNT(*)
		 FROM users WHERE created_at >= ?
		 GROUP BY 1`, midnight.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]int, statsNewDays)
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		seen[d] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]DayCount, 0, statsNewDays)
	for i := 0; i < statsNewDays; i++ {
		d := midnight.AddDate(0, 0, i).Format("2006-01-02")
		out = append(out, DayCount{Date: d, Count: seen[d]})
	}
	return out, nil
}

func (db *DB) statsContent(ctx context.Context, s *Stats, _ time.Time) error {
	c := &s.Content
	err := db.r.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM groups),
		        (SELECT COUNT(*) FROM groups WHERE is_active = 1),
		        (SELECT COUNT(*) FROM subgroups),
		        (SELECT COUNT(*) FROM lessons),
		        (SELECT COUNT(*) FROM disciplines),
		        (SELECT COUNT(*) FROM staff),
		        (SELECT COUNT(*) FROM classrooms)`).
		Scan(&c.Groups, &c.GroupsActive, &c.Subgroups, &c.Lessons,
			&c.Disciplines, &c.Staff, &c.Classrooms)
	if err != nil {
		return err
	}
	c.DBBytes = db.fileSize()
	return nil
}

// fileSize — сколько база занимает на диске вместе с журналом.
//
// Ошибка stat не ошибка запроса: файл может быть недоступен, и терять из-за
// этого всю статистику незачем — цифра просто не показывается.
func (db *DB) fileSize() int64 {
	if db.path == "" {
		return 0
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(db.path + suffix); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func (db *DB) statsSync(ctx context.Context, s *Stats, now time.Time) error {
	y := &s.Sync
	for key, dst := range map[string]*int64{
		MetaFullSyncAt:     &y.FullSyncAt,
		MetaHotSyncAt:      &y.HotSyncAt,
		MetaGroupsSyncedAt: &y.GroupsSyncAt,
	} {
		ts, err := db.metaUnix(ctx, key)
		if err != nil {
			return err
		}
		*dst = ts
	}

	day := now.AddDate(0, 0, -1).Unix()
	week := now.AddDate(0, 0, -7).Unix()
	err := db.r.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(changed_at >= ?), 0),
		        COALESCE(SUM(changed_at >= ?), 0)
		 FROM month_state`, day, week).
		Scan(&y.Months, &y.ChangedDay, &y.ChangedWeek)
	if err != nil {
		return err
	}

	// Свежесть считается по тому, что синк действительно обходит: месяцы от
	// текущего и дальше у действующих групп. Спрятанные дубликаты каталога
	// (см. shadow.go) тоже не в счёт — за ними никто не ходит.
	stale := now.Add(-24 * time.Hour).Unix()
	var oldest sql.NullInt64
	err = db.r.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(ms.fetched_at < ?), 0), MIN(ms.fetched_at)
		 FROM month_state ms
		 JOIN groups g ON g.id = ms.group_id
		 WHERE g.is_active = 1 AND g.shadowed = 0
		   AND (ms.year > ? OR (ms.year = ? AND ms.month >= ?))`,
		stale, now.Year(), now.Year(), int(now.Month())).
		Scan(&y.Tracked, &y.MonthsStale, &oldest)
	if err != nil {
		return err
	}
	y.OldestFetch = oldest.Int64

	return db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM change_days`).Scan(&y.ChangeDays)
}

func (db *DB) statsOutbox(ctx context.Context, s *Stats, _ time.Time) error {
	o := &s.Outbox
	var oldest sql.NullInt64
	err := db.r.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(attempts >= 3), 0), MIN(created_at) FROM outbox`).
		Scan(&o.Pending, &o.Stuck, &oldest)
	if err != nil {
		return err
	}
	o.OldestCreated = oldest.Int64

	o.ByPlatform, err = db.labelCounts(ctx,
		`SELECT platform, COUNT(*) FROM outbox GROUP BY platform ORDER BY COUNT(*) DESC`)
	return err
}

// metaUnix читает из meta значение-timestamp. Ни отсутствия ключа, ни мусора в
// нём панель не переживает как ошибку: пустая дата честнее пятисотки.
func (db *DB) metaUnix(ctx context.Context, key string) (int64, error) {
	raw, err := db.Meta(ctx, key)
	if err != nil || raw == "" {
		return 0, err
	}
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, nil
	}
	return ts, nil
}

func (db *DB) labelCounts(ctx context.Context, query string, args ...any) ([]LabelCount, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LabelCount
	for rows.Next() {
		var lc LabelCount
		if err := rows.Scan(&lc.Label, &lc.Count); err != nil {
			return nil, err
		}
		out = append(out, lc)
	}
	return out, rows.Err()
}

func (db *DB) minuteBuckets(ctx context.Context, query string, args ...any) ([]MinuteBucket, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MinuteBucket
	for rows.Next() {
		var b MinuteBucket
		if err := rows.Scan(&b.Hour, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (db *DB) statsWeb(ctx context.Context, s *Stats, now time.Time) error {
	return db.r.QueryRowContext(ctx, `
 WITH allowed AS (
  SELECT platform,ext_id FROM web_allowlist
  UNION SELECT 'tg',? WHERE ?<>''
  UNION SELECT 'vk',? WHERE ?<>''
 ), valid_sessions AS (
  SELECT s.platform,s.ext_id FROM web_sessions s
  JOIN allowed a ON a.platform=s.platform AND a.ext_id=s.ext_id
  WHERE s.expires>?
 ), accounts AS (SELECT DISTINCT platform,ext_id FROM valid_sessions)
 SELECT (SELECT COUNT(*) FROM accounts),
        (SELECT COUNT(*) FROM valid_sessions),
        (SELECT COUNT(*) FROM allowed),
        (SELECT COUNT(*) FROM accounts WHERE platform='tg'),
        (SELECT COUNT(*) FROM accounts WHERE platform='vk')`, profile.Current().AdminTG, profile.Current().AdminTG, profile.Current().AdminVK, profile.Current().AdminVK, now.Unix()).
		Scan(&s.Web.SignedIn, &s.Web.Sessions, &s.Web.Allowed, &s.Web.Telegram, &s.Web.VK)
}
