// Package slug creates and validates URL-safe identifiers. The format matches
// the CHECK constraints in the schema and Kubernetes DNS-1123 labels.
package slug

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
	valid   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Make turns a display name into a slug, e.g. "Acme Inc" -> "acme-inc".
func Make(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 50 {
		s = strings.TrimRight(s[:50], "-")
	}
	if s == "" {
		return "org"
	}
	return s
}

// WithSuffix appends a short random suffix so generated slugs are unique
// without a check-then-insert race.
func WithSuffix(name string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return Make(name) + "-" + hex.EncodeToString(b)
}

// Valid reports whether s is a well-formed slug (1-63 chars, DNS-1123 label).
func Valid(s string) bool { return valid.MatchString(s) }
