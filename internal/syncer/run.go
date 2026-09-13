package syncer

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Run поднимает фоновые контуры синхронизации и работает до отмены контекста.
//
// Контуров три, разной частоты: дерево групп раз в сутки, полный обход ночью,
// горячие группы каждые двадцать-тридцать минут. Всплески нагрузки вынесены из
// пользовательского пути: днём бот отвечает из базы, а ночью в одиночку
// разбирает 325 МБ JSON.
func (s *Syncer) Run(ctx context.Context) error {
	if err := s.bootstrap(ctx); err != nil && ctx.Err() == nil {
		// Пустая база на старте — повод громко пожаловаться, но не повод
		// падать: контуры продолжат попытки, а бот пока поработает на том,
		// что есть.
		s.log.Error("первичная загрузка не удалась", "ошибка", err)
		s.setError(err)
	}

	var wg sync.WaitGroup
	for _, loop := range []struct {
		name string
		fn   func(context.Context)
	}{
		{"дерево групп", s.groupsLoop},
		{"справочник преподавателей", s.staffLoop},
		{"горячие группы", s.hotLoop},
		{"полный обход", s.fullLoop},
		{"уборка", s.janitorLoop},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop.fn(ctx)
			s.log.Debug("контур остановлен", "контур", loop.name)
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// bootstrap обновляет каталог при каждом запуске. Один дешёвый запрос здесь
// важен даже при полной базе: upstream иногда удаляет старые записи двойников,
// и без свежего дерева бот до суточного фонового прохода показывает уже
// несуществующий ручной переключатель.
func (s *Syncer) bootstrap(ctx context.Context) error {
	s.log.Info("обновляю каталог групп при запуске")
	if err := s.SyncGroupTree(ctx); err != nil {
		return err
	}
	// Расписание при холодном старте не тянем целиком: полный обход займёт
	// одиннадцать минут, а нужные группы всё равно доберутся догоняющим
	// запросом при первом обращении.
	return nil
}

func (s *Syncer) groupsLoop(ctx context.Context) {
	// Bootstrap обычно только что обновил дерево, поэтому ждём остаток
	// интервала. После ошибки запуска повторяем запрос не позже чем через
	// минуту: адаптер может стартовать позже ядра.
	wait := s.opt.GroupsInterval - s.sinceMeta(ctx, store.MetaGroupsSyncedAt)
	if s.LastError() != nil {
		wait = min(wait, time.Minute)
	}
	if wait > 0 {
		if !sleep(ctx, wait) {
			return
		}
	}
	for {
		delay := s.opt.GroupsInterval
		if err := s.SyncGroupTree(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("обновление дерева групп", "ошибка", err)
			s.setError(err)
			delay = min(delay, time.Minute)
		}
		if !sleep(ctx, delay) {
			return
		}
	}
}

// hotLoop обновляет группы с живыми пользователями.
//
// Первый проход делается сразу, а не через интервал: демон мог простоять
// полдня, и держать людей на данных той давности незачем. Но только если с
// прошлого прохода интервал действительно вышел — иначе перезапуск в цикле
// слал бы вузу полный обход горячих групп на каждый старт.
func (s *Syncer) hotLoop(ctx context.Context) {
	if wait := s.opt.HotInterval - s.sinceMeta(ctx, store.MetaHotSyncAt); wait > 0 {
		if !sleep(ctx, wait) {
			return
		}
	}
	for {
		err := s.HotSync(ctx)
		var partial *PartialError
		switch {
		case err == nil:
			s.setError(nil)
		case ctx.Err() != nil:
			// Демон останавливают: жаловаться не на что и некому.
		case errors.As(err, &partial):
			// Обход состоялся, потеряна часть месяцев. Следующий проход через
			// четверть часа догонит их сам, поднимать алерт не за что.
			s.log.Warn("горячие группы обновлены не полностью", "ошибка", err)
			s.setError(nil)
		default:
			s.log.Error("обновление горячих групп", "ошибка", err)
			s.setError(err)
		}
		if !sleep(ctx, s.opt.HotInterval) {
			return
		}
	}
}

// fullSyncOverdue — с какого возраста прошлого полного обхода считаем, что
// ночь пропущена. Сутки плюс запас, чтобы штатный ежедневный обход не
// признавался просроченным из-за дрейфа на несколько минут.
const fullSyncOverdueAfter = 26 * time.Hour

// fullSyncRetry — пауза после обхода, который оборвался целиком.
const fullSyncRetry = time.Hour

func (s *Syncer) fullLoop(ctx context.Context) {
	for {
		// Ждать до ближайших 03:00 безусловно нельзя: демон, который
		// перезапускается позже этого часа (деплой, logrotate, OOM), не
		// обошёл бы вуз ни разу и не сказал бы об этом ни слова.
		if !s.fullSyncOverdue(ctx) {
			if !sleep(ctx, s.untilNextFullSync()) {
				return
			}
		}
		err := s.FullSync(ctx)
		var partial *PartialError
		broken := err != nil && !errors.As(err, &partial)
		if broken && ctx.Err() == nil {
			s.log.Error("полный обход", "ошибка", err)
			s.setError(err)
			// Пропущенную ночь нагоняем сразу, а вот оборвавшуюся попытку
			// повторяем с паузой. Без неё отозванный токен превратил бы
			// контур в непрерывный обход вуза: обход не дошёл до конца,
			// отметки нет, значит «просрочено» — и так по кругу.
			if !sleep(ctx, fullSyncRetry) {
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if partial != nil {
			// Вуз обойдён, несколько месяцев из тысячи запросов не дались.
			// Повторять из-за них весь обход — та самая невежливость, которой
			// мы избегаем; потерянное догонит либо горячий контур, либо
			// следующая ночь. Поэтому WARN и никакого алерта.
			s.log.Warn("полный обход завершён с потерями", "ошибка", err)
		}
		s.setError(nil)
	}
}

// fullSyncOverdue сообщает, что полный обход надо сделать не дожидаясь ночи.
//
// Отметки нет — это свежая установка. Раньше здесь стоял отказ: обход займёт
// лимит запросов на одиннадцать минут, а он нужнее догоняющим фетчам первых
// пользователей. Оказалось, что без единого занятия бот не может даже
// правильно выбрать запись из пары двойников (см. shadow.go): половина
// каталога прячется наугад, и человек не находит свою группу вовсе. Каталог
// без расписания бесполезен, поэтому первый обход идёт сразу.
//
// Условие именно «занятий нет», а не «отметки нет»: восстановленная из бэкапа
// база отметку потеряет, но данные в ней есть, и гнать вузу лишние 1350
// запросов из-за пропавшей строчки в meta незачем.
func (s *Syncer) fullSyncOverdue(ctx context.Context) bool {
	raw, err := s.db.Meta(ctx, store.MetaFullSyncAt)
	if err != nil {
		return false
	}
	if raw == "" {
		_, lessons, err := s.db.Counts(ctx)
		return err == nil && lessons == 0
	}
	return s.sinceMeta(ctx, store.MetaFullSyncAt) > fullSyncOverdueAfter
}

// outboxTTL — сколько живёт неотправленное сообщение очереди.
//
// Новость об изменении расписания за прошлую неделю бесполезна, а адресат мог
// заблокировать бота и не сообщить об этом ни разу неудачей, которую мы бы
// распознали. Поэтому очередь чистится по возрасту, а не только по попыткам.
const outboxTTL = 48 * time.Hour

// changeTTL — сколько живёт снимок изменённого дня, из которого собирается
// «до/после».
//
// Дольше очереди: человек читает новость о правке не сразу, а кнопка
// «подробнее» под ней должна работать и назавтра. Дольше трёх суток она
// показывала бы разницу с расписанием, которого никто уже не помнит.
const changeTTL = 72 * time.Hour

// janitorLoop убирает за системой то, что накапливается само.
func (s *Syncer) janitorLoop(ctx context.Context) {
	for {
		if err := s.db.PurgeOutbox(ctx, outboxTTL); err != nil && ctx.Err() == nil {
			s.log.Warn("уборка очереди исходящих", "ошибка", err)
		}
		if err := s.db.PurgeChangedDays(ctx, changeTTL, schedule.FormatDate(s.Now())); err != nil && ctx.Err() == nil {
			s.log.Warn("уборка снимков изменённых дней", "ошибка", err)
		}
		if !sleep(ctx, 6*time.Hour) {
			return
		}
	}
}

// untilNextFullSync считает, сколько ждать до ближайшего ночного обхода.
func (s *Syncer) untilNextFullSync() time.Duration {
	now := s.Now()
	target := time.Date(now.Year(), now.Month(), now.Day(),
		s.opt.FullSyncAt/60, s.opt.FullSyncAt%60, 0, 0, s.opt.Location)
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target.Sub(now)
}

// sinceMeta возвращает, сколько прошло с отметки времени в meta. Отсутствие
// отметки трактуется как «очень давно».
func (s *Syncer) sinceMeta(ctx context.Context, key string) time.Duration {
	raw, err := s.db.Meta(ctx, key)
	if err != nil || raw == "" {
		return 1 << 62
	}
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 1 << 62
	}
	return time.Since(time.Unix(ts, 0))
}

// sleep ждёт d или отмену контекста. Возвращает false, если пора выходить.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Healthy намеренно отсутствует.
//
// «Синк упал» и «сервис не может отвечать» — разные вещи, а раньше они были
// одним эндпоинтом: недоступный на старте источник делал /health красным, botd
// не проходил ожидание готовности и уходил в цикл перезапусков — при полной
// базе, из которой он мог бы отвечать. Живость проверяет api.Server по базе,
// а состояние контуров отдаётся отдельным полем для алертинга (LastError).
