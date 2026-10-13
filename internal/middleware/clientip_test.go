package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	proxies, err := ParsePrefixes("10.0.0.0/8, 192.168.5.0/24")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, peer, xff string
		trusted         []netip.Prefix
		want            string
	}{
		{"no proxies configured: header ignored", "203.0.113.9:5000", "1.2.3.4", nil, "203.0.113.9"},
		{"untrusted peer cannot spoof", "203.0.113.9:5000", "1.2.3.4", proxies, "203.0.113.9"},
		{"trusted proxy: client is the hop before it", "10.0.0.7:5000", "198.51.100.20", proxies, "198.51.100.20"},
		// The client prepended a fake 1.2.3.4; the proxy appended the real IP.
		{"forged leftmost entry is ignored", "10.0.0.7:5000", "1.2.3.4, 198.51.100.20", proxies, "198.51.100.20"},
		{"chain of trusted proxies is skipped", "10.0.0.7:5000", "198.51.100.20, 192.168.5.3, 10.1.1.1", proxies, "198.51.100.20"},
		{"all hops trusted: fall back to peer", "10.0.0.7:5000", "10.9.9.9", proxies, "10.0.0.7"},
		{"malformed hop stops the walk", "10.0.0.7:5000", "198.51.100.20, not-an-ip", proxies, "10.0.0.7"},
		{"ipv4-mapped ipv6 normalized", "[::ffff:10.0.0.7]:5000", "198.51.100.20", proxies, "198.51.100.20"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.peer
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			got, ok := clientIP(r, c.trusted)
			if !ok || got.String() != c.want {
				t.Errorf("got %v (%v), want %s", got, ok, c.want)
			}
		})
	}
}

func TestParsePrefixesRejectsGarbage(t *testing.T) {
	if _, err := ParsePrefixes("10.0.0.0/8, nonsense"); err == nil {
		t.Error("invalid CIDR accepted")
	}
	if p, err := ParsePrefixes(""); err != nil || len(p) != 0 {
		t.Errorf("empty list: %v %v", p, err)
	}
}
