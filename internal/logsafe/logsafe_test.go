package logsafe

import (
	"strings"
	"testing"
)

func TestLineStripsTerminalEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[33m1 warning found\x1b[0m": "1 warning found",
		"ok\x1b]0;evil title\x07 line":   "ok line",
		"progress\rOVERWRITE":            "progressOVERWRITE",
		"bell\x07 and \x1b[2K erase":     "bell and  erase",
		"tab\tkept":                      "tab\tkept",
		"unicode ✓ kept":                 "unicode ✓ kept",
	} {
		if got := Line(in); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFieldStripsLineBreaksAndBoundsLength(t *testing.T) {
	got := Field("/api\r\n{\"level\":\"ERROR\",\"msg\":\"forged\"}\x1b[31m")
	if strings.ContainsAny(got, "\r\n\x1b") {
		t.Fatalf("Field kept a line break or escape: %q", got)
	}
	long := Field("/" + strings.Repeat("a", 5000))
	if len(long) > maxFieldLen+len("…") {
		t.Fatalf("Field length %d, want <= %d", len(long), maxFieldLen+len("…"))
	}
}
