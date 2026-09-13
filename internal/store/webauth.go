package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"math/big"
	"strconv"
	"time"
)

const WebSessionDays = 60
const WebDeviceLimit = 4
const webAuthSchema = `
CREATE TABLE IF NOT EXISTS web_allowlist(platform TEXT NOT NULL, ext_id TEXT NOT NULL, added_at INTEGER NOT NULL, PRIMARY KEY(platform,ext_id));
CREATE TABLE IF NOT EXISTS web_challenges(id TEXT PRIMARY KEY, browser TEXT NOT NULL, platform TEXT NOT NULL, ext_id TEXT NOT NULL DEFAULT '', code TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS web_sessions(id TEXT PRIMARY KEY, token TEXT NOT NULL UNIQUE, platform TEXT NOT NULL, ext_id TEXT NOT NULL, label TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS web_sessions_owner ON web_sessions(platform,ext_id);
`

type WebAuthRequest struct {
	Op        string `json:"op"`
	Platform  string `json:"platform,omitempty"`
	ExtID     string `json:"ext_id,omitempty"`
	Challenge string `json:"challenge,omitempty"`
	Browser   string `json:"browser,omitempty"`
	Code      string `json:"code,omitempty"`
	Token     string `json:"token,omitempty"`
	Label     string `json:"label,omitempty"`
	ID        string `json:"id,omitempty"`
}
type WebDevice struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
	Current bool   `json:"current"`
}
type WebAuthResponse struct {
	ExtID      string      `json:"ext_id,omitempty"`
	Admin      bool        `json:"admin,omitempty"`
	Error      string      `json:"error,omitempty"`
	Challenge  string      `json:"challenge,omitempty"`
	Code       string      `json:"code,omitempty"`
	Token      string      `json:"token,omitempty"`
	Platform   string      `json:"platform,omitempty"`
	GroupID    int64       `json:"group_id,omitempty"`
	SubgroupID int64       `json:"subgroup_id,omitempty"`
	Devices    []WebDevice `json:"devices,omitempty"`
}

func WebSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func webHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

