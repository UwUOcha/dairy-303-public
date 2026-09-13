package schedule

import (
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"sort"
	"time"
)

// Semester returns the configured academic period containing the date.
func Semester(date string) (from, to string, err error) {
	t, err := ParseDate(date)
	if err != nil {
		return "", "", err
	}
	var starts []time.Time
	for y := t.Year() - 1; y <= t.Year()+1; y++ {
		for _, md := range profile.Current().TermStarts {
			s, _ := time.Parse("2006-01-02", time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006")+"-"+md)
			starts = append(starts, s)
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	for i := 0; i+1 < len(starts); i++ {
		if !t.Before(starts[i]) && t.Before(starts[i+1]) {
			return FormatDate(starts[i]), FormatDate(starts[i+1].AddDate(0, 0, -1)), nil
		}
	}
	return "", "", nil
}
