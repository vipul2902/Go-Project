// Package migrations embeds the SQL schema migrations into the binary, so
// the migrate command needs no files on disk at runtime.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
