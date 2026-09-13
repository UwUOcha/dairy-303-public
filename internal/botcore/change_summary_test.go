package botcore

import (
	"strings"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/schedule"
)

func summaryDay(before, after []schedule.Item) api.ChangeSummaryResponse {
	d := dayResponse()
	return api.ChangeSummaryResponse{Context: d.Context, Days: []api.ChangeDayResponse{{Context: d.Context, Date: d.Day.Date, Known: true, Before: dayResponse(before...).Day, After: dayResponse(after...).Day}}}
}

func TestPersonalSummaryConsequences(t *testing.T) {
	first := item(1, 510, 605, "08:30 - 10:05", "Анатомия")
	second := item(2, 625, 720, "10:25 - 12:00", "Химия")
	changed := second
	changed.Classroom = "412"
	changed.Flags = schedule.FlagRemote
	changed.Comments = "<новая ссылка>"
	text, kb := PersonalChangeMessage(summaryDay([]schedule.Item{first, second}, []schedule.Item{changed}))
	for _, want := range []string{"Анатомия в 08:30 — отменена", "Начало дня теперь в 10:25", "аудитория:", "412", "дистанционно", "&lt;новая ссылка&gt;"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if kb == nil || !strings.Contains(kb.Rows[0][0].Data, "2025-09-19") {
		t.Fatal("missing direct day button")
	}
}

func TestPersonalSummaryIgnoresUnchangedSubgroupAndRollback(t *testing.T) {
	first := item(1, 510, 605, "08:30 - 10:05", "Химия")
	for _, r := range []api.ChangeSummaryResponse{summaryDay([]schedule.Item{first}, []schedule.Item{first}), summaryDay(nil, nil)} {
		text, kb := PersonalChangeMessage(r)
		if text != "" || kb != nil {
			t.Fatalf("spurious notification: %s", text)
		}
	}
}

func TestPersonalSummaryMoveBetweenDays(t *testing.T) {
	old := item(1, 510, 605, "08:30 - 10:05", "Химия")
	moved := old
	moved.Date = "2025-09-21"
	r := summaryDay([]schedule.Item{old}, nil)
	r.Days = append(r.Days, api.ChangeDayResponse{Context: r.Context, Date: moved.Date,
		Before: schedule.Day{Date: moved.Date}, After: schedule.Day{Date: moved.Date, Items: []schedule.Item{moved}}})
	text, _ := PersonalChangeMessage(r)
	if !strings.Contains(text, "перенесена на") || !strings.Contains(text, "21 сентября") ||
		strings.Contains(text, "отменена") || strings.Contains(text, "добавлена") {
		t.Fatalf("move rendered as unrelated cancellation/addition: %s", text)
	}
}

func TestPersonalSummaryLongEscapedChangesKeepUsefulPreview(t *testing.T) {
	var before, after []schedule.Item
	for i := 1; i <= 12; i++ {
		old := item(i, 510+i, 605+i, "08:30 - 10:05", "Химия")
		changed := old
		changed.Comments = strings.Repeat("<&>", 200)
		before, after = append(before, old), append(after, changed)
	}
	text, kb := PersonalChangeMessage(summaryDay(before, after))
	if len([]rune(text)) > 3000 || !strings.Contains(text, "примечание:") || !strings.Contains(text, "Ещё правок:") || kb == nil {
		t.Fatalf("unhelpful or oversized summary (%d runes): %s", len([]rune(text)), text)
	}
	if strings.Contains(text, "<&>") {
		t.Fatal("unescaped upstream content")
	}
}
