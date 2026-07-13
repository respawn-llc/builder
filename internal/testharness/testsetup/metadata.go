package testsetup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"core/server/metadata"
)

type databaseSeed struct {
	contents []byte
	mode     fs.FileMode
}

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
	root, err := os.MkdirTemp("", "kent-test-metadata-database-*")
	if err != nil {
		return databaseSeed{}, fmt.Errorf("create temporary metadata database seed root: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary metadata database seed root %q: %w", root, err))
		}
	}()

	store, err := metadata.Open(root)
	if err != nil {
		return databaseSeed{}, fmt.Errorf("open metadata database seed: %w", err)
	}
	if err := store.Close(); err != nil {
		return databaseSeed{}, fmt.Errorf("close metadata database seed: %w", err)
	}

	databasePath := filepath.Join(root, "db", "main.sqlite3")
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecarPath := databasePath + suffix
		_, err := os.Stat(sidecarPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return databaseSeed{}, fmt.Errorf("stat metadata database seed sidecar %q: %w", sidecarPath, err)
		default:
			return databaseSeed{}, fmt.Errorf("metadata database seed sidecar remained after close: %q", sidecarPath)
		}
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		return databaseSeed{}, fmt.Errorf("stat metadata database seed: %w", err)
	}
	contents, err := os.ReadFile(databasePath)
	if err != nil {
		return databaseSeed{}, fmt.Errorf("read metadata database seed: %w", err)
	}
	return databaseSeed{contents: contents, mode: info.Mode().Perm()}, nil
}

func materializeDatabaseSeed(seed databaseSeed, persistenceRoot string) error {
	databasePath := filepath.Join(persistenceRoot, "db", "main.sqlite3")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return fmt.Errorf("create metadata database directory: %w", err)
	}
	databaseFile, err := os.OpenFile(databasePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, seed.mode)
	if err != nil {
		return fmt.Errorf("create metadata database: %w", err)
	}
	_, writeErr := databaseFile.Write(seed.contents)
	closeErr := databaseFile.Close()
	if writeErr != nil {
		writeErr = fmt.Errorf("write metadata database: %w", writeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close metadata database: %w", closeErr)
	}
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return nil
}
