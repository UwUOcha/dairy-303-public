package schedule

import "testing"

func TestSemesterBounds(t *testing.T) {
	cases := []struct{ date, from, to string }{
		{"2026-09-01", "2026-09-01", "2027-01-31"},
		{"2026-12-31", "2026-09-01", "2027-01-31"},
		// Январь принадлежит осеннему полугодию, а не начинает весеннее:
		// сессия сдаётся по предметам, которые читали с сентября.
		{"2027-01-15", "2026-09-01", "2027-01-31"},
		{"2027-02-01", "2027-02-01", "2027-08-31"},
		{"2027-08-31", "2027-02-01", "2027-08-31"},
	}
	for _, c := range cases {
		from, to, err := Semester(c.date)
		if err != nil || from != c.from || to != c.to {
			t.Fatalf("Semester(%s) = %s..%s, %v; ожидалось %s..%s", c.date, from, to, err, c.from, c.to)
		}
	}
	if _, _, err := Semester("не дата"); err == nil {
		t.Fatal("некорректная дата должна быть ошибкой")
	}
}
