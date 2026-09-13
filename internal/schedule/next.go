package schedule

import "time"

// NextLesson ищет ещё не начавшееся занятие по календарным дням, включая
// воскресные переносы. Самоподготовка и дистанционные занятия тоже занимают время.
func NextLesson(lessons []Lesson, grid Grid, subgroup int64, now time.Time, days int) (Day, *Item) {
	for i := 0; i < days; i++ {
		d := BuildDay(FormatDate(now.AddDate(0, 0, i)), lessons, grid, subgroup)
		for j := range d.Items {
			it := &d.Items[j]
			if it.Flags.Has(FlagEmpty) || it.Flags.Has(FlagNonStudy) {
				continue
			}
			if i == 0 && it.MinuteFrom < now.Hour()*60+now.Minute() {
				continue
			}
			return d, it
		}
	}
	return Day{}, nil
}
