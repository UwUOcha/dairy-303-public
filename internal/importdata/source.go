package importdata

import (
	"context"
	"errors"
)

var ErrAuth = errors.New("provider: authentication failed")

type Source interface {
	GroupTree(context.Context) (GroupTree, error)
	Month(context.Context, int64, int, int) (MonthSchedule, error)
	HasStaffDirectory(context.Context) (bool, error)
	StaffDirectory(context.Context) (StaffDirectory, error)
}
