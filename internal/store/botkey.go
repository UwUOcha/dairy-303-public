package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"strings"
	"time"
)

// The bot API has exactly one owner and one key for the entire installation.
func BotKeyOwner() string { return profile.Current().BotKeyOwner }

const BotKeyPrefix = "mp_bot_"

const botKeySchema = `CREATE TABLE IF NOT EXISTS bot_api_key (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  platform TEXT NOT NULL CHECK(platform = 'tg'),
  ext_id TEXT NOT NULL CHECK(ext_id <> ''),
  token_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL
);`

type BotKeyResponse struct {
	Active    bool   `json:"active"`
	CreatedAt int64  `json:"created_at,omitempty"`
	Token     string `json:"token,omitempty"`
}

// ManageBotKey is called only through the private socket CLI endpoint.
// Creation never replaces an existing key; rotation is explicit and atomic.
func (db *DB) ManageBotKey(ctx context.Context, op string) (out BotKeyResponse, err error) {
	err = db.tx(ctx, func(tx *sql.Tx) error {
		switch op {
		case "status":
			err := tx.QueryRowContext(ctx, `SELECT created_at FROM bot_api_key WHERE singleton=1`).Scan(&out.CreatedAt)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			out.Active = err == nil
			return err
		case "revoke":
			_, err := tx.ExecContext(ctx, `DELETE FROM bot_api_key`)
			return err
		case "create", "rotate":
			if BotKeyOwner() == "" {
				return errors.New("bot_key_owner is not configured in installation profile")
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM bot_api_key`).Scan(&n); err != nil {
				return err
			}
			if op == "create" && n != 0 {
				return errors.New("ключ уже существует; используйте rotate для перевыпуска")
			}
			var group int64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(group_id,0) FROM users WHERE platform='tg' AND ext_id=?`, BotKeyOwner()).Scan(&group); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errors.New("сначала настройте расписание в Telegram-аккаунте владельца API-ключа")
				}
				return err
			}
			if group <= 0 {
				return errors.New("сначала выберите группу в Telegram-боте")
			}
			out.Token = BotKeyPrefix + WebSecret()
			out.CreatedAt = time.Now().Unix()
			out.Active = true
			_, err := tx.ExecContext(ctx, `INSERT INTO bot_api_key(singleton,platform,ext_id,token_hash,created_at) VALUES(1,'tg',?,?,?) ON CONFLICT(singleton) DO UPDATE SET ext_id=excluded.ext_id,token_hash=excluded.token_hash,created_at=excluded.created_at`, BotKeyOwner(), webHash(out.Token), out.CreatedAt)
			return err
		default:
			return errors.New("используйте create, rotate, revoke или status")
		}
	})
	if err != nil {
		out = BotKeyResponse{}
	}
	return
}

// BotKeyProfile reads the current binding on every request, without touching
// user activity, notification settings or browser sessions.
func (db *DB) BotKeyProfile(ctx context.Context, token string) (group, subgroup int64, err error) {
	if len(token) != len(BotKeyPrefix)+43 || !strings.HasPrefix(token, BotKeyPrefix) {
		return 0, 0, ErrNotFound
	}
	err = db.r.QueryRowContext(ctx, `SELECT COALESCE(u.group_id,0),COALESCE(u.subgroup_id,0)
		FROM bot_api_key k LEFT JOIN users u ON u.platform=k.platform AND u.ext_id=k.ext_id
		WHERE k.singleton=1 AND k.platform='tg' AND k.ext_id=? AND k.token_hash=?`, BotKeyOwner(), webHash(token)).Scan(&group, &subgroup)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}
