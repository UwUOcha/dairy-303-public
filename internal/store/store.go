// Package store — локальная копия расписания вуза в SQLite.
//
// Единственный писатель — демон raspd. Читателей может быть много: WAL
// позволяет им работать, пока идёт запись, поэтому пулы соединений разделены.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion — версия схемы в PRAGMA user_version. Увеличивается вместе с
// добавлением новой миграции в upgrades.
const schemaVersion = 16

// upgrades[i] переводит существующую базу с версии i на версию i+1.
//
// Пустая база этот список не проходит вовсе: schema.sql всегда описывает
// актуальную схему, поэтому свежая установка получает её целиком и сразу
// объявляется текущей версией. Иначе миграции пришлось бы накатывать на схему,
// в которой их изменения уже есть, — например, добавлять колонку shadowed
// поверх той, что создана schema.sql.
var upgrades = map[int]func(context.Context, *sql.Tx) error{
	15: migrateUserTimezone,
	14: func(ctx context.Context, tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `ALTER TABLE bot_api_key RENAME TO bot_api_key_old;`+botKeySchema+`INSERT INTO bot_api_key SELECT * FROM bot_api_key_old; DROP TABLE bot_api_key_old;`)
		return e
	},
	13: func(ctx context.Context, tx *sql.Tx) error { _, e := tx.ExecContext(ctx, providerSchema); return e },
	12: func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, botKeySchema); return err },
	11: func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, webAuthSchema); return err },
	10: migrateStaffIdentity,
	9: func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, scheduleRevisionsSchema); err != nil {
			return err
		}
		return backfillScheduleRevisions(ctx, tx)
	},
	// Версия доставленного меню: прежний флаг не отличал старые кнопки от новых.
	8: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `ALTER TABLE users ADD COLUMN menu_version TEXT NOT NULL DEFAULT ''`)
		return err
	},
	// Признак «группа спрятана как дубликат имени» — см. shadow.go.
	1: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`ALTER TABLE groups ADD COLUMN shadowed INTEGER NOT NULL DEFAULT 0`)
		return err
	},
	// Очередь исходящих сообщений и признак «нижнее меню человеку показано».
	2: func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE users ADD COLUMN menu_sent INTEGER NOT NULL DEFAULT 0`,
			outboxSchema,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	},
	// Уведомления вместо одной утренней рассылки: главный выключатель, вечернее
	// сообщение, пустые дни, новости о правках — и место под ожидаемый ввод
	// времени. Прежняя колонка notify_at несла сразу два смысла (включено ли
	// вообще и во сколько), и разделить их без миграции было нельзя.
	3: func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE users ADD COLUMN notify_on INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE users ADD COLUMN morning_on INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE users ADD COLUMN morning_at INTEGER NOT NULL DEFAULT 420`,
			`ALTER TABLE users ADD COLUMN evening_on INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN evening_at INTEGER NOT NULL DEFAULT 1110`,
			`ALTER TABLE users ADD COLUMN empty_on INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN empty_hinted INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN changes_on INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE users ADD COLUMN notified_eve_on TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN await TEXT NOT NULL DEFAULT ''`,
			// Кто рассылку не включал — тому бот и дальше молчит; у остальных
			// сохраняется выбранное время. Пустые дни выключаются всем: это и
			// есть смена поведения, ради которой затевалась миграция.
			`UPDATE users SET notify_on = CASE WHEN notify_at IS NULL THEN 0 ELSE 1 END,
			                  morning_at = COALESCE(notify_at, 420)`,
			`DROP INDEX IF EXISTS idx_users_notify`,
			`ALTER TABLE users DROP COLUMN notify_at`,
			`CREATE INDEX idx_users_notify ON users(notify_on) WHERE notify_on = 1`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	},
	// Отпечаток будущей части месяца: по нему решается, беспокоить ли
	// подписчиков. Прежний content_hash ловил любую правку, включая правку
	// прошедшего дня, и человек получал «расписание изменилось» из-за темы,
	// проставленной задним числом к паре двухнедельной давности.
	4: func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE month_state ADD COLUMN future_hash TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE month_state ADD COLUMN future_from TEXT NOT NULL DEFAULT ''`,
			// Пустая граница значит «весь месяц», поэтому существующие месяцы
			// переезжают со своим content_hash: первое сравнение после
			// обновления пройдёт ровно так же, как прошло бы до него, а окно
			// встанет на место с первым же запросом.
			`UPDATE month_state SET future_hash = content_hash`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	},
	// Снимки изменённых дней: из них собирается «до/после» по кнопке
	// «подробнее» под новостью о правке. Раньше сообщение умело сказать
	// только «расписание изменилось» — по хэшу месяца больше и не скажешь.
	5: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, changeDaysSchema)
		return err
	},
	// Обратная связь из кнопки «Написать автору».
	//
	// Заодно сбрасывается menu_sent: в этой версии в нижнем меню поменялась
	// кнопка, а клавиатура остаётся у клиента навсегда. Без сброса все, кто
	// уже пользуется ботом, так и держали бы старую панель — отметка о показе
	// как раз и запрещает адаптеру присылать её повторно.
	6: func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{feedbackSchema, `UPDATE users SET menu_sent = 0`} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	},
	// Учёт заблокировавших бота: отдельный признак и дата последней живой
	// проверки. Раньше блокировка выражалась только в notify_on = 0 — тем же
	// самым, чем и осознанное «не пиши мне», так что отличить одно от другого
	// было нельзя ни в панели, ни в коде.
	//
	// probed_at у всех остаётся нулём: первый недельный обход разберёт базу
	// целиком, начиная с самых старых записей, и дальше пойдёт ровным кругом.
	7: func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE users ADD COLUMN blocked_at INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN probed_at INTEGER NOT NULL DEFAULT 0`,
			`CREATE INDEX idx_users_probe ON users(platform, probed_at)`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	},
}

