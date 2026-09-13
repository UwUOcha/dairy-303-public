package store

import (
	"context"
	"database/sql"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"time"
)

func migrateUserTimezone(ctx context.Context, tx *sql.Tx) error {
	if _, e := tx.ExecContext(ctx, `ALTER TABLE users ADD COLUMN tz_name TEXT NOT NULL DEFAULT ''`); e != nil {
		return e
	}
	p := profile.Current()
	_, e := tx.ExecContext(ctx, `UPDATE users SET tz_name=? WHERE tz_offset=?`, p.Timezone, p.Offset())
	return e
}

// RefreshUserTimezones updates only offsets that changed, including DST transitions.
func (db *DB) RefreshUserTimezones(ctx context.Context, at time.Time) error {
	rows, e := db.r.QueryContext(ctx, `SELECT DISTINCT tz_name FROM users WHERE tz_name<>''`)
	if e != nil {
		return e
	}
	var names []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			rows.Close()
			return e
		}
		names = append(names, name)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(names) == 0 {
		return nil
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		for _, name := range names {
			loc, e := time.LoadLocation(name)
			if e != nil {
				return e
			}
			_, offset := at.In(loc).Zone()
			if _, e = tx.ExecContext(ctx, `UPDATE users SET tz_offset=? WHERE tz_name=? AND tz_offset<>?`, offset/60, name, offset/60); e != nil {
				return e
			}
		}
		return nil
	})
}
