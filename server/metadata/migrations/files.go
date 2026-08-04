package migrations

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// FS is the authoritative metadata migration filesystem.
//
// Keeping the migration assets and provider construction together ensures
// production migrations and historical-version migration tests execute the
// same migration path.
//
//go:embed *.up.sql
var FS embed.FS

func NewProvider(db *sql.DB, logger goose.Logger) (*goose.Provider, error) {
	if db == nil {
		return nil, fmt.Errorf("metadata migration database is required")
	}
	if logger == nil {
		logger = goose.NopLogger()
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		FS,
		goose.WithLogger(logger),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return nil, fmt.Errorf("create metadata migration provider: %w", err)
	}
	return provider, nil
}
