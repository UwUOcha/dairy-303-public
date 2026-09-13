package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"net/http"
)

// The synchronizer's tests control a normalized source; university parsing is
// tested in the adapter, HTTP contract validation in providerclient.
type fixtureSource struct {
	base  string
	staff bool
}

func (f fixtureSource) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", f.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return importdata.ErrAuth
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("source: %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
func (f fixtureSource) GroupTree(ctx context.Context) (v importdata.GroupTree, e error) {
	e = f.get(ctx, "", &v)
	return
}
func (f fixtureSource) Month(ctx context.Context, g int64, y, m int) (v importdata.MonthSchedule, e error) {
	e = f.get(ctx, "", &v)
	v.GroupID = g
	v.Year = y
	v.Month = m
	return
}
func (f fixtureSource) HasStaffDirectory(context.Context) (bool, error) { return f.staff, nil }
func (f fixtureSource) StaffDirectory(ctx context.Context) (v importdata.StaffDirectory, e error) {
	e = f.get(ctx, "/staff", &v)
	return
}
