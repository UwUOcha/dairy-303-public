package logbuf

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Кольцо отдаёт записи от свежих к старым и вытесняет самые старые. Порядок
// важен: панель показывает первые несколько строк, и это должны быть последние
// события, а не первые за жизнь процесса.
func TestRingKeepsNewestFirst(t *testing.T) {
	ring := NewRing(3)
	for _, msg := range []string{"первая", "вторая", "третья", "четвёртая"} {
		ring.Add(Entry{At: time.Now(), Level: "WARN", Msg: msg})
	}

	got := ring.Entries()
	if len(got) != 3 {
		t.Fatalf("записей = %d, ожидалось 3", len(got))
	}
	want := []string{"четвёртая", "третья", "вторая"}
	for i, msg := range want {
		if got[i].Msg != msg {
			t.Errorf("запись %d = %q, ожидалась %q", i, got[i].Msg, msg)
		}
	}
}

func TestRingPartiallyFilled(t *testing.T) {
	ring := NewRing(5)
	ring.Add(Entry{Msg: "одна"})

	got := ring.Entries()
	if len(got) != 1 || got[0].Msg != "одна" {
		t.Errorf("записи = %+v, ожидалась одна", got)
	}
}

func TestRingEmpty(t *testing.T) {
	if got := NewRing(4).Entries(); len(got) != 0 {
		t.Errorf("пустое кольцо отдало %d записей", len(got))
	}
}

// Обработчик обязан остаться прозрачным: он оборачивает настоящий, а не
// заменяет его, иначе жалобы перестали бы попадать в journald.
func TestHandlerPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	ring := NewRing(10)
	log := slog.New(NewHandler(
		slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}), ring, slog.LevelWarn))

	log.Info("обычная работа")
	log.Warn("что-то не так", "группа", 39)

	if !strings.Contains(buf.String(), "обычная работа") {
		t.Error("info-запись не доехала до настоящего обработчика")
	}
	if !strings.Contains(buf.String(), "что-то не так") {
		t.Error("warn-запись не доехала до настоящего обработчика")
	}

	// В кольцо идёт только тревожное: INFO вытеснил бы единственную ошибку за
	// час двумя десятками строк «синк завершён».
	got := ring.Entries()
	if len(got) != 1 {
		t.Fatalf("в кольце %d записей, ожидалась одна", len(got))
	}
	if got[0].Msg != "что-то не так" {
		t.Errorf("запомнено %q, ожидалось предупреждение", got[0].Msg)
	}
	if !strings.Contains(got[0].Attrs, "группа=39") {
		t.Errorf("атрибуты = %q, ожидалось «группа=39»", got[0].Attrs)
	}
}

// Цепочка With должна доехать до кольца: без этого атрибуты, навешанные на
// логгер один раз при старте, терялись бы ровно в тех записях, ради которых
// кольцо и заводилось.
func TestHandlerKeepsWithAttrs(t *testing.T) {
	ring := NewRing(10)
	base := slog.New(NewHandler(
		slog.NewTextHandler(bytes.NewBuffer(nil), nil), ring, slog.LevelWarn))

	base.With("демон", "raspd").Error("упало", "код", 500)

	got := ring.Entries()
	if len(got) != 1 {
		t.Fatalf("в кольце %d записей, ожидалась одна", len(got))
	}
	if !strings.Contains(got[0].Attrs, "демон=raspd") || !strings.Contains(got[0].Attrs, "код=500") {
		t.Errorf("атрибуты = %q, ожидались оба набора", got[0].Attrs)
	}
	if got[0].Level != "ERROR" {
		t.Errorf("уровень = %q, ожидался ERROR", got[0].Level)
	}
}

// Родитель не должен видеть атрибуты потомка: slog вправе держать обоих сразу.
func TestHandlerWithDoesNotLeakIntoParent(t *testing.T) {
	ring := NewRing(10)
	handler := NewHandler(slog.NewTextHandler(bytes.NewBuffer(nil), nil), ring, slog.LevelWarn)

	parent := slog.New(handler)
	child := parent.With("платформа", "vk")

	child.Warn("детская")
	parent.Warn("родительская")

	got := ring.Entries()
	if len(got) != 2 {
		t.Fatalf("в кольце %d записей, ожидалось две", len(got))
	}
	if strings.Contains(got[0].Attrs, "платформа") {
		t.Errorf("родительская запись унаследовала атрибуты потомка: %q", got[0].Attrs)
	}
	if !strings.Contains(got[1].Attrs, "платформа=vk") {
		t.Errorf("детская запись потеряла свои атрибуты: %q", got[1].Attrs)
	}
}

// Уровни, выключенные обёрнутым обработчиком, не должны попадать в кольцо
// в обход его решения.
func TestHandlerRespectsInnerLevel(t *testing.T) {
	ring := NewRing(10)
	handler := NewHandler(
		slog.NewTextHandler(bytes.NewBuffer(nil), &slog.HandlerOptions{Level: slog.LevelError}),
		ring, slog.LevelWarn)

	if handler.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("обёртка включила уровень, выключенный настоящим обработчиком")
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Error("обёртка выключила уровень, включённый настоящим обработчиком")
	}
}
