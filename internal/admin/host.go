// Package admin — веб-панель наблюдения за сервисом.
//
// Панель не ходит в базу расписания и ничего в ней не меняет: всё, что она
// показывает, приезжает из raspd по тому же unix-сокету, что и данные для
// бота. Своё у неё ровно одно — история метрик машины, которую больше снимать
// некому.
//
// Кнопок, меняющих состояние, здесь нет намеренно. Панель, которая только
// смотрит, не нуждается ни в паролях, ни в CSRF-токенах, ни в аудите: цена
// ошибки в вайтлисте — чужой человек увидел, сколько у бота пользователей.
package admin

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Host — состояние машины на момент замера.
type Host struct {
	// CPUPercent — занятость процессора за интервал между замерами. Первый
	// замер после старта отдаёт 0: считать её не от чего.
	CPUPercent float64 `json:"cpu_percent"`
	Cores      int     `json:"cores"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`

	MemTotal  uint64 `json:"mem_total"`
	MemUsed   uint64 `json:"mem_used"`
	MemCache  uint64 `json:"mem_cache"`
	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`

	DiskTotal uint64 `json:"disk_total"`
	DiskUsed  uint64 `json:"disk_used"`

	// UptimeSec — аптайм машины, а не процесса: перезапуск контейнера его не
	// сбрасывает, и это ровно та цифра, которую ждут от слова «аптайм».
	UptimeSec int64 `json:"uptime_sec"`
	// Err — почему замер не удался. Панель показывает прочерки вместо цифр,
	// а не пятисотку: недоступный /proc не повод скрывать статистику бота.
	Err string `json:"err,omitempty"`
}

// Sampler снимает метрики машины.
//
// Держит предыдущий отсчёт процессорных тиков: занятость CPU — это разница
// между двумя замерами, мгновенного значения у неё не существует.
type Sampler struct {
	procPath string
	diskPath string

	mu       sync.Mutex
	prevIdle uint64
	prevAll  uint64
	prevAt   time.Time
}

// NewSampler собирает съёмщика метрик. procPath — куда смонтирован /proc
// хоста, diskPath — какую файловую систему мерить.
func NewSampler(procPath, diskPath string) *Sampler {
	if procPath == "" {
		procPath = "/proc"
	}
	if diskPath == "" {
		diskPath = "/"
	}
	return &Sampler{procPath: procPath, diskPath: diskPath}
}

// Sample снимает текущее состояние машины.
//
// Ошибки отдельных источников не прерывают замер: если не читается loadavg, то
// память всё равно нужна. Наружу уезжает первая встреченная — как пометка, что
// картина неполная.
func (s *Sampler) Sample() Host {
	var h Host
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	note(s.readCPU(&h))
	note(s.readMem(&h))
	note(s.readLoad(&h))
	note(s.readUptime(&h))
	note(s.readDisk(&h))

	if firstErr != nil {
		h.Err = firstErr.Error()
	}
	return h
}

// readCPU считает занятость процессора по разнице тиков в /proc/stat.
func (s *Sampler) readCPU(h *Host) error {
	f, err := os.Open(filepath.Join(s.procPath, "stat"))
	if err != nil {
		return fmt.Errorf("admin: /proc/stat: %w", err)
	}
	defer f.Close()

	var idle, all uint64
	cores := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		// Строка "cpu" — сумма по всем ядрам, строки "cpu0..N" — по одному.
		// Считаем занятость по сумме, а ядра — по числу отдельных строк.
		if fields[0] != "cpu" {
			cores++
			continue
		}
		for i, raw := range fields[1:] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				continue
			}
			all += v
			// Поля 4 и 5 (idle и iowait) — время, когда процессор ничего не
			// делал. iowait тоже простой: ядро ждёт диск, а не считает.
			if i == 3 || i == 4 {
				idle += v
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("admin: разбор /proc/stat: %w", err)
	}
	h.Cores = cores

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prevAll > 0 && all > s.prevAll {
		deltaAll := all - s.prevAll
		deltaIdle := idle - s.prevIdle
		h.CPUPercent = float64(deltaAll-deltaIdle) / float64(deltaAll) * 100
	}
	s.prevAll, s.prevIdle, s.prevAt = all, idle, time.Now()
	return nil
}

// readMem читает /proc/meminfo.
//
// «Занято» считается как MemTotal - MemAvailable, а не как total - free:
// страничный кэш освобождается по первому требованию, и вычитать его из
// свободной памяти значит пугать себя цифрой 95 % на здоровой машине.
func (s *Sampler) readMem(h *Host) error {
	values, err := readKV(filepath.Join(s.procPath, "meminfo"))
	if err != nil {
		return fmt.Errorf("admin: /proc/meminfo: %w", err)
	}
	h.MemTotal = values["MemTotal"] * 1024
	available := values["MemAvailable"] * 1024
	if available > h.MemTotal {
		available = h.MemTotal
	}
	h.MemUsed = h.MemTotal - available
	h.MemCache = (values["Cached"] + values["Buffers"]) * 1024
	h.SwapTotal = values["SwapTotal"] * 1024
	h.SwapUsed = h.SwapTotal - values["SwapFree"]*1024
	return nil
}

// readKV разбирает файлы вида "Ключ:  1234 kB".
func readKV(path string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string]uint64, 64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			out[key] = v
		}
	}
	return out, scanner.Err()
}

func (s *Sampler) readLoad(h *Host) error {
	raw, err := os.ReadFile(filepath.Join(s.procPath, "loadavg"))
	if err != nil {
		return fmt.Errorf("admin: /proc/loadavg: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return fmt.Errorf("admin: /proc/loadavg: неожиданный формат")
	}
	h.Load1, _ = strconv.ParseFloat(fields[0], 64)
	h.Load5, _ = strconv.ParseFloat(fields[1], 64)
	h.Load15, _ = strconv.ParseFloat(fields[2], 64)
	return nil
}

func (s *Sampler) readUptime(h *Host) error {
	raw, err := os.ReadFile(filepath.Join(s.procPath, "uptime"))
	if err != nil {
		return fmt.Errorf("admin: /proc/uptime: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return fmt.Errorf("admin: /proc/uptime: неожиданный формат")
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return fmt.Errorf("admin: /proc/uptime: %w", err)
	}
	h.UptimeSec = int64(sec)
	return nil
}

// readDisk мерит файловую систему через statfs.
//
// «Занято» считается по блокам, доступным непривилегированному процессу
// (Blocks - Bavail), а не по Bfree: резерв под root на ext4 — это пять
// процентов, которые для сервиса всё равно что заняты.
func (s *Sampler) readDisk(h *Host) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.diskPath, &st); err != nil {
		return fmt.Errorf("admin: statfs %s: %w", s.diskPath, err)
	}
	block := uint64(st.Bsize)
	h.DiskTotal = st.Blocks * block
	h.DiskUsed = (st.Blocks - st.Bavail) * block
	return nil
}