// outboxSchema и changeDaysSchema продублированы из schema.sql, чтобы миграция
// не зависела от того, как тот файл будет выглядеть через десять версий:
// миграция обязана создавать таблицу ровно такой, какой её ждала своя версия,
// а не текущая.
const outboxSchema = `
CREATE TABLE outbox (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  platform    TEXT    NOT NULL,
  ext_id      TEXT    NOT NULL,
  kind        TEXT    NOT NULL,
  dedup_key   TEXT    NOT NULL DEFAULT '',
  payload     TEXT    NOT NULL DEFAULT '',
  attempts    INTEGER NOT NULL DEFAULT 0,
  next_try_at INTEGER NOT NULL,
  created_at  INTEGER NOT NULL,
  UNIQUE(platform, ext_id, kind, dedup_key)
);
CREATE INDEX idx_outbox_ready ON outbox(platform, next_try_at);`

const changeDaysSchema = `
CREATE TABLE change_days (
  group_id    INTEGER NOT NULL,
  date        TEXT    NOT NULL,
  before_json TEXT    NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (group_id, date)
);
CREATE INDEX idx_change_days_created ON change_days(created_at);`

const feedbackSchema = `
CREATE TABLE feedback (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  platform    TEXT    NOT NULL,
  ext_id      TEXT    NOT NULL,
  group_name  TEXT    NOT NULL DEFAULT '',
  text        TEXT    NOT NULL,
  created_at  INTEGER NOT NULL,
  answered_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_feedback_created ON feedback(created_at);
CREATE INDEX idx_feedback_author ON feedback(platform, ext_id, created_at);`

// DB — хранилище с раздельными пулами на запись и на чтение.
type DB struct {
	// w — пул записи ровно на одно соединение. SQLite всё равно допускает
	// только одного писателя, а явный потолок превращает случайные
	// SQLITE_BUSY в честную очередь.
	w *sql.DB
	// r — пул чтения. Читатели не мешают писателю благодаря WAL.
	r *sql.DB
	// path — путь к файлу базы. Нужен ровно для одного: сказать админ-панели,
	// сколько база занимает на диске (см. fileSize).
	path string
}

