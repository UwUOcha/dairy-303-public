package store

// Historical schema definitions are immutable: only upgrades of existing databases
// use these names. New installations use the neutral schema.sql directly.
import (
	"context"
	"database/sql"
)

const providerSchema = `CREATE TABLE provider_ids (
 source TEXT NOT NULL, kind TEXT NOT NULL, external_id TEXT NOT NULL, internal_id INTEGER NOT NULL,
 PRIMARY KEY(source,kind,external_id), UNIQUE(kind,internal_id));
ALTER TABLE staff_apeks RENAME TO staff_accounts;
ALTER TABLE staff_accounts RENAME COLUMN apeks_id TO account_id;
ALTER TABLE staff_vacancies RENAME COLUMN apeks_id TO account_id;`

const staffIdentitySchema = `
CREATE TABLE staff_apeks (
 apeks_id INTEGER PRIMARY KEY,
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE
);
CREATE INDEX idx_staff_apeks_person ON staff_apeks(staff_id);
CREATE TABLE staff_departments (
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE,
 department TEXT NOT NULL,
 PRIMARY KEY(staff_id, department)
);
CREATE TABLE staff_aliases (
 id INTEGER PRIMARY KEY,
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE
);
CREATE TABLE staff_vacancies (apeks_id INTEGER PRIMARY KEY);
-- Missing upstream IDs have only a lesson-local identity, never initials as a key.
CREATE TABLE staff_unresolved (
 lesson_id INTEGER NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
 pos INTEGER NOT NULL,
 staff_id INTEGER NOT NULL REFERENCES staff(id) ON DELETE CASCADE,
 PRIMARY KEY(lesson_id,pos)
);
`

func migrateStaffIdentity(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE staff_new (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
 full_name TEXT NOT NULL DEFAULT '', degree TEXT NOT NULL DEFAULT '');
 INSERT INTO staff_new(id,name) SELECT id,name FROM staff;
 CREATE TEMP TABLE staff_old_accounts AS SELECT id,apeks_id FROM staff WHERE apeks_id<>0;
 DROP TABLE staff;
 ALTER TABLE staff_new RENAME TO staff;`+staffIdentitySchema+`
 INSERT INTO staff_apeks(apeks_id,staff_id) SELECT apeks_id,MIN(id) FROM staff_old_accounts GROUP BY apeks_id;
 DROP TABLE staff_old_accounts;`)
	return err
}
