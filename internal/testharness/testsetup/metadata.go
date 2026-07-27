package testsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"core/internal/testharness/databaseseed"
	"core/server/metadata"
)

type metadataPersistenceRootLock struct {
	mutex      sync.Mutex
	references int
}

var metadataPersistenceRootLocks = struct {
	sync.Mutex
	locks map[string]*metadataPersistenceRootLock
}{
	locks: make(map[string]*metadataPersistenceRootLock),
}

func OpenStore(t testing.TB, persistenceRoot string) *metadata.Store {
	t.Helper()
	materializeCurrentDatabaseSeed(t, persistenceRoot)
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

func PrepareMetadataPersistenceRoot(t testing.TB, persistenceRoot string) {
	t.Helper()
	if err := prepareMetadataPersistenceRoot(persistenceRoot); err != nil {
		t.Fatalf("prepare metadata persistence root: %v", err)
	}
}

func prepareMetadataPersistenceRoot(persistenceRoot string) error {
	absolutePersistenceRoot, err := filepath.Abs(persistenceRoot)
	if err != nil {
		return fmt.Errorf("resolve metadata persistence root %q: %w", persistenceRoot, err)
	}
	release := acquireMetadataPersistenceRootLock(absolutePersistenceRoot)
	defer release()

	databasePath := filepath.Join(absolutePersistenceRoot, databaseseed.CurrentMetadataDatabaseRelativePath)
	if _, err := os.Stat(databasePath); err == nil {
		return validateMetadataPersistenceRoot(absolutePersistenceRoot)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat metadata database %q: %w", databasePath, err)
	}
	if err := materializeCurrentMetadataDatabase(absolutePersistenceRoot); err != nil {
		return fmt.Errorf("materialize migrated metadata database seed: %w", err)
	}
	return validateMetadataPersistenceRoot(absolutePersistenceRoot)
}

func acquireMetadataPersistenceRootLock(persistenceRoot string) func() {
	metadataPersistenceRootLocks.Lock()
	rootLock := metadataPersistenceRootLocks.locks[persistenceRoot]
	if rootLock == nil {
		rootLock = &metadataPersistenceRootLock{}
		metadataPersistenceRootLocks.locks[persistenceRoot] = rootLock
	}
	rootLock.references++
	metadataPersistenceRootLocks.Unlock()

	rootLock.mutex.Lock()
	return func() {
		rootLock.mutex.Unlock()

		metadataPersistenceRootLocks.Lock()
		defer metadataPersistenceRootLocks.Unlock()
		rootLock.references--
		if rootLock.references == 0 {
			delete(metadataPersistenceRootLocks.locks, persistenceRoot)
		}
	}
}

func validateMetadataPersistenceRoot(persistenceRoot string) error {
	store, err := metadata.Open(persistenceRoot)
	if err != nil {
		return fmt.Errorf("open metadata database: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close metadata database: %w", err)
	}
	return nil
}

func materializeCurrentDatabaseSeed(t testing.TB, persistenceRoot string) {
	t.Helper()
	if err := materializeCurrentMetadataDatabase(persistenceRoot); err != nil {
		t.Fatalf("materialize migrated metadata database seed: %v", err)
	}
}

func materializeCurrentMetadataDatabase(persistenceRoot string) error {
	return databaseseed.MaterializeCurrentMetadataDatabase(persistenceRoot, metadata.Open)
}
