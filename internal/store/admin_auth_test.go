package store

import (
	"context"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"testing"
	"time"
)

func TestAdminLoginUsesVerifiedBotIdentityWithoutPublicAllowlist(t *testing.T) {
	old := profile.Current()
	p := old
	p.AdminTG = "777"
	profile.Set(p)
	defer profile.Set(old)
	ctx := context.Background()
	db := openTest(t)
	browser := WebSecret()
	start, e := db.WebAuth(ctx, WebAuthRequest{Op: "start", Platform: "tg", Browser: browser})
	if e != nil || start.Error != "" {
		t.Fatal(start, e)
	}
	proof, e := db.WebAuth(ctx, WebAuthRequest{Op: "issue", Platform: "tg", ExtID: "777", Challenge: start.Challenge})
	if e != nil || proof.Error != "" {
		t.Fatal(proof, e)
	}
	finish, e := db.WebAuth(ctx, WebAuthRequest{Op: "finish", Browser: browser, Challenge: start.Challenge, Code: proof.Code})
	if e != nil || finish.Error != "" {
		t.Fatal(finish, e)
	}
	session, e := db.WebAuth(ctx, WebAuthRequest{Op: "session", Token: finish.Token})
	if e != nil || session.ExtID != "777" || !session.Admin {
		t.Fatal(session, e)
	}
	stats, e := db.Stats(ctx, time.Now(), 0)
	if e != nil || stats.Web.SignedIn != 1 || stats.Web.Sessions != 1 || stats.Web.Allowed != 1 {
		t.Fatal("administrator missing from website statistics", stats, e)
	}
	p.AdminTG = "778"
	profile.Set(p)
	session, e = db.WebAuth(ctx, WebAuthRequest{Op: "session", Token: finish.Token})
	if e != nil || session.Error != "unauthorized" {
		t.Fatal("removed administrator retains access", session, e)
	}
}
