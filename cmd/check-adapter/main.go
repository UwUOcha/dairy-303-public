package main

import (
	"context"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/pkg/provider"
	"os"
	"time"
)

func run() error {
	base := os.Getenv("PROVIDER_URL")
	if base == "" {
		base = "http://127.0.0.1:8310"
	}
	client, e := provider.NewClient(base, os.Getenv("PROVIDER_TOKEN"))
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	info, e := client.Info(ctx)
	if e != nil {
		return e
	}
	loc, e := time.LoadLocation(info.Timezone)
	if e != nil {
		return e
	}
	catalog, e := client.Catalog(ctx)
	if e != nil {
		return e
	}
	var group string
	for _, g := range catalog.Groups {
		if g.Active {
			group = g.ID
			break
		}
	}
	if group == "" {
		return fmt.Errorf("no active group to check")
	}
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 1, -1)
	snapshot, e := client.Schedule(ctx, group, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if e != nil {
		return e
	}
	if info.StaffDirectory {
		if _, e = client.Directory(ctx); e != nil {
			return e
		}
	}
	fmt.Printf("OK · protocol %s · %s · %d groups · %d lessons in checked month\n", info.Version, info.Source, len(catalog.Groups), len(snapshot.Lessons))
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
