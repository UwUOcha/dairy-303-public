package store

import "time"

var nowFunc = time.Now

func activeSince(now time.Time) string { return now.Format("2006-01") + "-01" }
