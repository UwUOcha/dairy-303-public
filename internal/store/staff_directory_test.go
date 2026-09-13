package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

const legacyStaffSchema = `DROP TABLE staff_unresolved; DROP TABLE staff_aliases; DROP TABLE staff_departments; DROP TABLE staff_accounts; DROP TABLE provider_ids; DROP TABLE staff_vacancies;
 DROP TABLE staff; CREATE TABLE staff(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, apeks_id INTEGER NOT NULL DEFAULT 0);`

func staffFixture() importdata.StaffDirectory {
	return importdata.StaffDirectory{Staff: []importdata.StaffDetails{
		{ID: 65, Name: "Смирнов В.В.", FullName: "Смирнов Владимир Владимирович", Identity: "person:смирнов владимир владимирович", Departments: []string{"Кафедра информатики"}},
		{ID: 709, Name: "Смирнов В.В.", FullName: "Смирнов Виктор Валерьевич", Identity: "person:смирнов виктор валерьевич", Degree: "к.п.н.", Departments: []string{"Кафедра медицины"}},
		{ID: 33, Name: "Герасимова В.И.", FullName: "Герасимова Валентина Ивановна", Identity: "person:герасимова валентина ивановна", Departments: []string{"Кафедра физики и математики "}},
		{ID: 1382, Name: "Герасимова В.И.", FullName: " герасимова Валентина  Ивановна ", Identity: "person:герасимова валентина ивановна", Departments: []string{"Кафедра физики и математики", "Кафедра педагогики"}},
		{ID: 676, Name: "Коненкова Н.В.", FullName: "Коненкова Наталия Викторовна", Identity: "person:коненкова наталия викторовна"},
		{ID: 1001, Name: "Коненкова Н.В.", FullName: "Коненкова Наталья Викторовна", Identity: "person:коненкова наталья викторовна"},
		{ID: 201, Name: "Иванов И.И.", FullName: "Иванов И.И.", Identity: ""},
		{ID: 202, Name: "Иванов И.И.", FullName: "Иванов И.И.", Identity: ""},
	}, Vacancies: []int64{900}}
}
func staffMonth() importdata.MonthSchedule {
	m := importdata.MonthSchedule{GroupID: 231, Year: 2026, Month: 9, LessonTimes: testTimes}
	for i, p := range staffFixture().Staff[:4] {
		subject := "Математика"
		if p.ID == 65 {
			subject = "Большие данные"
		}
		if p.ID == 709 {
			subject = "Информационные технологии в профессиональной деятельности врача"
		}
		m.Lessons = append(m.Lessons, importdata.Lesson{ID: int64(i + 1), Date: "2026-09-07", LessonTimeID: 1, Discipline: subject, Staff: []importdata.Staff{{ID: p.ID, Name: p.Name}}})
	}
	return m
}
func accountPerson(t *testing.T, db *DB, id int64) int64 {
	t.Helper()
	var person int64
	if err := db.r.QueryRow(`SELECT staff_id FROM staff_accounts WHERE account_id=?`, id).Scan(&person); err != nil {
		t.Fatal(err)
	}
	return person
}
func TestStaffIdentityDirectoryAndFallback(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMonth(ctx, staffMonth()); err != nil {
		t.Fatal(err)
	}
	smirnov := accountPerson(t, db, 65)
	other := accountPerson(t, db, 709)
	if smirnov == other {
		t.Fatal("initials merged unknown accounts")
	}
	oldDuplicate := accountPerson(t, db, 1382)
	before, err := db.MonthState(ctx, 231, 2026, 9)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.SaveStaffDirectory(ctx, staffFixture(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if accountPerson(t, db, 65) != smirnov || accountPerson(t, db, 709) != other {
		t.Fatal("public IDs changed")
	}
	if accountPerson(t, db, 33) != accountPerson(t, db, 1382) {
		t.Fatal("confirmed duplicate not merged")
	}
	if accountPerson(t, db, 676) == accountPerson(t, db, 1001) || accountPerson(t, db, 201) == accountPerson(t, db, 202) {
		t.Fatal("guessed identity")
	}
	alias, err := db.Teacher(ctx, oldDuplicate)
	if err != nil {
		t.Fatal(err)
	}
	if alias.ID != accountPerson(t, db, 33) || len(alias.Departments) != 2 {
		t.Fatalf("old link or departments: %+v", alias)
	}
	ls, err := db.TeacherLessons(ctx, oldDuplicate, "2026-09-01", "2026-09-30")
	if err != nil || len(ls) != 2 {
		t.Fatalf("merged lessons: %d %v", len(ls), err)
	}
	ls, err = db.TeacherLessons(ctx, smirnov, "2026-09-01", "2026-09-30")
	if err != nil || len(ls) != 1 || ls[0].Discipline != "Большие данные" {
		t.Fatalf("homonym contamination: %+v %v", ls, err)
	}
	catalog, err := db.SearchTeachers(ctx, "Виктор Валерьевич", "2026-09-01", "2026-09-30", 0)
	if err != nil || len(catalog) != 1 || catalog[0].Degree != "к.п.н." {
		t.Fatalf("full name search: %+v %v", catalog, err)
	}
	catalog, err = db.SearchTeachers(ctx, "", "2026-09-01", "2026-09-30", 0)
	if err != nil || len(catalog) != 3 {
		t.Fatalf("catalog must contain only teachers with lessons: %+v %v", catalog, err)
	}
	after, err := db.MonthState(ctx, 231, 2026, 9)
	if err != nil || before != after {
		t.Fatalf("directory touched schedule state: %+v %+v %v", before, after, err)
	}
	if changed, err := db.SaveMonth(ctx, staffMonth()); err != nil || changed {
		t.Fatalf("identity refresh notified schedule change: %t %v", changed, err)
	}
}

func TestStaffMigrationRepairsUnchangedMonth(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "old.db")
	db, err := Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	m := staffMonth()
	m.Lessons = m.Lessons[:2]
	if _, err := db.SaveMonth(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, legacyStaffSchema+`INSERT INTO staff(id,name,apeks_id) VALUES(77,'Смирнов В.В.',709);
  UPDATE lesson_staff SET staff_id=77; ALTER TABLE users DROP COLUMN tz_name; PRAGMA user_version=10;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if accountPerson(t, db, 709) != 77 {
		t.Fatal("migration lost public ID")
	}
	if err := db.SaveStaffDirectory(ctx, staffFixture(), time.Now()); err != nil {
		t.Fatal(err)
	}
	// The content hash is unchanged: repair uses the ordinary fetch, no forced resync.
	if changed, err := db.SaveMonth(ctx, m); err != nil || changed {
		t.Fatalf("rebind: %t %v", changed, err)
	}
	for _, upstream := range []int64{65, 709} {
		ls, err := db.TeacherLessons(ctx, accountPerson(t, db, upstream), "2026-09-01", "2026-09-30")
		if err != nil || len(ls) != 1 {
			t.Fatalf("legacy homonym was not split: %+v %v", ls, err)
		}
	}
	var violation any
	if err := db.r.QueryRow(`PRAGMA foreign_key_check`).Scan(&violation); err != sql.ErrNoRows {
		t.Fatalf("foreign key check: %v", err)
	}
}

func TestDirectoryCorrectionDoesNotKeepDifferentPeopleMerged(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	directory := importdata.StaffDirectory{Staff: []importdata.StaffDetails{
		{ID: 10, Name: "Смирнов В.В.", FullName: "Смирнов Владимир Владимирович", Identity: "person:смирнов владимир владимирович"},
		{ID: 20, Name: "Смирнов В.В.", FullName: "Смирнов Владимир Владимирович", Identity: "person:смирнов владимир владимирович"},
	}}
	if err := db.SaveStaffDirectory(ctx, directory, time.Now()); err != nil {
		t.Fatal(err)
	}
	old := accountPerson(t, db, 10)
	directory.Staff[1].FullName = "Смирнов Виктор Валерьевич"
	directory.Staff[1].Identity = "person:second"
	if err := db.SaveStaffDirectory(ctx, directory, time.Now()); err != nil {
		t.Fatal(err)
	}
	if accountPerson(t, db, 10) != old || accountPerson(t, db, 20) == old {
		t.Fatal("corrected full names still merged")
	}
	first, err := db.Teacher(ctx, old)
	if err != nil || first.FullName != directory.Staff[0].FullName {
		t.Fatalf("wrong retained identity: %+v %v", first, err)
	}
}

func TestMissingIDsDoNotMergeInitials(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	m := staffMonth()
	m.Lessons = m.Lessons[:2]
	for i := range m.Lessons {
		m.Lessons[i].Staff[0].ID = 0
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := db.SaveMonth(ctx, m); err != nil {
			t.Fatal(err)
		}
		catalog, err := db.SearchTeachers(ctx, "Смирнов", "2026-09-01", "2026-09-30", 0)
		if err != nil || len(catalog) != 2 {
			t.Fatalf("unconfirmed names merged: %+v %v", catalog, err)
		}
		if catalog[0].ID != 1 || catalog[1].ID != 2 {
			t.Fatal("unresolved IDs unstable on refresh")
		}
	}
}

func TestStaffVacanciesAndDuplicateAccountsOnOneLesson(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.SaveGroupTree(ctx, testTree()); err != nil {
		t.Fatal(err)
	}
	m := staffMonth()
	m.Lessons = m.Lessons[:1]
	m.Lessons[0].Staff = []importdata.Staff{{ID: 33, Name: "Герасимова В.И."}, {ID: 1382, Name: "Герасимова В.И."}, {ID: 900, Name: "Свободная ставка"}}
	if _, err := db.SaveMonth(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveStaffDirectory(ctx, staffFixture(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		catalog, err := db.SearchTeachers(ctx, "", "2026-09-01", "2026-09-30", 0)
		if err != nil || len(catalog) != 1 {
			t.Fatalf("vacancy in catalog or duplicate account: %+v %v", catalog, err)
		}
		lessons, err := db.TeacherLessons(ctx, catalog[0].ID, "2026-09-01", "2026-09-30")
		if err != nil || len(lessons) != 1 || len(lessons[0].Staff) != 1 {
			t.Fatalf("duplicate lesson: %+v %v", lessons, err)
		}
		if _, err := db.SaveMonth(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
}
