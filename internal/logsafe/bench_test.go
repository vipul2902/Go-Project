package logsafe

import (
	"strings"
	"testing"
)

// Applied to every line of build output and container logs.
func BenchmarkLine(b *testing.B) {
	line := "#12 0.512 \x1b[33mwarning:\x1b[0m " + strings.Repeat("compiling package ", 6)
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	for b.Loop() {
		_ = Line(line)
	}
}
