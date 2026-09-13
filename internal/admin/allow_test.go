package admin

import (
	"github.com/UwUOcha/dairy-303-public/internal/config"
	"log/slog"
	"net/netip"
	"testing"
)

func TestAddressMembership(t *testing.T) {
	nets, e := config.ParsePrefixes("203.0.113.7,198.51.100.0/24,2001:db8::/64")
	if e != nil {
		t.Fatal(e)
	}
	a := NewAllow(nets, slog.Default())
	for _, tt := range []struct {
		ip   string
		want bool
	}{{"203.0.113.7", true}, {"::ffff:203.0.113.7", true}, {"198.51.100.5", true}, {"2001:db8::1", true}, {"203.0.113.8", false}, {"127.0.0.1", false}, {"10.0.0.1", false}} {
		if a.Permits(netip.MustParseAddr(tt.ip)) != tt.want {
			t.Error(tt.ip)
		}
	}
	if NewAllow(nil, slog.Default()).Permits(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("empty list permits access")
	}
}
