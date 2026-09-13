package store

import (
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	p := profile.Default()
	p.University = "Тестовый университет"
	p.Timezone = "Europe/Moscow"
	p.PublicURL = "https://schedule.example.edu"
	p.TelegramURL = "https://t.me/example_bot"
	p.VKURL = "https://vk.me/example_bot"
	p.BotKeyOwner = "1001"
	p.BrandMark = "Расписание от Автора ✶"
	p.BrandSign = "— Автор ★"
	profile.Set(p)
	os.Exit(m.Run())
}
