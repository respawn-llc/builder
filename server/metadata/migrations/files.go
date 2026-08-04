package migrations

import (
	"embed"
)

// FS is the authoritative metadata migration filesystem.
//
// Keeping the migration assets and provider construction together ensures
// production migrations and historical-version migration tests execute the
// same migration path.
//
//go:embed *.up.sql
var FS embed.FS
