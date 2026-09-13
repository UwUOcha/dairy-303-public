package store

import (
	"context"
	"testing"
	"time"
)

// saveProbeUser заводит человека с выбранной группой и включённой рассылкой.
func saveProbeUser(t *testing.T, db *DB, extID string, groupID int64) {
	t.Helper()
	u := User{Platform: "tg", ExtID: extID, GroupID: groupID}.WithDefaults(180)
	u.Platform, u.ExtID, u.GroupID = "tg", extID, groupID
	if err := db.SaveUser(context.Background(), u); err != nil {
		t.Fatalf("сохранение пользователя %s: %v", extID, err)
	}
}

// В очередь на проверку попадают только те, о ком есть что узнавать, и только
// когда прошёл срок.
func TestUsersToProbeSelection(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	saveProbeUser(t, db, "1", 232)
	saveProbeUser(t, db, "2", 231)
	// Нажал «старт» и ушёл, не выбрав группу: спрашивать площадку о нём
	// незачем — он ничего не расскажет ни уходом, ни группой.
	saveProbeUser(t, db, "3", 0)

	now := time.Now()
	targets, err := db.UsersToProbe(ctx, "tg", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("в очередь попало %d адресатов, ожидались двое настроенных: %+v", len(targets), targets)
	}
	for _, tg := range targets {
		if tg.ExtID == "3" {
			t.Error("ненастроенный пользователь попал в очередь на проверку")
		}
		if tg.Blocked {
			t.Errorf("%s числится заблокированным до первой проверки", tg.ExtID)
		}
	}

	// Другая площадка своей очереди не видит.
	if other, err := db.UsersToProbe(ctx, "vk", 10, now); err != nil || len(other) != 0 {
		t.Fatalf("очередь чужой площадки = %+v, %v", other, err)
	}

	// Проверенный уходит из очереди на неделю и возвращается в неё потом.
	if err := db.MarkProbed(ctx, "tg", "1", true); err != nil {
		t.Fatal(err)
	}
	targets, err = db.UsersToProbe(ctx, "tg", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ExtID != "2" {
		t.Fatalf("сразу после проверки очередь = %+v", targets)
	}
	targets, err = db.UsersToProbe(ctx, "tg", 10, now.Add(ProbeInterval+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("через неделю очередь = %+v, ожидались оба", targets)
	}
}

// Найденная блокировка выключает рассылку, чистит очередь и попадает в
// статистику вместе с группой, из которой человек ушёл.
func TestMarkProbedBlocked(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	saveProbeUser(t, db, "1", 232)
	if err := db.Enqueue(ctx, "tg", "1", OutboxChange, "k", `{}`); err != nil {
		t.Fatal(err)
	}

	if err := db.MarkProbed(ctx, "tg", "1", false); err != nil {
		t.Fatal(err)
	}

	u, err := db.User(ctx, "tg", "1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Notify {
		t.Error("заблокировавшему бота продолжают слать уведомления")
	}
	items, err := db.TakeOutbox(ctx, "tg", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("очередь заблокировавшего не очищена: %+v", items)
	}

	stats, err := db.Stats(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users.Blocked != 1 {
		t.Errorf("заблокировавших в статистике %d, ожидался один", stats.Users.Blocked)
	}
	if stats.Users.Probed != 1 {
		t.Errorf("проверенных в статистике %d, ожидался один", stats.Users.Probed)
	}
	if len(stats.Users.BlockedGroups) != 1 || stats.Users.BlockedGroups[0].Name != "ГР-12" {
		t.Errorf("разрез ушедших по группам = %+v", stats.Users.BlockedGroups)
	}
	// Общий счётчик заблокировавших не теряет: они остаются в базе, и цифра
	// «сколько человек когда-либо дошли до группы» от ухода не меняется.
	if n, err := db.CountUsers(ctx); err != nil || n != 1 {
		t.Errorf("публичный счётчик = %d, %v; ожидалась единица", n, err)
	}
}

// Вернувшемуся возвращают и рассылку: выключали её не он.
func TestMarkProbedReturn(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	saveProbeUser(t, db, "1", 232)
	if err := db.MarkProbed(ctx, "tg", "1", false); err != nil {
		t.Fatal(err)
	}

	targets, err := db.UsersToProbe(ctx, "tg", 10, time.Now().Add(ProbeInterval+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || !targets[0].Blocked {
		t.Fatalf("заблокировавший должен приезжать в очередь с отметкой: %+v", targets)
	}

	if err := db.MarkProbed(ctx, "tg", "1", true); err != nil {
		t.Fatal(err)
	}
	u, err := db.User(ctx, "tg", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Notify {
		t.Error("вернувшийся остался без рассылки, хотя выключали её мы")
	}
	stats, err := db.Stats(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users.Blocked != 0 {
		t.Errorf("вернувшийся так и числится ушедшим: %d", stats.Users.Blocked)
	}
}

// Человека, выключившего рассылку своими руками, проверка не трогает: он
// доступен, но включать ему уведомления обратно никто не просил.
func TestMarkProbedKeepsOwnChoice(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	u := User{Platform: "tg", ExtID: "1", GroupID: 232}.WithDefaults(180)
	u.Platform, u.ExtID, u.GroupID = "tg", "1", 232
	u.Notify = false
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	if err := db.MarkProbed(ctx, "tg", "1", true); err != nil {
		t.Fatal(err)
	}

	got, err := db.User(ctx, "tg", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Notify {
		t.Error("проверка включила рассылку тому, кто выключил её сам")
	}
	stats, err := db.Stats(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users.Blocked != 0 {
		t.Errorf("выключивший рассылку сам попал в ушедшие: %d", stats.Users.Blocked)
	}
}

// Отказ площадки в рассылке отмечает блокировку так же, как её отмечает
// недельный обход: иначе цифра зависела бы от того, каким путём мы узнали.
func TestDisableNotifyMarksBlocked(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	saveProbeUser(t, db, "1", 232)

	if err := db.DisableNotify(ctx, "tg", "1"); err != nil {
		t.Fatal(err)
	}
	stats, err := db.Stats(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Users.Blocked != 1 {
		t.Errorf("блокировка, найденная рассылкой, не попала в статистику: %d", stats.Users.Blocked)
	}
	// Проверять его всё равно надо: обход и узнает, что человек вернулся.
	targets, err := db.UsersToProbe(ctx, "tg", 10, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || !targets[0].Blocked {
		t.Fatalf("очередь после отказа рассылки = %+v", targets)
	}
}
