package auth

import "testing"

// Arbitrary stored hashes must never panic, hang or exhaust memory.
func FuzzVerifyPassword(f *testing.F) {
	good, _ := HashPassword("correct horse battery staple")
	for _, s := range []string{good, "", "$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=19456,t=4294967295,p=1$c2FsdHNhbHQ$aGFzaA", "$argon2id$v=19$m=19456,t=2,p=0$$"} {
		f.Add("pw", s)
	}
	f.Fuzz(func(t *testing.T, password, encoded string) {
		_, _ = VerifyPassword(password, encoded)
	})
}

func TestVerifyRejectsAbusiveCostParameters(t *testing.T) {
	for _, h := range []string{
		"$argon2id$v=19$m=4294967295,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",     // 4 TB of memory
		"$argon2id$v=19$m=19456,t=4294967295,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA", // effectively infinite time
		"$argon2id$v=19$m=19456,t=2,p=255$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if _, err := VerifyPassword("x", h); err != ErrInvalidHash {
			t.Errorf("accepted abusive parameters: %s", h)
		}
	}
}
