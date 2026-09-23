package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

func saveGroupReplacements(ctx context.Context, tx *sql.Tx, rs []importdata.Replacement) error {
	for _, r := range rs {
		body, _ := json.Marshal(r)
		if _, e := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprintf("group_replacement:%d", r.From), string(body)); e != nil {
			return e
		}
	}
	return nil
}
func applyGroupReplacements(ctx context.Context, tx *sql.Tx) error {
	rows, e := tx.QueryContext(ctx, `SELECT value FROM meta WHERE key GLOB 'group_replacement:*'`)
	if e != nil {
		return e
	}
	var rs []importdata.Replacement
	for rows.Next() {
		var body string
		if e = rows.Scan(&body); e != nil {
			rows.Close()
			return e
		}
		var r importdata.Replacement
		if e = json.Unmarshal([]byte(body), &r); e != nil {
			rows.Close()
			return e
		}
		rs = append(rs, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(rs) == 0 {
		return nil
	}
	// Most imports have no users left in any replacement source. Read their
	// groups once instead of executing three statements for every settled rule.
	rows, e = tx.QueryContext(ctx, `SELECT DISTINCT group_id FROM users`)
	if e != nil {
		return e
	}
	occupied := map[int64]bool{}
	for rows.Next() {
		var group int64
		if e = rows.Scan(&group); e != nil {
			rows.Close()
			return e
		}
		occupied[group] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	// Iterate to resolve chains regardless of the order of external IDs.
	for range rs {
		var moved int64
		for _, r := range rs {
			if !occupied[r.From] {
				continue
			}
			var ready bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meta WHERE key=?) OR EXISTS(SELECT 1 FROM subgroups WHERE group_id=?)`, fmt.Sprintf("subgroups_ready:%d", r.To), r.To).Scan(&ready); e != nil {
				return e
			}
			// Wait for target subgroups before moving a configured subgroup. Its choice is preserved.
			condition := `group_id=?`
			args := []any{r.From}
			if r.MatchSubgroupsByName && !ready {
				condition += ` AND subgroup_id=0`
			}
			if _, e = tx.ExecContext(ctx, `DELETE FROM outbox WHERE kind='change' AND EXISTS(SELECT 1 FROM users u WHERE u.platform=outbox.platform AND u.ext_id=outbox.ext_id AND `+condition+`)`, args...); e != nil {
				return e
			}
			expr := `0`
			updateArgs := []any{r.To}
			if r.MatchSubgroupsByName {
				expr = `COALESCE((SELECT MIN(n.id) FROM subgroups old JOIN subgroups n ON n.name=old.name AND n.group_id=? WHERE old.id=users.subgroup_id),0)`
				updateArgs = append(updateArgs, r.To)
			}
			updateArgs = append(updateArgs, args...)
			result, err := tx.ExecContext(ctx, `UPDATE users SET group_id=?,subgroup_id=`+expr+` WHERE `+condition, updateArgs...)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			moved += count
			if count > 0 {
				// A later rule (or pass) must see users arriving in this group.
				// Keep the source marked too: subgroup imports can defer some users.
				occupied[r.To] = true
			}
		}
		// Once a full pass moves nobody, no remaining chain can advance.
		// Most month imports have no affected users at all; repeating every
		// rule len(rs) times made this ordinary case quadratic.
		if moved == 0 {
			break
		}
	}
	return nil
}
func (db *DB) SubgroupsImported(ctx context.Context, group int64) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,'1')`, fmt.Sprintf("subgroups_ready:%d", group)); e != nil {
			return e
		}
		return applyGroupReplacements(ctx, tx)
	})
}
