package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

func authFixture(t *testing.T) (*DB, func(WebAuthRequest) WebAuthResponse) {
	t.Helper()
	db := openTest(t)
	err := db.tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO web_allowlist VALUES('tg','1',1),('vk','2',1)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, func(in WebAuthRequest) WebAuthResponse {
		t.Helper()
		out, err := db.WebAuth(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
}
func loginChallenge(t *testing.T, call func(WebAuthRequest) WebAuthResponse) (string, string, string) {
	t.Helper()
	b := WebSecret()
	s := call(WebAuthRequest{Op: "start", Platform: "tg", Browser: b})
	if s.Error != "" {
		t.Fatal(s)
	}
	issued := call(WebAuthRequest{Op: "issue", Platform: "tg", ExtID: "1", Challenge: s.Challenge})
	if len(issued.Code) != 8 {
		t.Fatal(issued)
	}
	return b, s.Challenge, issued.Code
}
func TestWebAuthBrowserBindingReplayAndWhitelist(t *testing.T) {
	db, call := authFixture(t)
	b, ch, code := loginChallenge(t, call)
	for _, in := range []WebAuthRequest{
		{Op: "issue", Platform: "tg", ExtID: "999", Challenge: ch},
		{Op: "issue", Platform: "vk", ExtID: "2", Challenge: ch},
		{Op: "issue", Platform: "tg", ExtID: "1", Challenge: ch},
		{Op: "finish", Browser: WebSecret(), Challenge: ch, Code: code},
	} {
		if out := call(in); out.Error == "" {
			t.Fatalf("unexpected grant: %+v", in)
		}
	}
	out := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code})
	if out.Token == "" {
		t.Fatal(out)
	}
	if got := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code}); got.Error != "expired" {
		t.Fatal(got)
	}
	if got := call(WebAuthRequest{Op: "session", Token: out.Token}); got.Error != "" {
		t.Fatal(got)
	}
	var raw string
	db.r.QueryRow(`SELECT token FROM web_sessions`).Scan(&raw)
	if raw == out.Token {
		t.Fatal("raw bearer stored")
	}
	if err := db.tx(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM web_allowlist WHERE platform='tg'`); return err }); err != nil {
		t.Fatal(err)
	}
	if got := call(WebAuthRequest{Op: "session", Token: out.Token}); got.Error != "unauthorized" {
		t.Fatal(got)
	}
}
func TestWebAuthGuessLimitAndExpiry(t *testing.T) {
	db, call := authFixture(t)
	b, ch, code := loginChallenge(t, call)
	wrong := "00000000"
	if wrong == code {
		wrong = "11111111"
	}
	for i := 0; i < 5; i++ {
		if out := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: wrong}); out.Error != "code" {
			t.Fatal(out)
		}
	}
	if out := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code}); out.Error != "expired" {
		t.Fatal(out)
	}
	b, ch, code = loginChallenge(t, call)
	db.tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE web_challenges SET expires=?`, time.Now().Unix()-1)
		return err
	})
	if out := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code}); out.Error != "expired" {
		t.Fatal(out)
	}
}
func TestWebAuthConcurrentDeviceLimitAndRevocation(t *testing.T) {
	db, call := authFixture(t)
	inputs := make([]WebAuthRequest, 8)
	for i := range inputs {
		b, ch, code := loginChallenge(t, call)
		inputs[i] = WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code, Label: "test"}
	}
	results := make(chan WebAuthResponse, 8)
	var wg sync.WaitGroup
	for _, in := range inputs {
		wg.Add(1)
		go func(in WebAuthRequest) {
			defer wg.Done()
			out, err := db.WebAuth(context.Background(), in)
			if err != nil {
				out.Error = err.Error()
			}
			results <- out
		}(in)
	}
	wg.Wait()
	close(results)
	var tokens []string
	for out := range results {
		if out.Error == "" {
			tokens = append(tokens, out.Token)
		} else if out.Error != "device_limit" {
			t.Fatal(out)
		}
	}
	if len(tokens) != 4 {
		t.Fatalf("sessions: %d", len(tokens))
	}
	list := call(WebAuthRequest{Op: "devices", Token: tokens[0]})
	if len(list.Devices) != 4 {
		t.Fatal(list)
	}
	// Another identity cannot revoke a session by knowing its public id.
	b := WebSecret()
	ch := call(WebAuthRequest{Op: "start", Platform: "vk", Browser: b}).Challenge
	code := call(WebAuthRequest{Op: "issue", Platform: "vk", ExtID: "2", Challenge: ch}).Code
	other := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code}).Token
	call(WebAuthRequest{Op: "revoke", Token: other, ID: list.Devices[0].ID})
	if got := call(WebAuthRequest{Op: "devices", Token: tokens[0]}); len(got.Devices) != 4 {
		t.Fatal(got)
	}
	call(WebAuthRequest{Op: "revoke", Token: tokens[0], ID: list.Devices[0].ID})
	var n int
	db.r.QueryRow(`SELECT count(*) FROM web_sessions WHERE platform='tg'`).Scan(&n)
	if n != 3 {
		t.Fatal(n)
	}
	call(WebAuthRequest{Op: "bot_revoke", Platform: "tg", ExtID: "1"})
	for _, token := range tokens {
		if got := call(WebAuthRequest{Op: "session", Token: token}); got.Error != "unauthorized" {
			t.Fatal(got)
		}
	}
	if got := call(WebAuthRequest{Op: "session", Token: other}); got.Error != "" {
		t.Fatal(got)
	}
}
func TestWebAuthDoesNotEnrollByGroup(t *testing.T) {
	_, call := authFixture(t)
	out := call(WebAuthRequest{Op: "issue", Platform: "tg", ExtID: "3", Challenge: WebSecret()})
	if out.Error != "not_allowed" {
		t.Fatal(out)
	}
}

