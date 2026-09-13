package profile

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicConfigNeverIncludesOperationalFields(t *testing.T) {
	p := Default()
	p.AdminTG = "private-admin"
	p.BotKeyOwner = "private-owner"
	p.AssetsDir = "/private/assets"
	b, _ := json.Marshal(p.Public())
	if strings.Contains(string(b), "private") {
		t.Fatal("private installation configuration exposed")
	}
}
func TestProfileValidation(t *testing.T) {
	for _, mutate := range []func(*Profile){func(p *Profile) { p.Timezone = "bad/zone" }, func(p *Profile) { p.TermStarts = []string{"02-30"} }, func(p *Profile) { p.TelegramURL = "javascript:alert(1)" }, func(p *Profile) { p.PublicURL = "https://example.org/path" }, func(p *Profile) { p.Accent = "red; display:none" }} {
		p := Default()
		mutate(&p)
		if e := p.Validate(); e == nil {
			t.Fatal("invalid profile accepted")
		}
	}
}

func TestWebsiteMarkSeparateFromBotSignature(t *testing.T) {
	p := Default()
	p.AppName = "  Учебный портал"
	p.BrandMark = "long bot signature"
	if p.WebsiteMark() != "у." {
		t.Fatal(p.WebsiteMark())
	}
	p.WebMark = "<У>"
	if p.Public()["web_mark"] != "<У>" || p.Render("{{web_mark}}", true) != "&lt;У&gt;" {
		t.Fatal("mark missing or unescaped")
	}
}
