package vk

import (
	"strings"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/botcore"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

func TestPlain(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "теги уходят, переносы остаются",
			in:   "<b>Пятница</b>\n<i>ГР-12</i>",
			want: "Пятница\nГР-12",
		},
		{
			// Цитата — единственный тег, у которого во ВКонтакте есть замена:
			// без полоски подробности пары сливались с её шапкой в простыню.
			name: "цитата отрисовывается полоской",
			in:   "<b>1 пара</b>\n<blockquote><b>Матан</b> · Лек\n<u>А-201</u> · Иванов</blockquote>\n",
			want: "1 пара\n│ Матан · Лек\n│ А-201 · Иванов",
		},
		{
			name: "после цитаты полоска не продолжается",
			in:   "<blockquote>внутри</blockquote>\nснаружи",
			want: "│ внутри\nснаружи",
		},
		{
			name: "неделя: у каждой пары своя полоска",
			in:   "<b>Понедельник, 11</b>\n<blockquote>1 · 08:30 · Биохимия\n2 · 10:25 · Токсикология</blockquote>\n",
			want: "Понедельник, 11\n│ 1 · 08:30 · Биохимия\n│ 2 · 10:25 · Токсикология",
		},
		{
			// Название дисциплины приходит от вуза и проходит через esc().
			// Если снять экранирование до тегов, «<» из него станет началом
			// тега и съест кусок сообщения.
			name: "экранирование снимается после тегов",
			in:   "<b>Тема &lt;b&gt;важно&lt;/b&gt;</b>",
			want: "Тема <b>важно</b>",
		},
		{
			name: "амперсанд и кавычки",
			in:   "<i>Иванов &amp; Ко &quot;тест&quot;</i>",
			want: `Иванов & Ко "тест"`,
		},
		{
			name: "одинокая скобка — это текст, а не тег",
			in:   "2 < 3 и всё",
			want: "2 < 3 и всё",
		},
		{
			name: "пустое остаётся пустым",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plain(tt.in); got != tt.want {
				t.Errorf("plain(%q) = %q, ожидалось %q", tt.in, got, tt.want)
			}
		})
	}
}

// Разметка не должна доезжать до человека ни из одного форматтера botcore.
func TestPlainStripsRealMessages(t *testing.T) {
	day := api.DayResponse{
		Context: api.Context{
			Group: store.Group{Name: "ГР-12"},
			Today: "2025-09-19",
		},
		Day: schedule.Day{
			Date:    "2025-09-19",
			Workday: true,
			Items: []schedule.Item{{
				Number: 1,
				Lesson: schedule.Lesson{
					TimeLabel:  "08:30 - 10:05",
					Discipline: "Анатомия & физиология",
					ClassType:  "лекция",
					Classroom:  "А-201",
					Staff:      []string{"Иванов И. И."},
				},
			}},
		},
	}

	got := plain(botcore.FormatDay(day))
	if strings.ContainsAny(got, "<>") {
		t.Errorf("в сообщении осталась разметка:\n%s", got)
	}
	for _, want := range []string{"Анатомия & физиология", "А-201", "Иванов И. И.", "08:30 - 10:05"} {
		if !strings.Contains(got, want) {
			t.Errorf("вместе с тегами потерялось %q:\n%s", want, got)
		}
	}
}

// Подпись длиннее сорока символов ВКонтакте не принимает — и отвергает при
// этом клавиатуру целиком. Названия институтов приходят от вуза, и одно из них
// в лимит не влезает; на нём обзор по институтам не открывался вовсе.
func TestLabel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "короткое не трогаем",
			in:   "Институт педагогики",
			want: "Институт педагогики",
		},
		{
			name: "ровно по лимиту",
			in:   strings.Repeat("я", maxLabelLen),
			want: strings.Repeat("я", maxLabelLen),
		},
		{
			name: "то самое название из базы вуза",
			in:   "Институт искусств и социокультурного проектирования",
			want: "Институт искусств и социокультурного…",
		},
		{
			// Границы слова нет — режем как есть, лишь бы влезло.
			name: "одно длинное слово",
			in:   strings.Repeat("я", 60),
			want: strings.Repeat("я", maxLabelLen-1) + cutMark,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := label(tt.in)
			if got != tt.want {
				t.Errorf("label(%q) = %q, ожидалось %q", tt.in, got, tt.want)
			}
			if labelLen(got) > maxLabelLen {
				t.Errorf("длина подписи %d при потолке %d", labelLen(got), maxLabelLen)
			}
		})
	}
}

// Значок за пределами базовой плоскости ВКонтакте считает за две позиции —
// меряем так же, иначе клавиатура с эмодзи в подписи уедет за лимит.
func TestLabelCountsEmojiAsTwo(t *testing.T) {
	if labelLen("📚") != 2 {
		t.Errorf("значок посчитан как %d позиций, ожидалось 2", labelLen("📚"))
	}
	long := "📚 " + strings.Repeat("я", 40)
	if got := labelLen(label(long)); got > maxLabelLen {
		t.Errorf("длина подписи со значком %d при потолке %d", got, maxLabelLen)
	}
}

func TestClamp(t *testing.T) {
	short := "коротко"
	if got := clamp(short); got != short {
		t.Errorf("короткое сообщение изменено: %q", got)
	}

	long := strings.Repeat("длинная строка расписания\n", 500)
	got := clamp(long)
	if len(got) > maxMessageLen {
		t.Errorf("длина после обрезки %d, лимит %d", len(got), maxMessageLen)
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Errorf("обрезанное сообщение не помечено: ...%q", got[len(got)-20:])
	}

	// Резать посреди руны нельзя: ВКонтакте получит битый UTF-8. Строка без
	// единого переноса — как раз тот случай, когда границу приходится искать
	// самим.
	solid := strings.Repeat("я", maxMessageLen)
	got = clamp(solid)
	if len(got) > maxMessageLen {
		t.Errorf("длина после обрезки %d, лимит %d", len(got), maxMessageLen)
	}
	if !utf8Valid(got) {
		t.Error("обрезка разрубила руну")
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}
