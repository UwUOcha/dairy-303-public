// Package profile holds the validated, immutable installation configuration.
package profile

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata"
)

type Profile struct {
	AppName       string            `json:"app_name"`
	University    string            `json:"university"`
	LocationLabel string            `json:"location_label"`
	SourceName    string            `json:"source_name"`
	SourceURL     string            `json:"source_url"`
	PublicURL     string            `json:"public_url"`
	Timezone      string            `json:"timezone"`
	TermStarts    []string          `json:"term_starts"`
	TelegramURL   string            `json:"telegram_url"`
	VKURL         string            `json:"vk_url"`
	WebMark       string            `json:"web_mark"`
	BrandMark     string            `json:"brand_mark"`
	BrandSign     string            `json:"brand_sign"`
	Description   string            `json:"description"`
	Accent        string            `json:"accent"`
	AssetsDir     string            `json:"assets_dir"`
	Access        string            `json:"access"` // public or allowlist
	AdminTG       string            `json:"admin_tg"`
	AdminVK       string            `json:"admin_vk"`
	BotKeyOwner   string            `json:"bot_key_owner"`
	Texts         map[string]string `json:"texts"`
}

func Default() Profile {
	return Profile{AppName: "Между парами", University: "Университет", LocationLabel: "Мой университет", SourceName: "расписание вуза", Timezone: "UTC", TermStarts: []string{"02-01", "09-01"}, Access: "allowlist", Accent: "#4f6656"}
}

var active atomic.Pointer[Profile]

func init()            { p := Default(); active.Store(&p) }
func Current() Profile { return *active.Load() }
func Set(p Profile)    { active.Store(&p) }
func Load(path string) (Profile, error) {
	p := Default()
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return p, e
		}
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.DisallowUnknownFields()
		if e = d.Decode(&p); e != nil {
			return p, e
		}
		if d.Decode(&struct{}{}) != io.EOF {
			return p, fmt.Errorf("profile must contain one JSON object")
		}
	}
	return p, p.Validate()
}
func Init() error {
	p, e := Load(os.Getenv("RASP_PROFILE"))
	if e == nil {
		Set(p)
	}
	return e
}
func (p Profile) Validate() error {
	if strings.TrimSpace(p.AppName) == "" || strings.TrimSpace(p.University) == "" {
		return fmt.Errorf("profile needs app_name and university")
	}
	if _, e := time.LoadLocation(p.Timezone); e != nil {
		return fmt.Errorf("profile timezone: %w", e)
	}
	if p.Access != "public" && p.Access != "allowlist" {
		return fmt.Errorf("profile access must be public or allowlist")
	}
	for _, v := range []string{p.PublicURL, p.SourceURL, p.TelegramURL, p.VKURL} {
		if v != "" {
			u, e := url.Parse(v)
			if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
				return fmt.Errorf("invalid profile URL")
			}
		}
	}
	if p.PublicURL != "" {
		u, _ := url.Parse(p.PublicURL)
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("public_url must be an origin")
		}
	}
	if len(p.TermStarts) == 0 {
		return fmt.Errorf("term_starts cannot be empty")
	}
	seen := map[string]bool{}
	for _, s := range p.TermStarts {
		if _, e := time.Parse("2006-01-02", "2001-"+s); e != nil || seen[s] {
			return fmt.Errorf("invalid term start")
		}
		seen[s] = true
	}
	sort.Strings(p.TermStarts)
	if len(p.Accent) != 7 || p.Accent[0] != '#' || strings.Trim(p.Accent[1:], "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("accent must be #RRGGBB")
	}
	return nil
}
func (p Profile) Location() *time.Location { loc, _ := time.LoadLocation(p.Timezone); return loc }
func (p Profile) Offset() int              { _, seconds := time.Now().In(p.Location()).Zone(); return seconds / 60 }

// WebsiteMark is separate from the longer bot signature.
func (p Profile) WebsiteMark() string {
	if p.WebMark != "" {
		return p.WebMark
	}
	for _, r := range strings.TrimSpace(p.AppName) {
		return strings.ToLower(string(r)) + "."
	}
	return "м."
}
func (p Profile) Public() map[string]any {
	return map[string]any{"web_mark": p.WebsiteMark(), "app_name": p.AppName, "university": p.University, "location_label": p.LocationLabel, "source_name": p.SourceName, "source_url": p.SourceURL, "timezone": p.Timezone, "term_starts": p.TermStarts, "telegram_url": p.TelegramURL, "vk_url": p.VKURL, "accent": p.Accent}
}
func (p Profile) Text(key, fallback string) string {
	if s := p.Texts[key]; s != "" {
		return s
	}
	return fallback
}
func (p Profile) Render(s string, escape bool) string {
	value := func(s string) string {
		if escape {
			return html.EscapeString(s)
		}
		return s
	}
	return strings.NewReplacer("{{web_mark}}", value(p.WebsiteMark()), "{{university}}", value(p.University), "{{app_name}}", value(p.AppName), "{{source_name}}", value(p.SourceName), "{{public_url}}", value(p.PublicURL), "{{brand_mark}}", value(p.BrandMark), "{{brand_sign}}", value(p.BrandSign)).Replace(s)
}

// IsAdmin matches a verified platform identity against this installation's operators.
func (p Profile) IsAdmin(platform, id string) bool {
	if id == "" {
		return false
	}
	return (platform == "tg" && p.AdminTG != "" && id == p.AdminTG) || (platform == "vk" && p.AdminVK != "" && id == p.AdminVK)
}
