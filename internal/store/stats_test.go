package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// Статистика панели — это десяток агрегатов, которые легко разъезжаются с
// данными молча: неверный COUNT не падает, а рисует правдоподобную цифру.
// Поэтому проверяется не «запрос отработал», а конкретные числа на наборе,
// собранном руками.

// now — момент, относительно которого считаются все окна в тестах.
var statsNow = time.Date(2026, 3, 12, 15, 0, 0, 0, time.UTC)

// seedUsers раскладывает людей по группам, платформам и датам так, чтобы у
// каждого окна («сутки», «неделя», «месяц») был хотя бы один человек и внутри,
// и снаружи.
func seedUsers(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()

	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatalf("каталог групп: %v", err)
	}

	type seed struct {
		platform string
		extID    string
		group    int64
		seenAgo  time.Duration
		madeAgo  time.Duration
		morning  int
		evening  bool
	}
	seeds := []seed{
		{"tg", "1", 231, time.Hour, 2 * time.Hour, 7 * 60, false},
		{"tg", "2", 231, 3 * time.Hour, 30 * time.Hour, 7 * 60, true},
		{"tg", "3", 231, 4 * 24 * time.Hour, 5 * 24 * time.Hour, 8 * 60, false},
		{"tg", "4", 232, 20 * 24 * time.Hour, 25 * 24 * time.Hour, 7*60 + 30, false},
		{"vk", "5", 232, 2 * time.Hour, 3 * time.Hour, 6 * 60, false},
		// Не выбрал группу: попадает в total, но не в configured и не в топы.
		{"vk", "6", 0, time.Hour, time.Hour, 7 * 60, false},
		// Ушёл давно и зарегистрировался за пределами графика в 30 дней.
		{"vk", "7", 232, 60 * 24 * time.Hour, 90 * 24 * time.Hour, 7 * 60, false},
	}

	for _, s := range seeds {
		u := User{Platform: s.platform, ExtID: s.extID, GroupID: s.group}.WithDefaults(180)
		u.MorningAt = s.morning
		u.Evening = s.evening
		if err := db.SaveUser(ctx, u); err != nil {
			t.Fatalf("сохранение пользователя %s: %v", s.extID, err)
		}
		// SaveUser ставит created_at и last_seen «сейчас»; сдвигаем их прямо в
		// базе — иначе окна проверить нечем.
		_, err := db.w.ExecContext(ctx,
			`UPDATE users SET created_at = ?, last_seen = ? WHERE platform = ? AND ext_id = ?`,
			statsNow.Add(-s.madeAgo).Unix(), statsNow.Add(-s.seenAgo).Unix(), s.platform, s.extID)
		if err != nil {
			t.Fatalf("сдвиг дат пользователя %s: %v", s.extID, err)
		}
	}
}

