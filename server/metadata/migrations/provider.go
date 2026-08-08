package migrations

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

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
