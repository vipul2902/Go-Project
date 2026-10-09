package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected hash format: %s", hash)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Errorf("correct password rejected: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong password", hash)
	if err != nil || ok {
		t.Errorf("wrong password accepted: ok=%v err=%v", ok, err)
	}
}

func TestHashIsSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("identical hashes for identical passwords: salt not applied")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, h := range []string{"", "plaintext", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=x$salt$hash", "$argon2id$v=19$m=1,t=1,p=1$!!$!!"} {
		if _, err := VerifyPassword("pw", h); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("VerifyPassword(%q) err = %v, want ErrInvalidHash", h, err)
		}
	}
}
