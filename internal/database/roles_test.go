package database

import (
	"regexp"
	"testing"
)

func TestScramVerifierFormat(t *testing.T) {
	v, err := scramVerifier("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^SCRAM-SHA-256\$4096:[A-Za-z0-9+/]{22}==\$[A-Za-z0-9+/]{43}=:[A-Za-z0-9+/]{43}=$`)
	if !re.MatchString(v) {
		t.Fatalf("verifier %q has the wrong shape", v)
	}
	// Only characters that are safe inside a single-quoted SQL literal.
	if regexp.MustCompile(`'`).MatchString(v) {
		t.Fatal("verifier contains a quote")
	}
}

func TestScramVerifierIsDeterministicForASalt(t *testing.T) {
	salt := []byte("0123456789abcdef")
	a, _ := scramVerifierWith("pw-pw-pw-pw-pw-pw", salt, 4096)
	b, _ := scramVerifierWith("pw-pw-pw-pw-pw-pw", salt, 4096)
	c, _ := scramVerifierWith("pw-pw-pw-pw-pw-px", salt, 4096)
	if a != b || a == c {
		t.Fatal("verifier must depend only on password, salt and iterations")
	}
	if s1, _ := scramVerifier("pw-pw-pw-pw-pw-pw"); s1 == a {
		t.Fatal("random salts must differ from the fixed one")
	}
}
