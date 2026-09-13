package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// История метрик — единственное, что панель хранит сама.
//
// Своя база, а не таблица в schedule.db, по правилу проекта: у базы расписания
// ровно один писатель, и подмешивать к нему второй процесс ради графиков
// значит разменять главную гарантию хранилища на удобство. Цена отдельного
// файла — один том в compose; цена общего — busy-таймауты в ночном обходе.
//
// Ряд получается крошечный: точка раз в пять минут за тридцать дней — это
// 8640 строк примерно по сотне байт. Уборка идёт при каждой записи, так что
// файл выходит на постоянный размер и там остаётся.

// Sample — точка истории.
type Sample struct {
	At         int64   `json:"at"`
	CPUPercent float64 `json:"cpu"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	SwapUsed   uint64  `json:"swap_used"`
	DiskUsed   uint64  `json:"disk_used"`
	DiskTotal  uint64  `json:"disk_total"`
	Load1      float64 `json:"load1"`
	Users      int     `json:"users"`
	UsersDay   int     `json:"users_day"`
	Lessons    int     `json:"lessons"`
	DBBytes    int64   `json:"db_bytes"`
	// RaspMem и BotMem — сколько памяти держит рантайм Go каждого демона.
	// Это не RSS контейнера, а именно куча процесса: рост здесь при ровном
	// потреблении машины — утечка, обратное — вопрос к аллокатору.
	RaspMem uint64 `json:"rasp_mem"`
	BotMem  uint64 `json:"bot_mem"`
}

// History — хранилище точек.
type History struct {
	db        *sql.DB
	retention time.Duration
}

const historySchema = `
CREATE TABLE IF NOT EXISTS samples (
  at         INTEGER PRIMARY KEY,   -- unix ts, он же ключ: точка на момент времени одна
  cpu        REAL    NOT NULL DEFAULT 0,
  mem_used   INTEGER NOT NULL DEFAULT 0,
  mem_total  INTEGER NOT NULL DEFAULT 0,
  swap_used  INTEGER NOT NULL DEFAULT 0,
  disk_used  INTEGER NOT NULL DEFAULT 0,
  disk_total INTEGER NOT NULL DEFAULT 0,
  load1      REAL    NOT NULL DEFAULT 0,
  users      INTEGER NOT NULL DEFAULT 0,
  users_day  INTEGER NOT NULL DEFAULT 0,
  lessons    INTEGER NOT NULL DEFAULT 0,
  db_bytes   INTEGER NOT NULL DEFAULT 0,
  rasp_mem   INTEGER NOT NULL DEFAULT 0,
  bot_mem    INTEGER NOT NULL DEFAULT 0
);`

// OpenHistory открывает или создаёт базу истории.
func OpenHistory(ctx context.Context, path string, retention time.Duration) (*History, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("admin: каталог базы истории: %w", err)
		}
	}

	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?"+strings.Join(pragmas, "&"))
	if err != nil {
		return nil, fmt.Errorf("admin: открытие базы истории: %w", err)
	}
	// Писатель и читатель здесь один процесс, а запись — одна строка раз в пять
	// минут: пул на одно соединение снимает вопрос блокировок целиком.
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, historySchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("admin: схема базы истории: %w", err)
	}
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	return &History{db: db, retention: retention}, nil
}

// Close закрывает базу.
func (h *History) Close() error { return h.db.Close() }

// Add записывает точку и убирает то, что вышло за срок хранения.
func (h *History) Add(ctx context.Context, s Sample) error {
	_, err := h.db.ExecContext(ctx,
		`INSERT INTO samples(at, cpu, mem_used, mem_total, swap_used, disk_used, disk_total,
		                     load1, users, users_day, lessons, db_bytes, rasp_mem, bot_mem)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(at) DO NOTHING`,
		s.At, s.CPUPercent, s.MemUsed, s.MemTotal, s.SwapUsed, s.DiskUsed, s.DiskTotal,
		s.Load1, s.Users, s.UsersDay, s.Lessons, s.DBBytes, s.RaspMem, s.BotMem)
	if err != nil {
		return fmt.Errorf("admin: запись точки истории: %w", err)
	}

	cutoff := time.Now().Add(-h.retention).Unix()
	if _, err := h.db.ExecContext(ctx, `DELETE FROM samples WHERE at < ?`, cutoff); err != nil {
		return fmt.Errorf("admin: уборка истории: %w", err)
	}
	return nil
}

// Range отдаёт точки за последние d, от старых к свежим.
//
// Порядок именно такой, потому что график читается слева направо, и
// разворачивать ряд в браузере — лишняя работа на каждом опросе.
func (h *History) Range(ctx context.Context, d time.Duration, limit int) ([]Sample, error) {
	if limit <= 0 {
		limit = 600
	}
	from := time.Now().Add(-d).Unix()

	// Прореживание в SQL, а не в Go: за месяц точек 8640, а нарисовать их на
	// графике шириной в тысячу пикселей всё равно нельзя.
	//
	// Шаг считается от того, сколько точек в окне на самом деле, а не от его
	// ширины: первый месяц жизни панели база заполнена не целиком, и оценка по
	// ширине проредила бы десяток точек до одной.
	var total int
	if err := h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM samples WHERE at >= ?`, from).Scan(&total); err != nil {
		return nil, fmt.Errorf("admin: подсчёт истории: %w", err)
	}
	step := 1
	if total > limit {
		step = (total + limit - 1) / limit
	}

	// Нумерация идёт от свежих: при любом шаге прореживания последняя точка
	// обязана попасть в выборку, иначе график отстаёт от чисел над ним.
	rows, err := h.db.QueryContext(ctx,
		`SELECT at, cpu, mem_used, mem_total, swap_used, disk_used, disk_total,
		        load1, users, users_day, lessons, db_bytes, rasp_mem, bot_mem
		 FROM (
		   SELECT *, ROW_NUMBER() OVER (ORDER BY at DESC) AS rn
		   FROM samples WHERE at >= ?
		 )
		 WHERE (rn - 1) % ? = 0
		 ORDER BY at`,
		from, step)
	if err != nil {
		return nil, fmt.Errorf("admin: чтение истории: %w", err)
	}
	defer rows.Close()

	var out []Sample
	for rows.Next() {
		var s Sample
		if err := rows.Scan(&s.At, &s.CPUPercent, &s.MemUsed, &s.MemTotal, &s.SwapUsed,
			&s.DiskUsed, &s.DiskTotal, &s.Load1, &s.Users, &s.UsersDay,
			&s.Lessons, &s.DBBytes, &s.RaspMem, &s.BotMem); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
