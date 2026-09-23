package botcore

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestGroupChatsAllowOncePerInterval(t *testing.T) {
	var g GroupChats
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	if !g.Allow("a", now) {
		t.Fatal("первое сообщение в чат не разрешено")
	}
	if g.Allow("a", now.Add(time.Minute)) {
		t.Error("второе сообщение через минуту разрешено — бот будет спамить")
	}
	if !g.Allow("b", now.Add(time.Minute)) {
		t.Error("соседний чат не должен зависеть от первого")
	}
	if !g.Allow("a", now.Add(GroupChatInterval)) {
		t.Error("через сутки напомнить можно снова")
	}
	if len(g.last) != 2 {
		t.Errorf("записей о чатах: %d", len(g.last))
	}
	g.Allow("c", now.Add(3*GroupChatInterval))
	if len(g.last) != 1 {
		t.Errorf("старые записи не вычищаются: %d", len(g.last))
	}
}

func TestGroupChatToastFitsSnackbar(t *testing.T) {
	// ВКонтакте обрезает плашку на 90 символах.
	if n := utf8.RuneCountInString(GroupChatToast); n > 90 {
		t.Errorf("подсказка длиной %d не влезет во всплывающую плашку", n)
	}
}

func TestGroupChatTextLink(t *testing.T) {
	if got := GroupChatText(""); got == "" {
		t.Error("без ссылки текст пустой")
	}
	if got := GroupChatText("vk.me/x"); !strings.Contains(got, "vk.me/x") {
		t.Errorf("ссылка потерялась: %q", got)
	}
}
