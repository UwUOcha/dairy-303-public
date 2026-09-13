package web

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"

	"github.com/UwUOcha/dairy-303-public/internal/buildinfo"
)

func sourceURL() string {
	value := os.Getenv("WEB_SOURCE_CODE_URL")
	if value == "" {
		value = buildinfo.SourceCodeURL
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return value
}

func serveSource(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/source" && r.URL.Path != "/license" && r.URL.Path != "/third-party" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/source" {
		name := "assets/license.txt"
		if r.URL.Path == "/third-party" {
			name = "assets/third-party.txt"
		}
		body, err := assets.ReadFile(name)
		if err != nil {
			http.Error(w, "License unavailable", 500)
			return true
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method != http.MethodHead {
			w.Write(body)
		}
		return true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return true
	}
	link := "Оператор этой установки должен указать адрес исходников своей версии."
	if value := sourceURL(); value != "" {
		link = `<a href="` + html.EscapeString(value) + `" rel="noopener noreferrer">Получить исходный код этой версии</a>`
	}
	fmt.Fprintf(w, `<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Исходный код и лицензия</title><body><main><h1>Исходный код и лицензия</h1><p>%s</p><p>Copyright © 2026 Mikhail (UwUOcha).</p><p>Платформа предоставляется без гарантий на условиях <a href="/license">GNU AGPL-3.0-only</a>. Библиотека контракта адаптера pkg/provider — MIT.</p><p><a href="/third-party">Лицензии сторонних компонентов</a></p><p><a href="/">Вернуться к расписанию</a></p></main></body></html>`, link)
	return true
}