func TestStatsUsers(t *testing.T) {
	db := openTest(t)
	seedUsers(t, db)

	st, err := db.Stats(context.Background(), statsNow)
	if err != nil {
		t.Fatalf("сбор статистики: %v", err)
	}
	u := st.Users

	if u.Total != 7 {
		t.Errorf("всего пользователей = %d, ожидалось 7", u.Total)
	}
	if u.Configured != 6 {
		t.Errorf("выбрали группу = %d, ожидалось 6", u.Configured)
	}
	// Заходили за сутки: четверо (включая того, кто так и не выбрал группу).
	if u.Active1d != 4 {
		t.Errorf("активных за сутки = %d, ожидалось 4", u.Active1d)
	}
	if u.Active7d != 5 {
		t.Errorf("активных за неделю = %d, ожидалось 5", u.Active7d)
	}
	if u.Active30d != 6 {
		t.Errorf("активных за месяц = %d, ожидалось 6", u.Active30d)
	}

	if got := labelOf(u.ByPlatform, "tg"); got != 4 {
		t.Errorf("в телеграме = %d, ожидалось 4", got)
	}
	if got := labelOf(u.ByPlatform, "vk"); got != 3 {
		t.Errorf("во ВКонтакте = %d, ожидалось 3", got)
	}

	// Топ считается только по тем, кто выбрал группу, и отсортирован по убыванию.
	if len(u.TopGroups) != 2 {
		t.Fatalf("групп в топе = %d, ожидалось 2", len(u.TopGroups))
	}
	if u.TopGroups[0].Name != "ГР-11" || u.TopGroups[0].Count != 3 {
		t.Errorf("первая группа топа = %s (%d), ожидалось ГР-11 (3)",
			u.TopGroups[0].Name, u.TopGroups[0].Count)
	}
	if u.TopGroups[0].Department != "Учебный факультет" {
		t.Errorf("подразделение группы = %q, ожидалось подставленное из справочника",
			u.TopGroups[0].Department)
	}
	if u.TopGroups[1].Count != 3 {
		t.Errorf("вторая группа топа = %d, ожидалось 3", u.TopGroups[1].Count)
	}

	// Уведомления: главный выключатель у всех включён по умолчанию, вечернюю
	// рассылку просил ровно один человек.
	if u.Notify.NotifyOn != 7 {
		t.Errorf("бот пишет сам = %d, ожидалось 7", u.Notify.NotifyOn)
	}
	if u.Notify.Evening != 1 {
		t.Errorf("вечерняя рассылка = %d, ожидалось 1", u.Notify.Evening)
	}

	// Гистограмма времени — по часам, и только у тех, кто выбрал группу.
	if got := hourOf(u.MorningAt, 7); got != 4 {
		t.Errorf("рассылок в 7 часов = %d, ожидалось 4", got)
	}
	if got := hourOf(u.MorningAt, 6); got != 1 {
		t.Errorf("рассылок в 6 часов = %d, ожидалось 1", got)
	}
	if got := hourOf(u.EveningAt, 18); got != 1 {
		t.Errorf("вечерних рассылок в 18 часов = %d, ожидалось 1", got)
	}
}

// График регистраций обязан быть сплошным: без пустых дней два соседних
// столбика встают рядом, хотя между ними неделя тишины.
func TestStatsNewByDayIsContinuous(t *testing.T) {
	db := openTest(t)
	seedUsers(t, db)

	st, err := db.Stats(context.Background(), statsNow)
	if err != nil {
		t.Fatalf("сбор статистики: %v", err)
	}
	days := st.Users.NewByDay

	if len(days) != statsNewDays {
		t.Fatalf("дней в графике = %d, ожидалось %d", len(days), statsNewDays)
	}
	if days[len(days)-1].Date != "2026-03-12" {
		t.Errorf("последний день графика = %s, ожидался сегодняшний", days[len(days)-1].Date)
	}

	prev, _ := time.Parse("2006-01-02", days[0].Date)
	for _, d := range days[1:] {
		cur, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			t.Fatalf("разбор даты %q: %v", d.Date, err)
		}
		if cur.Sub(prev) != 24*time.Hour {
			t.Fatalf("разрыв в графике между %s и %s", prev.Format("2006-01-02"), d.Date)
		}
		prev = cur
	}

	// Шестеро зарегистрировались внутри месяца; тот, кому 90 дней, в окно не
	// попадает — на нём и проверяется, что график не тянет всю таблицу.
	total := 0
	for _, d := range days {
		total += d.Count
	}
	if total != 6 {
		t.Errorf("новых за 30 дней = %d, ожидалось 6", total)
	}
}

func TestStatsEmptyBase(t *testing.T) {
	db := openTest(t)

	st, err := db.Stats(context.Background(), statsNow)
	if err != nil {
		t.Fatalf("статистика пустой базы не должна падать: %v", err)
	}
	if st.Users.Total != 0 || st.Content.Lessons != 0 {
		t.Errorf("пустая база отдала непустые цифры: %+v", st)
	}
	if len(st.Users.NewByDay) != statsNewDays {
		t.Errorf("график регистраций пустой базы = %d дней, ожидалось %d",
			len(st.Users.NewByDay), statsNewDays)
	}
	// Размер файла базы известен даже когда в ней ничего нет: страница схемы
	// на диске уже лежит.
	if st.Content.DBBytes <= 0 {
		t.Errorf("размер базы = %d, ожидалось положительное число", st.Content.DBBytes)
	}
}

