package auth

import (
	"testing"
	"time"
)

// argon2id is deliberately slow: this is the per-login cost (and why login
// is rate limited — each guess costs the server this much CPU and memory).
func BenchmarkHashPassword(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := HashPassword("correct horse battery staple"); err != nil {
			b.Fatal(err)
		}
	}
}

// Paid on every authenticated request.
func BenchmarkVerifyAccessToken(b *testing.B) {
	ti, _ := NewTokenIssuer([]byte("0123456789abcdef0123456789abcdef"), 15*time.Minute)
	tok, _, _ := ti.Issue("00000000-0000-4000-8000-000000000001")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ti.Verify(tok); err != nil {
			b.Fatal(err)
		}
	}
}
