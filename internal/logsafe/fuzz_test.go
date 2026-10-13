package logsafe

import (
	"strings"
	"testing"
)

func FuzzLineHasNoControlCharacters(f *testing.F) {
	for _, s := range []string{"\x1b[31mred\x1b[0m", "\x1b]0;title\x07", "a\rb", "tab\tok", "\x1b[", "\x1b]unterminated"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Line(s)
		for _, r := range out {
			if r != '\t' && (r < 0x20 || r == 0x7f) {
				t.Fatalf("Line(%q) = %q still contains control character %U", s, out, r)
			}
		}
		if strings.ContainsRune(out, 0x1b) {
			t.Fatalf("escape character survived: %q", out)
		}
	})
}