func TestStatsSyncAndOutbox(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seedUsers(t, db)

	if err := db.SetMeta(ctx, MetaFullSyncAt, "1773500000"); err != nil {
		t.Fatalf("метка полного обхода: %v", err)
	}
	// Мусор в meta не должен ронять панель: дата просто не показывается.
	if err := db.SetMeta(ctx, MetaHotSyncAt, "не число"); err != nil {
		t.Fatalf("метка горячего обхода: %v", err)
	}

	fresh := statsNow.Add(-time.Hour).Unix()
	stale := statsNow.Add(-48 * time.Hour).Unix()
	for _, row := range [][2]int64{{231, fresh}, {232, stale}} {
		_, err := db.w.ExecContext(ctx,
			`INSERT INTO month_state(group_id, year, month, content_hash, fetched_at, changed_at, lesson_count)
			 VALUES(?, 2026, 3, 'hash', ?, ?, 10)`, row[0], row[1], row[1])
		if err != nil {
			t.Fatalf("состояние месяца: %v", err)
		}
	}

	// Одно сообщение в очереди уже трижды не доставлено — именно такие и надо
	// замечать: площадка отвечает отказом, а не бот занят.
	_, err := db.w.ExecContext(ctx,
		`INSERT INTO outbox(platform, ext_id, kind, dedup_key, attempts, next_try_at, created_at)
		 VALUES('tg', '1', 'change', 'a', 4, ?, ?), ('vk', '5', 'change', 'b', 0, ?, ?)`,
		statsNow.Unix(), statsNow.Unix(), statsNow.Unix(), statsNow.Unix())
	if err != nil {
		t.Fatalf("очередь: %v", err)
	}

	st, err := db.Stats(ctx, statsNow)
	if err != nil {
		t.Fatalf("сбор статистики: %v", err)
	}

	if st.Sync.FullSyncAt != 1773500000 {
		t.Errorf("время полного обхода = %d, ожидалось 1773500000", st.Sync.FullSyncAt)
	}
	if st.Sync.HotSyncAt != 0 {
		t.Errorf("нечисловая метка должна читаться как «не было», получено %d", st.Sync.HotSyncAt)
	}
	if st.Sync.Months != 2 {
		t.Errorf("месяцев загружено = %d, ожидалось 2", st.Sync.Months)
	}
	if st.Sync.MonthsStale != 1 {
		t.Errorf("протухших месяцев = %d, ожидался 1", st.Sync.MonthsStale)
	}
	if st.Sync.Tracked != 2 {
		t.Errorf("месяцев под присмотром синка = %d, ожидалось 2", st.Sync.Tracked)
	}
	if st.Sync.ChangedDay != 1 {
		t.Errorf("правок за сутки = %d, ожидалась 1", st.Sync.ChangedDay)
	}

	if st.Outbox.Pending != 2 {
		t.Errorf("в очереди = %d, ожидалось 2", st.Outbox.Pending)
	}
	if st.Outbox.Stuck != 1 {
		t.Errorf("застрявших = %d, ожидалось 1", st.Outbox.Stuck)
	}
	if got := labelOf(st.Outbox.ByPlatform, "tg"); got != 1 {
		t.Errorf("в очереди у телеграма = %d, ожидалось 1", got)
	}
}

