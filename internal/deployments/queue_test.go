package deployments

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateMessage(t *testing.T) {
	if got := truncateMessage("short", 10); got != "short" {
		t.Errorf("short message changed: %q", got)
	}
	got := truncateMessage(strings.Repeat("a", 20), 10)
	if got != strings.Repeat("a", 10)+" …[truncated]" {
		t.Errorf("ascii: %q", got)
	}
	// "é" is 2 bytes and "😀" is 4: try every cut position across them.
	msg := strings.Repeat("é", 5) + strings.Repeat("😀", 5)
	for max := 0; max < len(msg); max++ {
		out := truncateMessage(msg, max)
		if !utf8.ValidString(out) {
			t.Fatalf("max=%d produced invalid UTF-8: %q", max, out)
		}
		if body := strings.TrimSuffix(out, " …[truncated]"); len(body) > max || !strings.HasPrefix(msg, body) {
			t.Fatalf("max=%d: body %q is not a prefix within the limit", max, body)
		}
	}
}

func FuzzTruncateMessage(f *testing.F) {
	for _, s := range []string{"", "hello", "é😀x", strings.Repeat("ü", 100), "\xff\xfe invalid"} {
		f.Add(s, 5)
	}
	f.Fuzz(func(t *testing.T, msg string, max int) {
		if max < 0 || max > 1<<16 {
			t.Skip()
		}
		out := truncateMessage(msg, max)
		if len(msg) <= max {
			if out != msg {
				t.Fatalf("message within the limit was changed")
			}
			return
		}
		body := strings.TrimSuffix(out, " …[truncated]")
		if len(body) > max || !strings.HasPrefix(msg, body) {
			t.Fatalf("body %q exceeds %d or is not a prefix", body, max)
		}
		// Valid input must stay valid (never split a rune).
		if utf8.ValidString(msg) && !utf8.ValidString(out) {
			t.Fatalf("split a multi-byte character: %q", out)
		}
	})
}
