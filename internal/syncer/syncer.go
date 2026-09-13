// Package syncer держит локальную копию расписания вуза в актуальном виде.
//
// Ключевая идея всей архитектуры: этот пакет — не обработчик пользовательских
// запросов, а фоновый синхронизатор. Пользовательский путь upstream вообще не
// касается, потому что за 0.8 с и 241 КБ на каждое нажатие кнопки бот был бы
// невыносимым. Вместо этого расписание всего вуза лежит рядом, в SQLite, и
// отдаётся за доли миллисекунды.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Options — настройки синхронизатора.
type Options struct {
	StaffInterval time.Duration // Zero preserves calendar-month refresh for existing callers.
	// FullSyncAt — минута суток для ночного полного обхода.
	FullSyncAt int
	// HotInterval — период обновления групп, за которыми стоят живые пользователи.
	HotInterval time.Duration
	// GroupsInterval — период обновления дерева групп.
	GroupsInterval time.Duration
	// MonthsAhead — сколько месяцев вперёд тянуть сверх текущего.
	MonthsAhead int
	// Location — часовой пояс вуза.
	Location *time.Location
	// StaleAfter — через сколько месяц считается протухшим и требует
	// догоняющего запроса при обращении пользователя.
	StaleAfter time.Duration
	// MonthTimeout — потолок на загрузку одного месяца. Это backstop поверх
	// ретраев клиента, а не политика ожидания: пользовательский запрос ждёт
	// столько, сколько отпущено его собственному контексту (см. SyncMonth).
	MonthTimeout time.Duration
}

// ChangeFunc вызывается, когда расписание месяца действительно изменилось.
// Первая загрузка месяца изменением не считается.
type ChangeFunc func(ctx context.Context, groupID int64, year, month int)

// Syncer связывает клиент upstream и хранилище.
type Syncer struct {
	client importdata.Source
	db     *store.DB
	opt    Options
	log    *slog.Logger

	// flight схлопывает одновременные запросы одного и того же месяца.
	// Утром тридцать человек из одной группы жмут «сегодня» практически
	// одновременно; без этого получилось бы тридцать запросов к вузу.
	flight singleflight.Group

	mu       sync.Mutex
	onChange ChangeFunc
	// lastErr — последняя ошибка синка, для healthcheck.
	lastErr error
}

// New собирает синхронизатор.
func New(client importdata.Source, db *store.DB, opt Options, log *slog.Logger) *Syncer {
	if opt.Location == nil {
		opt.Location = time.UTC
	}
	if opt.HotInterval <= 0 {
		opt.HotInterval = 25 * time.Minute
	}
	if opt.GroupsInterval <= 0 {
		opt.GroupsInterval = 24 * time.Hour
	}
	if opt.StaleAfter <= 0 {
		opt.StaleAfter = 6 * time.Hour
	}
	if opt.MonthTimeout <= 0 {
		opt.MonthTimeout = 2 * time.Minute
	}
	return &Syncer{client: client, db: db, opt: opt, log: log}
}

// OnChange задаёт обработчик изменения расписания.
func (s *Syncer) OnChange(fn ChangeFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// LastError возвращает последнюю ошибку фонового контура.
func (s *Syncer) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *Syncer) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
}

// Now возвращает текущее время в часовом поясе вуза.
func (s *Syncer) Now() time.Time { return time.Now().In(s.opt.Location) }

// SyncGroupTree обновляет каталог групп — один запрос на весь вуз.
func (s *Syncer) SyncGroupTree(ctx context.Context) error {
	tree, err := s.client.GroupTree(ctx)
	if err != nil {
		return fmt.Errorf("дерево групп: %w", err)
	}
	if err := s.db.SaveGroupTree(ctx, tree); err != nil {
		return fmt.Errorf("сохранение дерева групп: %w", err)
	}
	if err := s.db.SetMeta(ctx, store.MetaGroupsSyncedAt, fmt.Sprint(time.Now().Unix())); err != nil {
		return err
	}
	s.log.Info("дерево групп обновлено",
		"подразделений", len(tree.Departments), "групп", len(tree.Groups))
	return nil
}