// WebAuth is private socket RPC. All capacity checks, consumption and revocation
// share the single writer transaction. Business denials commit failed attempts.
func (db *DB) WebAuth(ctx context.Context, in WebAuthRequest) (out WebAuthResponse, err error) {
	now := time.Now().Unix()
	err = db.tx(ctx, func(tx *sql.Tx) error {
		deny := func(s string) error { out.Error = s; return nil }
		allowed := func(p, e string) bool {
			if profile.Current().IsAdmin(p, e) {
				return true
			}
			var n int
			return tx.QueryRowContext(ctx, `SELECT 1 FROM web_allowlist WHERE platform=? AND ext_id=?`, p, e).Scan(&n) == nil
		}
		switch in.Op {
		// Administrative operations are reachable only on the private Unix socket.
		case "allow", "deny":
			id, err := strconv.ParseInt(in.ExtID, 10, 64)
			if err != nil || id <= 0 || (in.Platform != "tg" && in.Platform != "vk") {
				return deny("invalid")
			}
			if in.Op == "allow" {
				_, err = tx.ExecContext(ctx, `INSERT INTO web_allowlist(platform,ext_id,added_at) VALUES(?,?,?) ON CONFLICT(platform,ext_id) DO NOTHING`, in.Platform, strconv.FormatInt(id, 10), now)
				return err
			}
			in.ExtID = strconv.FormatInt(id, 10)
			for _, table := range []string{"web_allowlist", "web_sessions", "web_challenges"} {
				if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE platform=? AND ext_id=?`, in.Platform, in.ExtID); err != nil {
					return err
				}
			}
			return nil

		case "start":
			if (in.Platform != "tg" && in.Platform != "vk") || len(in.Browser) != 43 {
				return deny("invalid")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM web_challenges WHERE expires<=? OR browser=?`, now, webHash(in.Browser)); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM web_challenges`).Scan(&n); err != nil {
				return err
			}
			if n >= 1000 {
				return deny("busy")
			}
			out.Challenge = WebSecret()
			_, err := tx.ExecContext(ctx, `INSERT INTO web_challenges(id,browser,platform,expires) VALUES(?,?,?,?)`, webHash(out.Challenge), webHash(in.Browser), in.Platform, now+300)
			return err
		case "issue":
			if !allowed(in.Platform, in.ExtID) {
				return deny("not_allowed")
			}
			var owner string
			if tx.QueryRowContext(ctx, `SELECT ext_id FROM web_challenges WHERE id=? AND platform=? AND expires>? AND attempts<5`, webHash(in.Challenge), in.Platform, now).Scan(&owner) != nil {
				return deny("expired")
			}
			if owner != "" {
				return deny("issued")
			}
			n, err := rand.Int(rand.Reader, big.NewInt(100000000))
			if err != nil {
				return err
			}
			out.Code = fmt.Sprintf("%08d", n.Int64())
			_, err = tx.ExecContext(ctx, `UPDATE web_challenges SET ext_id=?,code=? WHERE id=?`, in.ExtID, webHash(in.Challenge+out.Code), webHash(in.Challenge))
			return err
		case "finish":
			var p, e, code string
			var attempts int
			if tx.QueryRowContext(ctx, `SELECT platform,ext_id,code,attempts FROM web_challenges WHERE id=? AND browser=? AND expires>?`, webHash(in.Challenge), webHash(in.Browser), now).Scan(&p, &e, &code, &attempts) != nil {
				return deny("expired")
			}
			if attempts >= 5 {
				return deny("expired")
			}
			if e == "" {
				return deny("pending")
			}
			if subtle.ConstantTimeCompare([]byte(code), []byte(webHash(in.Challenge+in.Code))) != 1 {
				if _, err := tx.ExecContext(ctx, `UPDATE web_challenges SET attempts=attempts+1 WHERE id=?`, webHash(in.Challenge)); err != nil {
					return err
				}
				return deny("code")
			}
			if !allowed(p, e) {
				return deny("not_allowed")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires<=?`, now); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM web_sessions WHERE platform=? AND ext_id=?`, p, e).Scan(&n); err != nil {
				return err
			}
			if n >= WebDeviceLimit {
				return deny("device_limit")
			}
			out.Token = WebSecret()
			if _, err := tx.ExecContext(ctx, `INSERT INTO web_sessions(id,token,platform,ext_id,label,created,expires) VALUES(?,?,?,?,?,?,?)`, WebSecret(), webHash(out.Token), p, e, in.Label, now, now+WebSessionDays*86400); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM web_challenges WHERE id=?`, webHash(in.Challenge))
			return err
		case "bot_revoke":
			if !allowed(in.Platform, in.ExtID) {
				return deny("not_allowed")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM web_challenges WHERE platform=? AND ext_id=?`, in.Platform, in.ExtID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE platform=? AND ext_id=?`, in.Platform, in.ExtID)
			return err
		case "session", "devices", "revoke", "logout":
			if len(in.Token) != 43 {
				return deny("unauthorized")
			}
			var p, e, current string
			if tx.QueryRowContext(ctx, `SELECT platform,ext_id,id FROM web_sessions WHERE token=? AND expires>?`, webHash(in.Token), now).Scan(&p, &e, &current) != nil || !allowed(p, e) {
				return deny("unauthorized")
			}
			out.Platform = p
			out.ExtID = e
			out.Admin = profile.Current().IsAdmin(p, e)
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT group_id FROM users WHERE platform=?1 AND ext_id=?2),0),COALESCE((SELECT subgroup_id FROM users WHERE platform=?1 AND ext_id=?2),0)`, p, e).Scan(&out.GroupID, &out.SubgroupID); err != nil {
				return err
			}
			if in.Op == "logout" {
				in.ID = current
			}
			if in.Op == "revoke" || in.Op == "logout" {
				_, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE id=? AND platform=? AND ext_id=?`, in.ID, p, e)
				return err
			}
			if in.Op == "devices" {
				rows, err := tx.QueryContext(ctx, `SELECT id,label,created,expires FROM web_sessions WHERE platform=? AND ext_id=? AND expires>? ORDER BY created DESC`, p, e, now)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var d WebDevice
					if err := rows.Scan(&d.ID, &d.Label, &d.Created, &d.Expires); err != nil {
						return err
					}
					d.Current = d.ID == current
					out.Devices = append(out.Devices, d)
				}
				return rows.Err()
			}
			return nil
		default:
			return deny("invalid")
		}
	})
	return
}
