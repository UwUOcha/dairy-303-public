package web

import (
	"bytes"
	"encoding/json"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"html"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func publicConfig(demo bool) map[string]any {
	p := profile.Current()
	c := p.Public()
	c["demo"] = demo
	c["admin_enabled"] = !demo && os.Getenv("ADMIN_ENABLED") == "true"
	c["beta"] = !demo && p.Access == "allowlist"
	return c
}
func profileHTML(body []byte, demo bool) []byte {
	p := profile.Current()
	c, _ := json.Marshal(publicConfig(demo))
	s := p.Render(string(body), true)
	s = strings.ReplaceAll(s, "{{config}}", html.EscapeString(string(c)))
	return []byte(s)
}

// Only explicit public asset names may be served from the operator's directory.
func profileAsset(w http.ResponseWriter, r *http.Request) bool {
	allowed := map[string]bool{"/icon.svg": true, "/app-icon-192.png": true, "/app-icon-512.png": true, "/og.png": true}
	p := profile.Current()
	if r.URL.Path == "/profile.css" {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(":root { --focus-ring: " + p.Accent + "; --accent-muted: " + p.Accent + "; }"))
		return true
	}
	if allowed[r.URL.Path] && p.AssetsDir != "" {
		path := filepath.Join(p.AssetsDir, strings.TrimPrefix(r.URL.Path, "/"))
		if b, e := os.ReadFile(path); e == nil {
			w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(path)))
			http.ServeContent(w, r, r.URL.Path, time.Time{}, bytes.NewReader(b))
			return true
		}
	}
	if strings.HasSuffix(r.URL.Path, ".webmanifest") {
		b, e := assets.ReadFile("assets/" + strings.TrimPrefix(r.URL.Path, "/"))
		if e != nil {
			return false
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil {
			return false
		}
		v["name"] = p.AppName + " · Расписание " + p.University
		v["short_name"] = p.AppName
		b, _ = json.Marshal(v)
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, r.URL.Path, time.Time{}, bytes.NewReader(b))
		return true
	}
	return false
}
