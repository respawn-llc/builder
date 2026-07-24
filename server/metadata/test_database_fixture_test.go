package metadata

import (
	"fmt"
	"sync"
	"testing"

	"core/internal/testharness/databaseseed"
)

const metadataTestDatabaseRelativePath = "db/main.sqlite3"

var metadataTestDatabaseFixture struct {
	once sync.Once
	seed databaseseed.Seed
	err  error
}

func installMetadataTestDatabase(t *testing.T, persistenceRoot string) {
	t.Helper()
	metadataTestDatabaseFixture.once.Do(func() {
		metadataTestDatabaseFixture.seed, metadataTestDatabaseFixture.err = databaseseed.Create(
			"kent-metadata-test-database-",
			metadataTestDatabaseRelativePath,
			func(root string) error {
				store, err := Open(root)
				if err != nil {
					return fmt.Errorf("migrate fixture database: %w", err)
				}
				if err := store.Close(); err != nil {
					return fmt.Errorf("close fixture database: %w", err)
				}
				return nil
			},
		)
	})
	if metadataTestDatabaseFixture.err != nil {
		t.Fatalf("create metadata test database fixture: %v", metadataTestDatabaseFixture.err)
	}
	if err := metadataTestDatabaseFixture.seed.Materialize(persistenceRoot, metadataTestDatabaseRelativePath); err != nil {
		t.Fatalf("materialize metadata test database fixture: %v", err)
	}
}