// SyncMonth загружает и сохраняет один месяц одной группы.
//
// Одновременные вызовы для одного и того же месяца схлопываются в один запрос
// к upstream — но каждый вызывающий ждёт ровно столько, сколько отпущено его
// контексту. Это две разные вещи, и раньше они были склеены: singleflight.Do
// контекста не знает вовсе, поэтому пользовательский запрос с потолком в
// шесть секунд простаивал столько, сколько работал фоновый синк, ставший
// лидером, — а фоновый синк, наоборот, получал чужой DeadlineExceeded и писал
// его себе в ошибку контура.
//
// Отсюда две поправки: ожидание идёт через DoChan с select по ctx.Done(), а
// сама работа выполняется в контексте, отвязанном от заказчика. Отвал первого
// в очереди не повод бросать загрузку месяца для всех остальных.
func (s *Syncer) SyncMonth(ctx context.Context, groupID int64, year, month int) error {
	key := fmt.Sprintf("%d:%d:%d", groupID, year, month)
	ch := s.flight.DoChan(key, func() (any, error) {
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opt.MonthTimeout)
		defer cancel()
		return nil, s.syncMonth(workCtx, groupID, year, month)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		return r.Err
	}
}

// syncMonth — собственно загрузка и запись месяца, без схлопывания.
func (s *Syncer) syncMonth(ctx context.Context, groupID int64, year, month int) error {
	ms, err := s.client.Month(ctx, groupID, year, month)
	if err != nil {
		return err
	}
	changed, err := s.db.SaveMonth(ctx, ms)
	if err != nil {
		return err
	}
	// HTTP imports persist all metadata atomically with the authoritative month.
	// The compatibility source is retained only for existing parser tests.
	if ms.FetchedAt == "" {
		if err := s.db.SaveSubgroups(ctx, groupID, ms.Subgroups); err != nil {
			return err
		}
		if err := s.db.SaveWorkdays(ctx, ms.Workdays); err != nil {
			return err
		}
	}
	if changed {
		s.log.Info("расписание изменилось", "группа", groupID, "год", year, "месяц", month)
		s.mu.Lock()
		fn := s.onChange
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, groupID, year, month)
		}
	}
	return nil
}

