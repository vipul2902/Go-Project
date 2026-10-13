package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Runs on every request.
func BenchmarkClientIPBehindProxy(b *testing.B) {
	trusted, _ := ParsePrefixes("10.0.0.0/8")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.7:5000"
	r.Header.Set("X-Forwarded-For", "198.51.100.20, 10.1.1.1")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = clientIP(r, trusted)
	}
}
