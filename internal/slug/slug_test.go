package slug

import (
	"strings"
	"testing"
)

func TestMake(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Inc":              "acme-inc",
		"  Héllo__World!! ":     "h-llo-world",
		"---":                   "org",
		"ABC123":                "abc123",
		strings.Repeat("a", 80): strings.Repeat("a", 50),
	} {
		if got := Make(in); got != want {
			t.Errorf("Make(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithSuffixIsValidAndUnique(t *testing.T) {
	a, b := WithSuffix("Acme"), WithSuffix("Acme")
	if a == b || !Valid(a) || !strings.HasPrefix(a, "acme-") {
		t.Errorf("WithSuffix: %q, %q", a, b)
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		"api": true, "my-api-2": true, "a": true,
		"": false, "-api": false, "api-": false, "My-Api": false, "my_api": false,
		strings.Repeat("a", 63): true, strings.Repeat("a", 64): false,
	} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v, want %v", s, !want, want)
		}
	}
}
