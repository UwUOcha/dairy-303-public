package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

// saveImportMetadata participates in the same transaction as month replacement.
func saveImportMetadata(ctx context.Context, tx *sql.Tx, ms importdata.MonthSchedule) error {
	ids := []int64{}
	for _, sub := range ms.Subgroups {
		var owner int64
		e := tx.QueryRowContext(ctx, `SELECT group_id FROM subgroups WHERE id=?`, sub.ID).Scan(&owner)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		if e == nil && owner != ms.GroupID {
			return fmt.Errorf("subgroup identity belongs to another group")
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO subgroups(id,group_id,name) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name`, sub.ID, ms.GroupID, sub.Name); e != nil {
			return e
		}
		ids = append(ids, sub.ID)
	}
	active, _ := json.Marshal(ids)
	if _, e := tx.ExecContext(ctx, `UPDATE users SET subgroup_id=0 WHERE group_id=? AND subgroup_id<>0 AND subgroup_id NOT IN(SELECT value FROM json_each(?))`, ms.GroupID, string(active)); e != nil {
		return e
	}
	// Retain historical subgroup names, but expose only the current catalog in the picker.
	if _, e := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprintf("active_subgroups:%d", ms.GroupID), string(active)); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,'1')`, fmt.Sprintf("subgroups_ready:%d", ms.GroupID)); e != nil {
		return e
	}
	if e := applyGroupReplacements(ctx, tx); e != nil {
		return e
	}
	if len(ms.Visibility) > 0 {
		update, e := tx.PrepareContext(ctx, `UPDATE groups SET shadowed=? WHERE id=? AND shadowed<>?`)
		if e != nil {
			return e
		}
		defer update.Close()
		for id, hidden := range ms.Visibility {
			if _, e := update.ExecContext(ctx, hidden, id, hidden); e != nil {
				return e
			}
		}
	}
	return nil
}