// EnsureRange догоняющим запросом добирает месяцы, покрывающие диапазон дат,
// если их в базе нет или они протухли.
//
// Это подстраховка на случай, когда пользователь заглянул в месяц, до
// которого ночной обход ещё не дошёл. В норме путь пользователя upstream не
// трогает вовсе.
func (s *Syncer) EnsureRange(ctx context.Context, groupID int64, from, to string) error {
	start, err := schedule.ParseDate(from)
	if err != nil {
		return err
	}
	end, err := schedule.ParseDate(to)
	if err != nil {
		return err
	}

	var errs []error
	cur := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
	for ; !cur.After(last); cur = cur.AddDate(0, 1, 0) {
		year, month := cur.Year(), int(cur.Month())
		if !s.needsFetch(ctx, groupID, year, month) {
			continue
		}
		if err := s.SyncMonth(ctx, groupID, year, month); err != nil {
			// Недоступный upstream — не повод отказать пользователю: то, что
			// уже лежит в базе, мы всё равно покажем, пометив datedness.
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Syncer) needsFetch(ctx context.Context, groupID int64, year, month int) bool {
	st, err := s.db.MonthState(ctx, groupID, year, month)
	if err != nil {
		return true
	}
	return time.Since(st.FetchedAt) > s.opt.StaleAfter
}

// monthsToCover перечисляет месяцы, которые синк держит загруженными:
// текущий плюс MonthsAhead вперёд.
func (s *Syncer) monthsToCover() [][2]int {
	now := s.Now()
	out := make([][2]int, 0, s.opt.MonthsAhead+1)
	for i := 0; i <= s.opt.MonthsAhead; i++ {
		m := now.AddDate(0, i, 0)
		out = append(out, [2]int{m.Year(), int(m.Month())})
	}
	return out
}

// PartialError — обход, который дошёл до конца, потеряв часть месяцев.
//
// Отдельный тип нужен контурам: сетевая рябь на нескольких запросах из сотни
// и мёртвый upstream требуют разной реакции, а по одной ошибке первого
// неудавшегося месяца их не различить.
type PartialError struct {
	Contour  string
	Failed   int
	Requests int
	Err      error
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("%s обход: %d из %d запросов не удались: %v", e.Contour, e.Failed, e.Requests, e.Err)
}

func (e *PartialError) Unwrap() error { return e.Err }

// syncGroups обходит переданные группы по всем покрываемым месяцам.
func (s *Syncer) syncGroups(ctx context.Context, groupIDs []int64, what string) error {
	months := s.monthsToCover()
	started := time.Now()

	var failed int
	var firstErr error
	for _, gid := range groupIDs {
		for _, ym := range months {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if err := s.SyncMonth(ctx, gid, ym[0], ym[1]); err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
				// Отозванный токен кладёт весь синк — дальше молотить бессмысленно.
				if errors.Is(err, importdata.ErrAuth) {
					s.log.Error("upstream отверг токен, синк остановлен", "контур", what)
					return err
				}
				s.log.Warn("месяц не загружен", "группа", gid, "год", ym[0], "месяц", ym[1], "ошибка", err)
			}
		}
	}

	requests := len(groupIDs) * len(months)
	s.log.Info("синк завершён",
		"контур", what,
		"групп", len(groupIDs),
		"запросов", requests,
		"ошибок", failed,
		"длительность", time.Since(started).Round(time.Second))
	// Потерять несколько месяцев из сотен запросов — это не поломка контура,
	// а обычная сетевая рябь; не удаться целиком — поломка. Разницу решает
	// вызывающий, наше дело — не выдать одно за другое.
	if failed > 0 && failed < requests {
		return &PartialError{Contour: what, Failed: failed, Requests: requests, Err: firstErr}
	}
	return firstErr
}

// FullSync обходит все действующие группы вуза. 675 групп × 2 месяца при
// вежливых 2 req/s — около одиннадцати минут раз в сутки.
func (s *Syncer) FullSync(ctx context.Context) error {
	ids, err := s.db.ActiveGroupIDs(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("каталог активных групп пуст: полный обход отложен")
	}
	err = s.syncGroups(ctx, ids, "полный")

	// Отметку ставим и после прохода, на котором часть месяцев не далась:
	// обход вуза состоялся, и повторять его целиком из-за нескольких
	// неудачных запросов — как раз та невежливость, которой мы избегаем.
	// Не ставим только если он оборвался весь: отозванный токен или остановка.
	if errors.Is(err, importdata.ErrAuth) || ctx.Err() != nil {
		return err
	}
	if serr := s.db.SetMeta(ctx, store.MetaFullSyncAt, fmt.Sprint(time.Now().Unix())); serr != nil {
		return serr
	}
	return err
}

// HotSync обновляет только группы, за которыми стоят живые пользователи.
//
// Из 675 групп вуза их наберётся несколько десятков, и цена дневного контура
// падает на порядок.
func (s *Syncer) HotSync(ctx context.Context) error {
	ids, err := s.db.HotGroupIDs(ctx)
	if err != nil {
		return err
	}
	var err2 error
	if len(ids) > 0 {
		err2 = s.syncGroups(ctx, ids, "горячий")
		// Как и в полном обходе: проход состоялся, даже если пара месяцев не
		// далась. Не отмечаем только полный обрыв.
		if errors.Is(err2, importdata.ErrAuth) || ctx.Err() != nil {
			return err2
		}
	}
	// Отметка нужна и при пустом списке: она говорит «контур отработал», и
	// перезапуск демона не должен считать это поводом пойти к вузу заново.
	if err := s.db.SetMeta(ctx, store.MetaHotSyncAt, fmt.Sprint(time.Now().Unix())); err != nil {
		return err
	}
	return err2
}