// Свежесть меряется только по тому, что синк обходит. Иначе показатель
// «протухло» горит красным всегда — из-за архивных групп и прошлых месяцев,
// которые не обновляются никогда и не должны.
func TestStatsStaleCountsOnlyTrackedMonths(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	seedUsers(t, db)

	long := statsNow.Add(-90 * 24 * time.Hour).Unix()
	rows := []struct {
		group     int64
		year, mon int
		fetched   int64
	}{
		// Действующая группа, текущий месяц, давно не обновлялась — вот это
		// настоящая протухшая пара.
		{231, statsNow.Year(), int(statsNow.Month()), long},
		// Та же группа, но месяц уже прошёл: синк туда не возвращается.
		{231, statsNow.Year(), int(statsNow.Month()) - 1, long},
		// Архивная группа (is_active = 0 в testTree) — за ней тоже не ходят.
		{859, statsNow.Year(), int(statsNow.Month()), long},
	}
	for _, r := range rows {
		_, err := db.w.ExecContext(ctx,
			`INSERT INTO month_state(group_id, year, month, content_hash, fetched_at, changed_at, lesson_count)
			 VALUES(?, ?, ?, 'hash', ?, ?, 5)`, r.group, r.year, r.mon, r.fetched, r.fetched)
		if err != nil {
			t.Fatalf("состояние месяца: %v", err)
		}
	}

	st, err := db.Stats(ctx, statsNow)
	if err != nil {
		t.Fatalf("сбор статистики: %v", err)
	}
	if st.Sync.Months != 3 {
		t.Errorf("всего месяцев = %d, ожидалось 3", st.Sync.Months)
	}
	if st.Sync.Tracked != 1 {
		t.Errorf("под присмотром синка = %d, ожидался 1: прошедший месяц и архивная группа не в счёт",
			st.Sync.Tracked)
	}
	if st.Sync.MonthsStale != 1 {
		t.Errorf("протухших = %d, ожидался 1", st.Sync.MonthsStale)
	}
}

func labelOf(list []LabelCount, label string) int {
	for _, lc := range list {
		if lc.Label == label {
			return lc.Count
		}
	}
	return -1
}

func hourOf(list []MinuteBucket, hour int) int {
	for _, b := range list {
		if b.Hour == hour {
			return b.Count
		}
	}
	return -1
}

func TestStatsWebsiteAccountsAndSessions(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	now := time.Unix(1800000000, 0)
	// Same numeric id across platforms means two identities. Duplicated devices
	// must not inflate people; expired and non-allowlisted sessions grant no access.
	if _, err := db.w.Exec(`INSERT INTO web_allowlist VALUES('tg','1',1),('vk','1',1),('vk','2',1),('tg','3',1)`); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct {
		p, id   string
		expires int64
	}{
		{"tg", "1", now.Unix() + 100}, {"tg", "1", now.Unix() + 200},
		{"vk", "1", now.Unix() + 100}, {"vk", "2", now.Unix()},
		{"tg", "3", now.Unix() - 1}, {"vk", "999", now.Unix() + 100},
	} {
		if _, err := db.w.Exec(`INSERT INTO web_sessions VALUES(?,?,?,?,?,?,?)`, strconv.Itoa(i), strconv.Itoa(i), row.p, row.id, "test", now.Unix()-200, row.expires); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.Stats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	want := WebStats{SignedIn: 2, Sessions: 3, Allowed: 4, Telegram: 1, VK: 1}
	if got.Web != want {
		t.Fatalf("got %+v want %+v", got.Web, want)
	}
	if _, err := db.w.Exec(`DELETE FROM web_sessions WHERE platform='tg' AND ext_id='1'`); err != nil {
		t.Fatal(err)
	}
	got, err = db.Stats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Web.SignedIn != 1 || got.Web.Sessions != 1 || got.Web.Telegram != 0 {
		t.Fatal(got.Web)
	}
	if _, err := db.w.Exec(`DELETE FROM web_allowlist WHERE platform='vk' AND ext_id='1'`); err != nil {
		t.Fatal(err)
	}
	got, err = db.Stats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Web.SignedIn != 0 || got.Web.Sessions != 0 || got.Web.Allowed != 3 {
		t.Fatal(got.Web)
	}
}
