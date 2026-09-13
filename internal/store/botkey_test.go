package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestBotKeySingletonLifecycleAndOwner(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, err := db.ManageBotKey(ctx, "create"); err == nil {
		t.Fatal("created without owner")
	}
	// Neither another Telegram account nor the same numeric VK ID qualifies.
	for _, u := range []User{{Platform: "tg", ExtID: "1", GroupID: 39}, {Platform: "vk", ExtID: BotKeyOwner(), GroupID: 39}} {
		if err := db.SaveUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ManageBotKey(ctx, "create"); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: BotKeyOwner(), GroupID: 39, SubgroupID: 707}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan BotKeyResponse, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := db.ManageBotKey(ctx, "create")
			if err == nil {
				results <- out
			}
		}()
	}
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("created %d keys", len(results))
	}
	key := <-results
	var raw string
	if err := db.r.QueryRow(`SELECT token_hash FROM bot_api_key`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == key.Token || raw != webHash(key.Token) {
		t.Fatal("key not hashed")
	}
	if status, err := db.ManageBotKey(ctx, "status"); err != nil || !status.Active || status.Token != "" {
		t.Fatal(status, err)
	}
	if g, sub, err := db.BotKeyProfile(ctx, key.Token); err != nil || g != 39 || sub != 707 {
		t.Fatal(g, sub, err)
	}
	if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: BotKeyOwner(), GroupID: 545, SubgroupID: 708}); err != nil {
		t.Fatal(err)
	}
	if g, sub, err := db.BotKeyProfile(ctx, key.Token); err != nil || g != 545 || sub != 708 {
		t.Fatal("stale binding", g, sub, err)
	}
	rotated, err := db.ManageBotKey(ctx, "rotate")
	if err != nil || rotated.Token == key.Token {
		t.Fatal("rotation failed", err)
	}
	if _, _, err := db.BotKeyProfile(ctx, key.Token); !errors.Is(err, ErrNotFound) {
		t.Fatal("old key accepted", err)
	}
	if _, err := db.w.Exec(`INSERT INTO bot_api_key VALUES(2,'tg','1001','x',1)`); err == nil {
		t.Fatal("second row allowed")
	}
	if _, err := db.w.Exec(`UPDATE bot_api_key SET ext_id='1'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BotKeyProfile(ctx, rotated.Token); !errors.Is(err, ErrNotFound) {
		t.Fatal("unconfigured owner accepted", err)
	}
	if _, err := db.w.Exec(`UPDATE bot_api_key SET ext_id=?`, BotKeyOwner()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.w.Exec(`UPDATE bot_api_key SET platform='vk'`); err == nil {
		t.Fatal("VK allowed")
	}
	path := db.path
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := db.BotKeyProfile(ctx, rotated.Token); err != nil {
		t.Fatal("key lost after restart", err)
	}
	if _, err := db.ManageBotKey(ctx, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BotKeyProfile(ctx, rotated.Token); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked key accepted", err)
	}
	if out, err := db.ManageBotKey(ctx, "status"); err != nil || out.Active {
		t.Fatal(out, err)
	}
}

func TestBotKeyMigrationFrom12(t *testing.T) {
	db := openTest(t)
	if _, err := db.w.Exec(`DROP TABLE bot_api_key; DROP TABLE provider_ids; ALTER TABLE staff_accounts RENAME TO staff_apeks; ALTER TABLE staff_apeks RENAME COLUMN account_id TO apeks_id; ALTER TABLE staff_vacancies RENAME COLUMN account_id TO apeks_id; ALTER TABLE users DROP COLUMN tz_name; PRAGMA user_version=12`); err != nil {
		t.Fatal(err)
	}
	path := db.path
	db.Close()
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if out, err := db.ManageBotKey(context.Background(), "status"); err != nil || out.Active {
		t.Fatal(out, err)
	}
}
