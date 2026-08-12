package migrations

import (
	"embed"
)

// FS is the authoritative metadata migration filesystem.
//
// It starts at the minimum supported release baseline and contains every
// supported forward migration after that baseline.
//
//go:embed *.up.sql
var FS embed.FS
