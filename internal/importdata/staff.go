package importdata

import (
	"strings"
)

// StaffDetails describes an upstream account, not a unique person.
type StaffDetails struct {
	Identity               string // Explicit identity supplied by the adapter; empty means separate account.
	ID                     int64
	Name, FullName, Degree string
	Departments            []string
}
type StaffDirectory struct {
	FetchedAt string
	Staff     []StaffDetails
	Vacancies []int64
}

func CleanStaffText(s string) string { return strings.Join(strings.Fields(s), " ") }
