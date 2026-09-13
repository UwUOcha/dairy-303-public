// Package config собирает настройки обоих демонов из переменных окружения.
//
// Ни одно значение, которое может смениться на стороне вуза (токен, базовый
// URL), не зашито в код: админ источника может крутануть токен в любой момент,
// и это не повод пересобирать бинарь.
package config

import (
	"fmt"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Rasp — конфигурация фонового демона raspd.
type Rasp struct {
	StaffInterval time.Duration
	// DBPath — файл SQLite. raspd единственный писатель.
	DBPath string
	// SocketPath — unix-сокет, на котором raspd слушает HTTP API.
	SocketPath string

	ProviderURL     string
	ProviderToken   string
	ImportLegacyIDs bool
	// FullSyncAt — время ночного полного обхода всех групп, минуты от полуночи.
	FullSyncAt int
	// HotInterval — период обновления «горячих» групп (тех, к которым привязаны юзеры).
	HotInterval time.Duration
	// GroupsInterval — период обновления дерева групп.
	GroupsInterval time.Duration
	// MonthsAhead — сколько месяцев вперёд тянуть при полном обходе (0 = только текущий).
	MonthsAhead int

	// StaleAfter — через сколько загруженный месяц требует догоняющего
	// запроса при обращении пользователя.
	StaleAfter time.Duration
	// WarnAfter — с какого возраста данных бот честно предупреждает человека,
	// что показывает не сегодняшнюю копию. Это не то же самое, что StaleAfter:
	// сходить за свежим мы пробуем задолго до того, как начинаем извиняться.
	WarnAfter time.Duration
	// MonthTimeout — потолок на загрузку одного месяца.
	MonthTimeout time.Duration

	// TZOffset — часовой пояс вуза в минутах от UTC. Расписание живёт по нему.
	TZOffset int
}

// Bot — конфигурация демона botd.
type Bot struct {
	// SocketPath — сокет raspd, куда ходит бот за данными.
	SocketPath string
	// TelegramToken — токен бота от @BotFather. Пусто = телеграм-адаптер не
	// запускается.
	TelegramToken string
	// VKToken — ключ доступа сообщества ВКонтакте. Пусто = VK-адаптер не
	// запускается.
	VKToken string
	// VKRequireSub — отвечать во ВКонтакте только подписчикам сообщества.
	// Выключается на время отладки: в тестовом сообществе состоять незачем.
	VKRequireSub bool
	// TZOffset — часовой пояс по умолчанию для новых пользователей.
	TZOffset int
	// AdminTG и AdminVK — кому уходят обращения из кнопки «Написать автору».
	// Пусто на обеих площадках — кнопки нет вовсе: писать в пустоту хуже, чем
	// не предлагать.
	//
	// Автор один, а площадок две, поэтому адресат выбирается один раз на всю
	// установку: обращение из ВКонтакте приезжает в телеграм, если телеграм
	// указан. Ответ уходит обратно на площадку человека — это знает очередь.
	//
	// Второй id при этом не лишний: он держит запасной путь на случай, когда
	// телеграм не задан, и он же — единственный способ узнать автора на его
	// площадке, если админских возможностей внутри бота станет больше.
	AdminTG string
	AdminVK string
	// LinkTG и LinkVK — куда звать друзей с каждой площадки.
	LinkTG string
	LinkVK string
}

// Admin — конфигурация админ-панели admind.
type Admin struct {
	TrustedProxies []netip.Prefix
	// SocketPath — сокет raspd: панель берёт статистику там же, где бот берёт
	// расписание, и базу не открывает вовсе.
	SocketPath string
	// Listen — адрес TCP-слушателя внутри контейнера.
	Listen string
	// BasePath — префикс, под которым панель живёт на домене ("/admin").
	// Нужен потому, что префикс не срезается прокси: иначе ссылки внутри
	// страницы указывали бы в корень чужого сайта.
	BasePath string
	// AllowIPs — кому открыта панель. Пустой список значит «никому»: панель
	// без вайтлиста это панель нараспашку, и падать тут честнее, чем пускать.
	AllowIPs []netip.Prefix
	// DBPath — своя маленькая база под историю метрик. Расписание она не
	// трогает: писатель у schedule.db по-прежнему ровно один.
	DBPath string
	// ProcPath — где смонтирован /proc хоста. Изнутри контейнера собственный
	// /proc показывает сам контейнер, а знать надо про машину.
	ProcPath string
	// DiskPath — какую файловую систему мерить.
	DiskPath string
	// SampleInterval — как часто снимать точку истории.
	SampleInterval time.Duration
	// Retention — сколько хранить историю.
	Retention time.Duration
	// TZOffset — часовой пояс для показа времени.
	TZOffset int
}

// LoadAdmin читает конфигурацию admind из окружения.
func LoadAdmin() (Admin, error) {
	c := Admin{
		SocketPath:     Socket(),
		Listen:         env("ADMIN_LISTEN", ":8305"),
		BasePath:       normalizeBase(env("ADMIN_BASE_PATH", "/admin")),
		DBPath:         env("ADMIN_DB", defaultAdminDB),
		ProcPath:       env("ADMIN_PROC", "/host/proc"),
		DiskPath:       env("ADMIN_DISK", "/"),
		SampleInterval: envDur("ADMIN_SAMPLE_INTERVAL", 5*time.Minute),
		Retention:      envDur("ADMIN_RETENTION", 30*24*time.Hour),
		TZOffset:       envInt("RASP_TZ_OFFSET", profile.Current().Offset()),
	}

	allow, err := ParsePrefixes(os.Getenv("ADMIN_ALLOW_IPS"))
	if err != nil {
		return c, err
	}
	if len(allow) == 0 {
		return c, fmt.Errorf("ADMIN_ALLOW_IPS пуст — панель без вайтлиста не поднимается")
	}
	c.AllowIPs = allow
	c.TrustedProxies, err = ParsePrefixes(os.Getenv("WEB_TRUSTED_PROXIES"))
	if err != nil {
		return c, fmt.Errorf("WEB_TRUSTED_PROXIES: %w", err)
	}
	return c, nil
}

// ParsePrefixes разбирает список адресов и подсетей через запятую или пробел.
//
// Голый адрес принимается наравне с подсетью и превращается в /32 (или /128):
// список в .env пишет человек, и требовать от него маску на каждый адрес — это
// способ однажды закрыть себе доступ опечаткой.
func ParsePrefixes(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if strings.Contains(field, "/") {
			p, err := netip.ParsePrefix(field)
			if err != nil {
				return nil, fmt.Errorf("ADMIN_ALLOW_IPS: %q не подсеть: %w", field, err)
			}
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("ADMIN_ALLOW_IPS: %q не адрес: %w", field, err)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// normalizeBase приводит префикс к виду "/admin" — с ведущей косой и без
// хвостовой. Пустая строка значит «панель в корне».
func normalizeBase(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

const (
	defaultSocket  = "/run/rasp/api.sock"
	defaultDB      = "/var/lib/rasp/schedule.db"
	defaultAdminDB = "/var/lib/admin/admin.db"
	// moscowOffset — вуз живёт по московскому времени.
	moscowOffset = 180
)

// Socket — путь к сокету raspd.
//
// Отдельно от LoadRasp и LoadBot: пробе живости (raspd -health) не нужны ни
// токены, ни остальная конфигурация — только куда стучаться.
func Socket() string { return env("RASP_SOCKET", defaultSocket) }

// LoadRasp читает конфигурацию raspd из окружения.
func LoadRasp() (Rasp, error) {
	c := Rasp{
		StaffInterval:   envDur("RASP_STAFF_INTERVAL", 24*time.Hour),
		DBPath:          env("RASP_DB", defaultDB),
		SocketPath:      Socket(),
		ProviderURL:     env("PROVIDER_URL", "http://127.0.0.1:8310"),
		ProviderToken:   os.Getenv("PROVIDER_TOKEN"),
		ImportLegacyIDs: envBool("RASP_IMPORT_LEGACY_IDS", false),
		FullSyncAt:      envInt("RASP_FULL_SYNC_AT", 3*60),
		HotInterval:     envDur("RASP_HOT_INTERVAL", 25*time.Minute),
		GroupsInterval:  envDur("RASP_GROUPS_INTERVAL", 24*time.Hour),
		MonthsAhead:     envInt("RASP_MONTHS_AHEAD", 1),
		StaleAfter:      envDur("RASP_STALE_AFTER", 6*time.Hour),
		WarnAfter:       envDur("RASP_WARN_AFTER", 24*time.Hour),
		MonthTimeout:    envDur("RASP_MONTH_TIMEOUT", 2*time.Minute),
		TZOffset:        envInt("RASP_TZ_OFFSET", profile.Current().Offset()),
	}
	return c, nil
}

// Ссылки на бота по умолчанию. В окружении переопределяются, но зашиты
// здесь: без них кнопка «Поделиться» на свежей установке молчит, а это ровно
// та кнопка, ради которой её и жмут.
const (
	defaultLinkTG = ""
	defaultLinkVK = ""
)

// LoadBot читает конфигурацию botd из окружения.
//
// Платформы независимы: botd поднимает те адаптеры, для которых есть токен.
// Так можно держать телеграм в бою, а ВКонтакте отлаживать отдельным запуском,
// не поднимая двух копий бота на одном токене.
func LoadBot() (Bot, error) {
	c := Bot{
		SocketPath:    Socket(),
		TelegramToken: os.Getenv("TELEGRAM_TOKEN"),
		VKToken:       os.Getenv("VK_TOKEN"),
		VKRequireSub:  envBool("VK_REQUIRE_SUBSCRIPTION", true),
		TZOffset:      envInt("RASP_TZ_OFFSET", profile.Current().Offset()),
		AdminTG:       env("ADMIN_TG_ID", profile.Current().AdminTG),
		AdminVK:       env("ADMIN_VK_ID", profile.Current().AdminVK),
		LinkTG:        env("BOT_LINK_TG", profile.Current().TelegramURL),
		LinkVK:        env("BOT_LINK_VK", profile.Current().VKURL),
	}
	if c.TelegramToken == "" && c.VKToken == "" {
		return c, fmt.Errorf("не задан ни TELEGRAM_TOKEN, ни VK_TOKEN — боту не с чем работать")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func envBool(key string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func envFloat(key string, def float64) float64 {
	v, err := strconv.ParseFloat(os.Getenv(key), 64)
	if err != nil {
		return def
	}
	return v
}

func envDur(key string, def time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}
