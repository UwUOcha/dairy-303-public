package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/admin"
	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/config"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

// WithAdmin protects every admin document, asset and API call at the site boundary.
func WithAdmin(site, panel http.Handler, socket string, cfg config.Admin, log *slog.Logger) http.Handler {
	return adminGate(site, panel, api.NewClient(socket), cfg, log)
}
func adminGate(site, panel http.Handler, client authClient, cfg config.Admin, log *slog.Logger) http.Handler {
	allow := admin.NewAllow(cfg.AllowIPs, log)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin" && !strings.HasPrefix(r.URL.Path, "/admin/") {
			site.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Vary", "Cookie")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if r.Method != "GET" && r.Method != "HEAD" {
			writeError(w, 405, "Метод не поддерживается")
			return
		}
		ip, ok := adminClientIP(r, cfg.TrustedProxies)
		if !ok || !allow.Permits(ip) {
			writeError(w, 403, "Доступ к админке с этого адреса запрещён.")
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || len(cookie.Value) != 43 {
			if r.URL.Path == "/admin" || r.URL.Path == "/admin/" {
				http.Redirect(w, r, "/login?next=admin", http.StatusSeeOther)
			} else {
				writeError(w, 401, "Войдите через бота.")
			}
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		identity, err := client.WebAuth(ctx, store.WebAuthRequest{Op: "session", Token: cookie.Value})
		if err != nil {
			writeError(w, 503, "Проверка доступа временно недоступна.")
			return
		}
		if identity.Error != "" {
			writeError(w, 401, "Сеанс завершён. Войдите через бота.")
			return
		}
		if !profile.Current().IsAdmin(identity.Platform, identity.ExtID) {
			writeError(w, 403, "Доступ разрешён только администратору.")
			return
		}
		if r.URL.Path == "/admin/access" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"admin":true}`))
			return
		}
		r.Header.Del("If-None-Match")
		r.Header.Del("If-Modified-Since")
		panel.ServeHTTP(&privateWriter{w}, r)
	})
}

// Only explicitly configured proxy peers may supply a single, overwritten XFF.
// Never fall back to the proxy's own allowlisted address on malformed forwarding.
func adminClientIP(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	peer = peer.Unmap()
	for _, p := range trusted {
		if !p.Contains(peer) {
			continue
		}
		values := r.Header.Values("X-Forwarded-For")
		if len(values) != 1 {
			return netip.Addr{}, false
		}
		addr, e := netip.ParseAddr(strings.TrimSpace(values[0]))
		return addr.Unmap(), e == nil
	}
	return peer, true
}
