package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

const MetaStaffSyncedAt = "staff_synced_at"
const MetaStaffAttemptedAt = "staff_attempted_at"

// SaveStaffDirectory enriches and merges identities using full names only.
// Schedule downloads and month-state hashes are deliberately untouched.
func (db *DB) SaveStaffDirectory(ctx context.Context, directory importdata.StaffDirectory, fetched time.Time) error {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type person struct {
		id   int64
		full string
	}
	people := map[int64]person{}
	rows, err := tx.QueryContext(ctx, `SELECT id,full_name FROM staff`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p person
		if err := rows.Scan(&p.id, &p.full); err != nil {
			rows.Close()
			return err
		}
		people[p.id] = p
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	accounts := map[int64]int64{}
	rows, err = tx.QueryContext(ctx, `SELECT account_id,staff_id FROM staff_accounts`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			rows.Close()
			return err
		}
		accounts[a] = b
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	// Group accounts before touching identities; iteration order cannot split a duplicate.
	groups := map[string][]importdata.StaffDetails{}
	for _, p := range directory.Staff {
		key := fmt.Sprintf("id:%d", p.ID)
		if p.Identity != "" {
			key = "identity:" + p.Identity
		}
		groups[key] = append(groups[key], p)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// If an account previously merged by full name is corrected, reserve the
	// old public ID for one identity only. The next ordinary schedule refresh
	// restores the per-lesson association, which older databases did not keep.
	accountGroups := map[int64]string{}
	for key, entries := range groups {
		for _, entry := range entries {
			accountGroups[entry.ID] = key
		}
	}
	owners := map[int64]string{}
	ownerAccount := map[int64]int64{}
	for account, id := range accounts {
		key, present := accountGroups[account]
		if !present {
			continue
		}
		if owners[id] == "" || account < ownerAccount[id] {
			owners[id], ownerAccount[id] = key, account
		}
	}
	for _, key := range keys {
		entries := groups[key]
		sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
		first := entries[0]
		candidates := map[int64]bool{}

		for _, entry := range entries {
			if id := accounts[entry.ID]; id != 0 && owners[id] == key {
				// A changed full name is a correction to this known account. Only merge
				// another person when the new directory explicitly confirms the identity.
				candidates[id] = true
			}
		}
		var canonical int64
		for id := range candidates {
			if canonical == 0 || id < canonical {
				canonical = id
			}
		}
		if canonical == 0 {
			result, err := tx.ExecContext(ctx, `INSERT INTO staff(name) VALUES(?)`, first.Name)
			if err != nil {
				return err
			}
			canonical, err = result.LastInsertId()
			if err != nil {
				return err
			}
		}
		for id := range candidates {
			if id == canonical {
				continue
			}
			for _, stmt := range []string{
				`INSERT INTO lesson_staff(lesson_id,staff_id,pos) SELECT lesson_id,?,pos FROM lesson_staff WHERE staff_id=? ON CONFLICT(lesson_id,staff_id) DO UPDATE SET pos=MIN(pos,excluded.pos)`,
				`UPDATE staff_accounts SET staff_id=? WHERE staff_id=?`,
				`UPDATE staff_aliases SET staff_id=? WHERE staff_id=?`,
				`UPDATE staff_unresolved SET staff_id=? WHERE staff_id=?`,
				`INSERT OR REPLACE INTO staff_aliases(staff_id,id) VALUES(?,?)`,
			} {
				if _, err := tx.ExecContext(ctx, stmt, canonical, id); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM lesson_staff WHERE staff_id=?`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM staff WHERE id=?`, id); err != nil {
				return err
			}
			delete(people, id)
			for account, owner := range accounts {
				if owner == id {
					accounts[account] = canonical
				}
			}
		}
		degree := ""
		for _, entry := range entries {
			if entry.Degree != "" {
				degree = entry.Degree
				break
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE staff SET name=?,full_name=?,degree=? WHERE id=?`, first.Name, importdata.CleanStaffText(first.FullName), degree, canonical); err != nil {
			return err
		}
		people[canonical] = person{canonical, first.FullName}
		if _, err := tx.ExecContext(ctx, `DELETE FROM staff_departments WHERE staff_id=?`, canonical); err != nil {
			return err
		}
		for _, entry := range entries {
			if _, err := tx.ExecContext(ctx, `INSERT INTO staff_accounts(account_id,staff_id) VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET staff_id=excluded.staff_id`, entry.ID, canonical); err != nil {
				return err
			}
			accounts[entry.ID] = canonical
			for _, department := range entry.Departments {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO staff_departments(staff_id,department) VALUES(?,?)`, canonical, importdata.CleanStaffText(department)); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM staff_vacancies`); err != nil {
		return err
	}
	for _, id := range directory.Vacancies {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO staff_vacancies VALUES(?)`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM lesson_staff WHERE staff_id IN (SELECT staff_id FROM staff_accounts WHERE account_id=?)`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, MetaStaffSyncedAt, fmt.Sprint(fetched.Unix())); err != nil {
		return err
	}
	return tx.Commit()
}

// Unknown IDs stay separate. A name-only record is never attached to an ID
// by initials; even without the directory, homonyms cannot absorb each other.
func (d *dicts) staffID(s importdata.Staff, lessonID int64, pos int) (sql.NullInt64, error) {
	key := fmt.Sprintf("staff-id:%d", s.ID)
	unresolved := s.ID <= 0
	if s.ID <= 0 {
		key = "staff-name:" + s.Name
	}
	if unresolved {
		key = fmt.Sprintf("staff-unresolved:%d:%d", lessonID, pos)
	}
	if id, ok := d.cache[key]; ok {
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	var id int64
	var err error
	if s.ID > 0 {
		var excluded int
		err = d.tx.QueryRowContext(d.ctx, `SELECT 1 FROM staff_vacancies WHERE account_id=?`, s.ID).Scan(&excluded)
		if err == nil {
			return sql.NullInt64{}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return sql.NullInt64{}, err
		}
		err = d.tx.QueryRowContext(d.ctx, `SELECT staff_id FROM staff_accounts WHERE account_id=?`, s.ID).Scan(&id)
	} else if unresolved {
		err = d.tx.QueryRowContext(d.ctx, `SELECT s.id FROM staff_unresolved u JOIN staff s ON s.id=u.staff_id WHERE u.lesson_id=? AND u.pos=? AND s.name=?`, lessonID, pos, s.Name).Scan(&id)
	} else {
		err = d.tx.QueryRowContext(d.ctx, `SELECT id FROM staff WHERE name=? AND full_name='' AND NOT EXISTS(SELECT 1 FROM staff_accounts a WHERE a.staff_id=staff.id) ORDER BY id LIMIT 1`, s.Name).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if s.Name == "" {
			return sql.NullInt64{}, nil
		}
		result, e := d.tx.ExecContext(d.ctx, `INSERT INTO staff(name) VALUES(?)`, s.Name)
		if e != nil {
			return sql.NullInt64{}, e
		}
		id, err = result.LastInsertId()
		if err == nil && unresolved {
			_, err = d.tx.ExecContext(d.ctx, `INSERT INTO staff_unresolved(lesson_id,pos,staff_id) VALUES(?,?,?) ON CONFLICT(lesson_id,pos) DO UPDATE SET staff_id=excluded.staff_id`, lessonID, pos, id)
		}
		if err == nil && s.ID > 0 {
			_, err = d.tx.ExecContext(d.ctx, `INSERT INTO staff_accounts(account_id,staff_id) VALUES(?,?)`, s.ID, id)
		}
	}
	if err != nil {
		return sql.NullInt64{}, err
	}
	d.cache[key] = id
	return sql.NullInt64{Int64: id, Valid: true}, nil
}
