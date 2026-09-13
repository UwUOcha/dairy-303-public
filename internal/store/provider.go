package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// BindProvider prevents accidentally importing a different university over live users.
func (db *DB) BindProvider(ctx context.Context, source string, legacy bool) error {
	var saved string
	e := db.r.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='provider_source'`).Scan(&saved)
	if e == nil {
		if saved != source {
			return fmt.Errorf("database belongs to provider %q, received %q", saved, source)
		}
		return nil
	}
	if e != sql.ErrNoRows {
		return e
	}

	return db.tx(ctx, func(tx *sql.Tx) error {
		var saved string
		e := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='provider_source'`).Scan(&saved)
		if e == nil {
			if saved != source {
				return fmt.Errorf("database belongs to provider %q, received %q", saved, source)
			}
			return nil
		}
		if e != sql.ErrNoRows {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM groups`).Scan(&count); e != nil {
			return e
		}
		if count > 0 && !legacy {
			return fmt.Errorf("existing catalog requires explicit RASP_IMPORT_LEGACY_IDS=true for migration")
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('provider_source',?)`, source)
		return e
	})
}

// ProviderID preserves identities across restarts. Legacy numeric IDs are adopted only
// during an explicitly requested migration; arbitrary external IDs never become SQL IDs.
type ProviderIdentity struct{ Kind, External string }

func (db *DB) ProviderID(ctx context.Context, source, kind, external string, legacy bool) (int64, error) {
	key := ProviderIdentity{kind, external}
	ids, err := db.ProviderIDs(ctx, source, []ProviderIdentity{key}, legacy)
	return ids[key], err
}

// ProviderIDs resolves an import in one transaction, and uses only the reader pool
// when all identities already exist. Recheck misses inside the transaction so
// concurrent imports cannot allocate the same ID or race on an external identity.
func (db *DB) ProviderIDs(ctx context.Context, source string, keys []ProviderIdentity, legacy bool) (map[ProviderIdentity]int64, error) {
	ids := make(map[ProviderIdentity]int64, len(keys))
	var missing []ProviderIdentity
	seen := map[ProviderIdentity]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		if key.External == "" {
			ids[key] = 0
			continue
		}
		var id int64
		err := db.r.QueryRowContext(ctx, `SELECT internal_id FROM provider_ids WHERE source=? AND kind=? AND external_id=?`, source, key.Kind, key.External).Scan(&id)
		if err == nil {
			ids[key] = id
		} else if err == sql.ErrNoRows {
			missing = append(missing, key)
		} else {
			return nil, err
		}
	}
	if len(missing) == 0 {
		return ids, nil
	}
	err := db.tx(ctx, func(tx *sql.Tx) error {
		for _, key := range missing {
			id, err := providerID(ctx, tx, source, key.Kind, key.External, legacy)
			if err != nil {
				return err
			}
			ids[key] = id
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func providerID(ctx context.Context, tx *sql.Tx, source, kind, external string, legacy bool) (id int64, err error) {
	if external == "" {
		return 0, nil
	}
	tables := map[string]string{"group": "groups", "department": "departments", "subgroup": "subgroups", "lesson": "lessons", "slot": "lesson_times", "teacher": "staff_accounts"}
	table, ok := tables[kind]
	if !ok {
		return 0, fmt.Errorf("unknown identity kind")
	}
	column := "id"
	if kind == "teacher" {
		column = "account_id"
	}
	err = func() error {
		e := tx.QueryRowContext(ctx, `SELECT internal_id FROM provider_ids WHERE source=? AND kind=? AND external_id=?`, source, kind, external).Scan(&id)
		if e == nil {
			return nil
		}
		if e != sql.ErrNoRows {
			return e
		}
		if legacy {
			n, e := strconv.ParseInt(external, 10, 64)
			if e == nil && n > 0 && strconv.FormatInt(n, 10) == external {
				var occupied int
				if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM provider_ids WHERE kind=? AND internal_id=?`, kind, n).Scan(&occupied); e != nil {
					return e
				}
				if occupied == 0 {
					id = n
				}
			}
		}
		if id == 0 {
			if e = tx.QueryRowContext(ctx, `SELECT MAX(COALESCE((SELECT MAX(`+column+`) FROM `+table+`),0),COALESCE((SELECT MAX(internal_id) FROM provider_ids WHERE kind=?),0))+1`, kind).Scan(&id); e != nil {
				return e
			}
		}
		if id > 1_000_000_000 && (kind == "group" || kind == "subgroup" || kind == "department") {
			return fmt.Errorf("identity exceeds public API range")
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO provider_ids(source,kind,external_id,internal_id) VALUES(?,?,?,?)`, source, kind, external, id)
		return e
	}()
	return
}
func (db *DB) ProviderExternalID(ctx context.Context, source, kind string, id int64) (external string, err error) {
	err = db.r.QueryRowContext(ctx, `SELECT external_id FROM provider_ids WHERE source=? AND kind=? AND internal_id=?`, source, kind, id).Scan(&external)
	return
}
func (db *DB) ApplyVisibility(ctx context.Context, values map[int64]bool) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		for id, hidden := range values {
			if _, e := tx.ExecContext(ctx, `UPDATE groups SET shadowed=? WHERE id=?`, hidden, id); e != nil {
				return e
			}
		}
		return nil
	})
}

// CatalogRecords returns persisted catalog entries, including inactive ones.
type CatalogRecord struct {
	ID             int64
	Name           string
	Active, Hidden bool
}

func (db *DB) CatalogRecords(ctx context.Context) (out []CatalogRecord, err error) {
	rows, e := db.r.QueryContext(ctx, `SELECT id,name,is_active,shadowed FROM groups`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var r CatalogRecord
		if e = rows.Scan(&r.ID, &r.Name, &r.Active, &r.Hidden); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ProviderSlotKey makes time immutable. Reusing a source slot with different times
// creates a new local slot, so old weeks and other groups retain their times.
func (db *DB) ProviderSlotKey(ctx context.Context, external, start, end string, legacy bool) (string, error) {
	if legacy && external != "" {
		id, e := strconv.ParseInt(external, 10, 64)
		if e == nil && id > 0 {
			var a, b int
			e = db.r.QueryRowContext(ctx, `SELECT minute_from,minute_to FROM lesson_times WHERE id=?`, id).Scan(&a, &b)
			if e == sql.ErrNoRows {
				return external, nil
			}
			if e != nil {
				return "", e
			}
			if fmt.Sprintf("%02d:%02d", a/60, a%60) == start && fmt.Sprintf("%02d:%02d", b/60, b%60) == end {
				return external, nil
			}
		}
	}
	return "time:" + external + ":" + start + "/" + end, nil
}
