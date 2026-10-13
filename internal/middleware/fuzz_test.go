package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Spoofing invariants for any X-Forwarded-For value an attacker can send.
func FuzzClientIP(f *testing.F) {
	f.Add("203.0.113.9:5000", "1.2.3.4")
	f.Add("10.0.0.7:5000", "1.2.3.4, 198.51.100.20")
	f.Add("10.0.0.7:5000", ",,, ::1 , garbage")
	trusted, _ := ParsePrefixes("10.0.0.0/8")
	f.Fuzz(func(t *testing.T, peer, xff string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-For", xff)
		got, ok := clientIP(r, trusted)
		if !ok {
			return
		}
		// An untrusted peer can never choose its IP via the header.
		if !isTrusted(got, trusted) {
			plain, _ := clientIP(r, nil)
			if !isTrusted(plain, trusted) && got != plain {
				t.Fatalf("untrusted peer %q obtained IP %v from X-Forwarded-For %q", peer, got, xff)
			}
		}
	})
}