// Open открывает или создаёт базу и приводит схему к актуальной версии.
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: каталог базы: %w", err)
		}
	}

	w, err := open(path, true)
	if err != nil {
		return nil, err
	}
	r, err := open(path, false)
	if err != nil {
		w.Close()
		return nil, err
	}
	db := &DB{w: w, r: r, path: path}
	if err := db.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func open(path string, writer bool) (*sql.DB, error) {
	// journal_mode применяется к файлу базы, остальные прагмы — к соединению,
	// поэтому задаём их в DSN: пул может открыть новое соединение в любой момент.
	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)", // fsync на checkpoint, а не на каждый коммит
		"_pragma=foreign_keys(1)",
		"_pragma=busy_timeout(10000)",
	}
	if !writer {
		pragmas = append(pragmas, "mode=ro")
	}
	dsn := "file:" + url.PathEscape(path) + "?" + strings.Join(pragmas, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: открытие базы: %w", err)
	}
	if writer {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(4)
	}
	db.SetMaxIdleConns(2)
	return db, nil
}

// Close закрывает оба пула.
func (db *DB) Close() error {
	var first error
	for _, h := range []*sql.DB{db.r, db.w} {
		if h == nil {
			continue
		}
		if err := h.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// migrate приводит схему к актуальной версии.
func (db *DB) migrate(ctx context.Context) error {
	var version int
	if err := db.w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: чтение user_version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("store: база версии %d новее, чем понимает бинарь (%d)", version, schemaVersion)
	}

	if version == 0 {
		empty, err := db.isEmpty(ctx)
		if err != nil {
			return err
		}
		if empty {
			return db.tx(ctx, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
					return fmt.Errorf("store: создание схемы: %w", err)
				}
				return setUserVersion(ctx, tx, schemaVersion)
			})
		}
	}

	for v := version; v < schemaVersion; v++ {
		up, ok := upgrades[v]
		if !ok {
			return fmt.Errorf("store: нет миграции с версии %d", v)
		}
		if err := db.tx(ctx, func(tx *sql.Tx) error {
			if err := up(ctx, tx); err != nil {
				return err
			}
			return setUserVersion(ctx, tx, v+1)
		}); err != nil {
			return fmt.Errorf("store: миграция %d→%d: %w", v, v+1, err)
		}
	}
	return nil
}

// isEmpty сообщает, что в базе нет ни одной нашей таблицы.
func (db *DB) isEmpty(ctx context.Context) (bool, error) {
	var n int
	err := db.w.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n)
	return n == 0, err
}

// setUserVersion проставляет версию схемы. PRAGMA не принимает плейсхолдеры,
// но значение сюда приходит из констант кода, а не от пользователя.
func setUserVersion(ctx context.Context, tx *sql.Tx, v int) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v))
	return err
}

// tx выполняет fn в транзакции записи, откатывая её при ошибке или панике.
func (db *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Ping проверяет, что база отвечает. Используется в healthcheck.
func (db *DB) Ping(ctx context.Context) error { return db.r.PingContext(ctx) }

// meta-таблица хранит редкие глобальные значения вроде времени последнего
// обновления дерева групп.

// SetMeta записывает значение.
func (db *DB) SetMeta(ctx context.Context, key, value string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return err
	})
}

// Meta читает значение; отсутствие ключа — не ошибка.
func (db *DB) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := db.r.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// Counts возвращает число действующих групп и занятий — цифры для healthcheck.
func (db *DB) Counts(ctx context.Context) (groups, lessons int, err error) {
	if err = db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE is_active = 1`).Scan(&groups); err != nil {
		return 0, 0, err
	}
	err = db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM lessons`).Scan(&lessons)
	return groups, lessons, err
}
