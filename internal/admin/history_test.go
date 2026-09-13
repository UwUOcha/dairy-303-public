package admin

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openHistory(t *testing.T, retention time.Duration) *History {
	t.Helper()
	h, err := OpenHistory(context.Background(), filepath.Join(t.TempDir(), "admin.db"), retention)
	if err != nil {
		t.Fatalf("открытие базы истории: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// Уборка идёт при каждой записи: иначе файл рос бы вечно, а панель обещает
// постоянный размер.
func TestHistoryDropsOldSamples(t *testing.T) {
	h := openHistory(t, time.Hour)
	ctx := context.Background()
	now := time.Now()

	for _, age := range []time.Duration{3 * time.Hour, 30 * time.Minute, 0} {
		if err := h.Add(ctx, Sample{At: now.Add(-age).Unix(), CPUPercent: 5}); err != nil {
			t.Fatalf("запись точки: %v", err)
		}
	}

	got, err := h.Range(ctx, 24*time.Hour, 100)
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("точек = %d, ожидалось 2: трёхчасовая должна быть убрана", len(got))
	}
	if got[0].At >= got[1].At {
		t.Error("точки должны идти от старых к свежим — график читается слева направо")
	}
}

// Прореживание обязано сохранять последнюю точку: иначе график заканчивается
// раньше, чем числа над ним, и это выглядит как зависший сервис.
func TestHistoryKeepsNewestWhenThinning(t *testing.T) {
	h := openHistory(t, 30*24*time.Hour)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)

	// Сутки точек с шагом в пять минут — 288 штук.
	const points = 288
	var newest int64
	for i := points - 1; i >= 0; i-- {
		at := now.Add(-time.Duration(i) * 5 * time.Minute).Unix()
		if at > newest {
			newest = at
		}
		if err := h.Add(ctx, Sample{At: at, CPUPercent: float64(i)}); err != nil {
			t.Fatalf("запись точки: %v", err)
		}
	}

	got, err := h.Range(ctx, 25*time.Hour, 50)
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("выборка пуста")
	}
	if len(got) > 60 {
		t.Errorf("точек = %d, ожидалось не больше полусотни: прореживание не сработало", len(got))
	}
	if got[len(got)-1].At != newest {
		t.Errorf("последняя точка = %d, ожидалась самая свежая %d", got[len(got)-1].At, newest)
	}
}

// Точка на один и тот же момент времени одна: перезапуск демона в ту же
// секунду не должен ломать запись.
func TestHistoryIgnoresDuplicateMoment(t *testing.T) {
	h := openHistory(t, time.Hour)
	ctx := context.Background()
	at := time.Now().Unix()

	for i := 0; i < 2; i++ {
		if err := h.Add(ctx, Sample{At: at, CPUPercent: float64(i)}); err != nil {
			t.Fatalf("запись точки: %v", err)
		}
	}

	got, err := h.Range(ctx, time.Hour, 100)
	if err != nil {
		t.Fatalf("чтение истории: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("точек = %d, ожидалась 1", len(got))
	}
}

func TestHistoryEmptyRange(t *testing.T) {
	h := openHistory(t, time.Hour)
	got, err := h.Range(context.Background(), time.Hour, 100)
	if err != nil {
		t.Fatalf("пустая история не должна быть ошибкой: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("точек = %d, ожидалось 0", len(got))
	}
}
