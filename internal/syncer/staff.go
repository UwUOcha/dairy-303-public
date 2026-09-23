package syncer

import (
	"context"
	"errors"
	"fmt"
	"github.com/UwUOcha/dairy-303-public/pkg/provider"
	"strconv"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// SyncStaffDirectory обновляет справочник по расписанию. После ошибки
// повторяет попытку не чаще раза в сутки (либо заданного меньшего интервала).
// Отметка попытки хранится в базе и переживает перезапуск.
func (s *Syncer) SyncStaffDirectory(ctx context.Context) error {
	work, cancel := context.WithTimeout(ctx, s.opt.MonthTimeout)
	defer cancel()
	supported, err := s.client.HasStaffDirectory(work)
	if err != nil {
		return err
	}
	if !supported {
		return nil
	}
	_, err, _ = s.flight.Do("staff-directory", func() (any, error) {
		now := s.Now()
		for _, key := range []string{store.MetaStaffSyncedAt, store.MetaStaffAttemptedAt} {
			raw, err := s.db.Meta(ctx, key)
			if err != nil {
				return nil, err
			}
			if stamp, err := strconv.ParseInt(raw, 10, 64); err == nil {
				last := time.Unix(stamp, 0).In(s.opt.Location)
				if key == store.MetaStaffAttemptedAt {
					if now.Sub(last) < s.staffRetryInterval() {
						return nil, nil
					}
				} else if (s.opt.StaffInterval > 0 && now.Sub(last) < s.opt.StaffInterval) || (s.opt.StaffInterval == 0 && last.Year() == now.Year() && last.Month() == now.Month()) {
					return nil, nil
				}
			}
		}
		if err := s.db.SetMeta(ctx, store.MetaStaffAttemptedAt, fmt.Sprint(now.Unix())); err != nil {
			return nil, err
		}
		work, cancel := context.WithTimeout(ctx, s.opt.MonthTimeout)
		defer cancel()
		directory, err := s.client.StaffDirectory(work)
		if err != nil {
			var pe *provider.Error
			if errors.As(err, &pe) && pe.Code == "not_supported" {
				return nil, nil
			}
			return nil, err
		}
		fetched := now
		if directory.FetchedAt != "" {
			var e error
			fetched, e = time.Parse(time.RFC3339, directory.FetchedAt)
			if e != nil {
				return nil, e
			}
		}
		if err := s.db.SaveStaffDirectory(work, directory, fetched); err != nil {
			return nil, err
		}
		s.log.Info("справочник преподавателей обновлён", "учётных записей", len(directory.Staff))
		return nil, nil
	})
	return err
}

func (s *Syncer) staffLoop(ctx context.Context) {
	for {
		if err := s.SyncStaffDirectory(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("справочник преподавателей недоступен, используются сохранённые сведения", "ошибка", err)
		}
		now := s.Now()
		next := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, s.opt.Location)
		// Проверяем и повтор после ошибки, не дожидаясь нового месяца.
		if retry := now.Add(s.staffRetryInterval()); retry.Before(next) {
			next = retry
		}
		if !sleep(ctx, next.Sub(now)) {
			return
		}
	}
}

func (s *Syncer) staffRetryInterval() time.Duration {
	if s.opt.StaffInterval > 0 && s.opt.StaffInterval < 24*time.Hour {
		return s.opt.StaffInterval
	}
	return 24 * time.Hour
}
