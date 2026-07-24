package testsetup

import (
	"fmt"
	"sync"
	"testing"

	"core/internal/testharness/databaseseed"
	"core/server/metadata"
)

const metadataDatabaseRelativePath = "db/main.sqlite3"

type databaseSeed = databaseseed.Seed

var currentDatabaseSeed struct {
	once sync.Once
	seed databaseSeed
	err  error
}

func OpenStore(t testing.TB, persistenceRoot string) *metadata.Store {
	t.Helper()
	seed, err := migratedDatabaseSeed()
	if err != nil {
		t.Fatalf("prepare migrated metadata database seed: %v", err)
	}
	if err := materializeDatabaseSeed(seed, persistenceRoot); err != nil {
		t.Fatalf("materialize migrated metadata database seed: %v", err)
	}
	store, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close metadata store: %v", err)
		}
	})
	return store
}

func migratedDatabaseSeed() (databaseSeed, error) {
	currentDatabaseSeed.once.Do(func() {
		currentDatabaseSeed.seed, currentDatabaseSeed.err = createMigratedDatabaseSeed()
	})
	return currentDatabaseSeed.seed, currentDatabaseSeed.err
}

func createMigratedDatabaseSeed() (seed databaseSeed, resultErr error) {
	return databaseseed.Create(
		"kent-test-metadata-database-",
		metadataDatabaseRelativePath,
		func(root string) error {
			store, err := metadata.Open(root)
			if err != nil {
				return fmt.Errorf("open metadata database seed: %w", err)
			}
			if err := store.Close(); err != nil {
				return fmt.Errorf("close metadata database seed: %w", err)
			}
			return nil
		},
	)
}

func materializeDatabaseSeed(seed databaseSeed, persistenceRoot string) error {
	return seed.Materialize(persistenceRoot, metadataDatabaseRelativePath)
}
