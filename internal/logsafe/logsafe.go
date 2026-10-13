// Package logsafe sanitizes untrusted text (build output, container logs)
// before it is stored and later shown in terminals or the dashboard.
package logsafe

import (
	"regexp"
	"strings"
)

// ansiRE matches terminal escape sequences (CSI and OSC). Untrusted output
// could use them to re-color, hide or rewrite lines when displayed.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// Line removes escape sequences and control characters (keeping tabs).
func Line(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		return -1
	}, s)
}
