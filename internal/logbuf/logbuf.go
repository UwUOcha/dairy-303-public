// Package logbuf — кольцевой буфер последних жалоб демона.
//
// Мониторинг у проекта по средствам: логи в journald, перезапуск демоном
// инициализации, никакого Prometheus. Но чтобы увидеть, что именно сломалось,
// приходится идти по ssh и листать `docker compose logs` — а вопрос почти
// всегда один и тот же: «что тут ругалось за последний час».
//
// Буфер отвечает на него, не заводя ни агента, ни хранилища: последние
// несколько десятков записей уровня WARN и выше лежат в памяти процесса и
// отдаются админ-панели вместе с остальной статистикой. Логи при этом никуда
// не деваются — обработчик оборачивает настоящий, а не заменяет его.
package logbuf

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Capacity — сколько записей помнить. Это витрина «что сейчас не так», а не
// архив: за архивом идут в journald.
const Capacity = 50

// Entry — одна запомненная запись.
type Entry struct {
	At    time.Time `json:"at"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	// Attrs — атрибуты записи, сплющенные в «ключ=значение». Панель показывает
	// их строкой, а не деревом: раскрывать вложенные группы там нечем.
	Attrs string `json:"attrs,omitempty"`
	// Source — какой демон пожаловался. Проставляется на приёме, потому что
	// записи botd приезжают в raspd уже готовыми.
	Source string `json:"source,omitempty"`
}

// Ring — потокобезопасный кольцевой буфер записей.
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	// next — куда писать следующую; буфер заполняется по кругу.
	next int
	full bool
}

// NewRing создаёт буфер на capacity записей.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = Capacity
	}
	return &Ring{entries: make([]Entry, capacity)}
}

// Add кладёт запись, вытесняя самую старую.
func (r *Ring) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
}

// Entries возвращает записи от свежих к старым.
func (r *Ring) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := r.next
	if r.full {
		n = len(r.entries)
	}
	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		// Идём назад от последней записанной, оборачиваясь через начало.
		idx := (r.next - 1 - i + len(r.entries)*2) % len(r.entries)
		out = append(out, r.entries[idx])
	}
	return out
}

// Handler — slog.Handler, который пишет в настоящий обработчик и попутно
// запоминает жалобы.
//
// Обёртка, а не отдельный логгер: иначе каждое место, желающее попасть в
// буфер, пришлось бы звать дважды, и рано или поздно кто-то забыл бы.
type Handler struct {
	inner slog.Handler
	ring  *Ring
	// min — с какого уровня запоминать. INFO в буфер не идёт: он вытеснил бы
	// единственную ошибку за час двадцатью строчками «синк завершён».
	min slog.Level
	// attrs и group накоплены цепочкой With/WithGroup: slog отдаёт их
	// обработчику по одному, а в буфер нужна уже собранная строка.
	attrs []slog.Attr
	group string
}

// NewHandler оборачивает обработчик буфером.
func NewHandler(inner slog.Handler, ring *Ring, min slog.Level) *Handler {
	return &Handler{inner: inner, ring: ring, min: min}
}

// Enabled повторяет решение обёрнутого обработчика: буфер не должен включать
// уровни, которые иначе не писались бы вовсе.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle пишет запись в обёрнутый обработчик и, если она достаточно тревожна,
// кладёт её в кольцо.
func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= h.min {
		h.ring.Add(Entry{
			At:    rec.Time,
			Level: rec.Level.String(),
			Msg:   rec.Message,
			Attrs: h.formatAttrs(rec),
		})
	}
	return h.inner.Handle(ctx, rec)
}

// WithAttrs и WithGroup обязаны вернуть новый обработчик: slog вправе
// удерживать и родителя, и потомка одновременно.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.inner = h.inner.WithAttrs(attrs)
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &next
}

func (h *Handler) WithGroup(name string) slog.Handler {
	next := *h
	next.inner = h.inner.WithGroup(name)
	if name != "" {
		next.group = name
	}
	return &next
}

// formatAttrs сплющивает атрибуты записи в одну строку «ключ=значение».
func (h *Handler) formatAttrs(rec slog.Record) string {
	var b []byte
	appendAttr := func(a slog.Attr) {
		if a.Equal(slog.Attr{}) {
			return
		}
		if len(b) > 0 {
			b = append(b, ' ')
		}
		if h.group != "" {
			b = append(b, h.group...)
			b = append(b, '.')
		}
		b = append(b, a.Key...)
		b = append(b, '=')
		b = append(b, a.Value.String()...)
	}
	for _, a := range h.attrs {
		appendAttr(a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		appendAttr(a)
		return true
	})
	return string(b)
}
