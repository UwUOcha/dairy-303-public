package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/logbuf"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// Здесь — та часть контракта, которой пользуется только админ-панель.
//
// Она вынесена отдельным файлом, потому что это единственный кусок API,
// который ничего не даёт боту: панель спрашивает про систему, бот — про
// расписание. Смешивать их в одном файле значит через полгода не понимать,
// что можно менять без оглядки на живых пользователей.

// Пути диагностических эндпоинтов.
const (
	// PathStats — снимок системы для панели.
	PathStats = "/stats"
	// PathStatsBot — сюда botd складывает свои счётчики. Направление
	// «от бота к демону», а не наоборот, потому что у botd нет ни одного
	// входящего порта и заводить его ради статистики не стоит.
	PathStatsBot = "/stats/bot"
)

// Runtime — как себя чувствует процесс Go.
//
// Эти цифры не заменяют системные (их даёт ядро), а дополняют: рост HeapAlloc
// при ровном RSS — это утечка в куче, а рост RSS при ровной куче — уже вопрос
// к аллокатору или к mmap sqlite.
type Runtime struct {
	StartedAt  int64  `json:"started_at"`
	UptimeSec  int64  `json:"uptime_sec"`
	Goroutines int    `json:"goroutines"`
	HeapAlloc  uint64 `json:"heap_alloc"`
	HeapSys    uint64 `json:"heap_sys"`
	Sys        uint64 `json:"sys"`
	NumGC      uint32 `json:"num_gc"`
	GoVersion  string `json:"go_version,omitempty"`
}

// SampleRuntime снимает состояние текущего процесса.
func SampleRuntime(startedAt time.Time) Runtime {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return Runtime{
		StartedAt:  startedAt.Unix(),
		UptimeSec:  int64(time.Since(startedAt).Seconds()),
		Goroutines: runtime.NumGoroutine(),
		HeapAlloc:  m.HeapAlloc,
		HeapSys:    m.HeapSys,
		Sys:        m.Sys,
		NumGC:      m.NumGC,
		GoVersion:  runtime.Version(),
	}
}

// BotCounters — что botd сделал с момента старта на одной площадке.
type BotCounters struct {
	Platform string `json:"platform"`
	// Updates — принято событий (сообщений и нажатий).
	Updates int64 `json:"updates"`
	// Errors — сценарий вернул ошибку. Человек в этом случае получает
	// «сервис недоступен», так что цифра прямо переводится в испорченные
	// впечатления.
	Errors int64 `json:"errors"`
	// Sent — отправлено сообщений по инициативе бота (рассылки и новости).
	Sent int64 `json:"sent"`
	// Failed — отправить не удалось даже после ретрая.
	Failed int64 `json:"failed"`
	// Blocked — адресат недостижим навсегда, уведомления выключены.
	Blocked int64 `json:"blocked"`
}

// BotReport — то, что botd присылает демону расписания.
type BotReport struct {
	Runtime   Runtime        `json:"runtime"`
	Platforms []BotCounters  `json:"platforms"`
	Log       []logbuf.Entry `json:"log,omitempty"`
}

// BotSnapshot — отчёт бота с отметкой, когда он приехал.
type BotSnapshot struct {
	BotReport
	// ReceivedAt — когда демон получил отчёт. По нему панель понимает, жив ли
	// бот: сам отчёт мог быть снят и час назад, если botd с тех пор молчит.
	ReceivedAt int64 `json:"received_at"`
}

// StatsResponse — полный снимок для панели.
type StatsResponse struct {
	// Now — время демона. Панель считает возраст всего остального от него, а
	// не от часов браузера: расхождение в пару минут иначе рисует «синк был
	// в будущем».
	Now int64 `json:"now"`
	// TZOffset — часовой пояс вуза, минуты от UTC.
	TZOffset int `json:"tz_offset"`

	Stats store.Stats `json:"stats"`
	Rasp  Runtime     `json:"rasp"`
	// SyncError — чем закончился последний проход фоновых контуров. Пусто,
	// когда всё хорошо.
	SyncError string `json:"sync_error,omitempty"`
	// Bot — последний отчёт botd; nil, если бот ещё ни разу не отчитался.
	Bot *BotSnapshot `json:"bot,omitempty"`
	// Log — последние жалобы самого raspd, свежие первыми.
	Log []logbuf.Entry `json:"log,omitempty"`
}

// diagnostics — источники данных панели, которых нет у остального API.
//
// Отдельная структура, а не пять полей в Server: их всех подключает один
// вызов и все они не нужны ни одному пользовательскому обработчику.
type diagnostics struct {
	mu        sync.RWMutex
	ring      *logbuf.Ring
	startedAt time.Time
	bot       *BotSnapshot
}

// Diagnostics подключает к серверу данные админ-панели: буфер жалоб и момент
// старта процесса.
//
// Вызов необязателен. Без него /stats продолжает отдавать статистику базы —
// просто без журнала и с нулевым аптаймом, — а тестам не приходится собирать
// половину демона ради одного обработчика.
func (s *Server) Diagnostics(ring *logbuf.Ring, startedAt time.Time) {
	s.diag.mu.Lock()
	defer s.diag.mu.Unlock()
	s.diag.ring = ring
	s.diag.startedAt = startedAt
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()

	st, err := s.db.Stats(ctx, now)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	s.diag.mu.RLock()
	ring, startedAt, bot := s.diag.ring, s.diag.startedAt, s.diag.bot
	s.diag.mu.RUnlock()

	resp := StatsResponse{
		Now: now.Unix(),
		// Смещение берём у самой зоны: она собрана из конфигурации в raspd, и
		// повторять её разбор в панели незачем.
		TZOffset: offsetMinutes(now, s.loc),
		Stats:    st,
		Rasp:     SampleRuntime(startedAt),
		Bot:      bot,
	}
	if err := s.sync.LastError(); err != nil {
		resp.SyncError = err.Error()
	}
	if ring != nil {
		resp.Log = ring.Entries()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStatsBot принимает отчёт botd.
//
// Отчёт целиком замещает предыдущий, а не складывается с ним: счётчики
// считаются от старта процесса, и суммировать два отчёта значило бы удвоить
// всё, что бот успел сделать.
func (s *Server) handleStatsBot(w http.ResponseWriter, r *http.Request) {
	var rep BotReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	s.diag.mu.Lock()
	s.diag.bot = &BotSnapshot{BotReport: rep, ReceivedAt: time.Now().Unix()}
	s.diag.mu.Unlock()
	writeJSON(w, http.StatusOK, struct{}{})
}

// offsetMinutes — смещение зоны в минутах от UTC.
func offsetMinutes(t time.Time, loc *time.Location) int {
	if loc == nil {
		return 0
	}
	_, off := t.In(loc).Zone()
	return off / 60
}
