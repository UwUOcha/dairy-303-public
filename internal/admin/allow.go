package admin

import (
	"log/slog"
	"net/netip"
)

// Allow checks IP membership only. Authentication and trusted proxy resolution
// belong to the site's adminGate; there is no independently callable IP-only page.
type Allow struct{ nets []netip.Prefix }

func NewAllow(nets []netip.Prefix, _ *slog.Logger) *Allow { return &Allow{nets: nets} }
func (a *Allow) Permits(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, n := range a.nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}