func TestWebAuthPrivateEnrollmentAndRemoval(t *testing.T) {
	_, call := authFixture(t)
	if got := call(WebAuthRequest{Op: "allow", Platform: "tg", ExtID: "00042"}); got.Error != "" {
		t.Fatal(got)
	}
	b := WebSecret()
	ch := call(WebAuthRequest{Op: "start", Platform: "tg", Browser: b}).Challenge
	code := call(WebAuthRequest{Op: "issue", Platform: "tg", ExtID: "42", Challenge: ch}).Code
	session := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code}).Token
	if session == "" {
		t.Fatal("new allowlisted identity without configured group could not login")
	}
	if got := call(WebAuthRequest{Op: "session", Token: session}); got.Error != "" || got.GroupID != 0 {
		t.Fatal(got)
	}
	call(WebAuthRequest{Op: "deny", Platform: "tg", ExtID: "42"})
	if got := call(WebAuthRequest{Op: "session", Token: session}); got.Error != "unauthorized" {
		t.Fatal(got)
	}
	if got := call(WebAuthRequest{Op: "allow", Platform: "tg", ExtID: "-1"}); got.Error != "invalid" {
		t.Fatal(got)
	}
}

func TestWebAuthSessionSurvivesRestartAndExpires(t *testing.T) {
	db, call := authFixture(t)
	b, ch, code := loginChallenge(t, call)
	token := call(WebAuthRequest{Op: "finish", Browser: b, Challenge: ch, Code: code}).Token
	// A new reader/writer process must see durable sessions, not an in-memory map.
	var path string
	if err := db.r.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if out, err := reopened.WebAuth(context.Background(), WebAuthRequest{Op: "session", Token: token}); err != nil || out.Error != "" {
		t.Fatal(out, err)
	}
	if err := reopened.tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE web_sessions SET expires=?`, time.Now().Unix()-1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if out, err := reopened.WebAuth(context.Background(), WebAuthRequest{Op: "session", Token: token}); err != nil || out.Error != "unauthorized" {
		t.Fatal(out, err)
	}
}
