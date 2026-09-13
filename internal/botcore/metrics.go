package botcore

import (
	"sort"
	"sync"
	"sync/atomic"

	"github.com/UwUOcha/dairy-303-public/internal/api"
)

// Счётчики бота для админ-панели.
//
// Считаются от старта процесса и живут в памяти: складывать их в базу значило
// бы писать в неё из botd, чего вся архитектура старательно избегает. Панель
// получает счётчики отчётом (см. api.BotReport), а после перезапуска они
// честно начинаются с нуля — вместе с аптаймом, рядом с которым и показаны.
//
// Инкремент стоит одного атомарного сложения и стоит на пути каждого события,
// поэтому карта платформ заводится один раз при старте адаптеров, а горячий
// путь ходит только по указателю.

// Metrics — счётчики по площадкам.
type Metrics struct {
	mu sync.RWMutex
	by map[string]*platformCounters
}

type platformCounters struct {
	updates atomic.Int64
	errors  atomic.Int64
	sent    atomic.Int64
	failed  atomic.Int64
	blocked atomic.Int64
}

// NewMetrics создаёт пустой набор счётчиков.
func NewMetrics() *Metrics { return &Metrics{by: make(map[string]*platformCounters, 2)} }

// counters отдаёт счётчики площадки, заводя их при первом обращении.
func (m *Metrics) counters(platform string) *platformCounters {
	m.mu.RLock()
	c, ok := m.by[platform]
	m.mu.RUnlock()
	if ok {
		return c
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok = m.by[platform]; ok {
		return c
	}
	c = &platformCounters{}
	m.by[platform] = c
	return c
}

// Update отмечает принятое событие.
func (m *Metrics) Update(platform string) { m.counters(platform).updates.Add(1) }

// Error отмечает сценарий, закончившийся ошибкой: человек увидел «сервис
// недоступен».
func (m *Metrics) Error(platform string) { m.counters(platform).errors.Add(1) }

// Sent отмечает сообщение, отправленное по инициативе бота.
func (m *Metrics) Sent(platform string) { m.counters(platform).sent.Add(1) }

// Failed отмечает неудачную отправку.
func (m *Metrics) Failed(platform string) { m.counters(platform).failed.Add(1) }

// Blocked отмечает адресата, который заблокировал бота.
func (m *Metrics) Blocked(platform string) { m.counters(platform).blocked.Add(1) }

// Snapshot снимает счётчики в порядке имён площадок.
//
// Порядок фиксирован намеренно: панель рисует площадки строками, и от опроса к
// опросу они не должны меняться местами.
func (m *Metrics) Snapshot() []api.BotCounters {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]api.BotCounters, 0, len(m.by))
	for name, c := range m.by {
		out = append(out, api.BotCounters{
			Platform: name,
			Updates:  c.updates.Load(),
			Errors:   c.errors.Load(),
			Sent:     c.sent.Load(),
			Failed:   c.failed.Load(),
			Blocked:  c.blocked.Load(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform < out[j].Platform })
	return out
}
