package databaseseed

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSeedMaterializeWritesOnlyWithinPersistenceRoot(t *testing.T) {
	persistenceRoot := t.TempDir()
	seed := Seed{contents: []byte("seed database"), mode: 0o600}
	databaseRelativePath := filepath.Join("metadata", "store.db")

	if err := seed.Materialize(persistenceRoot, databaseRelativePath); err != nil {
		t.Fatalf("materialize database: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(persistenceRoot, databaseRelativePath))
	if err != nil {
		t.Fatalf("read materialized database: %v", err)
	}
	if string(contents) != string(seed.contents) {
		t.Fatalf("materialized database = %q, want %q", contents, seed.contents)
	}
}

func TestSeedMaterializeRejectsSymlinkedDirectoryOutsidePersistenceRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation requires elevated privileges")
	}

	persistenceRoot := t.TempDir()
	outsideRoot := t.TempDir()
	symlinkPath := filepath.Join(persistenceRoot, "outside")
	if err := os.Symlink(outsideRoot, symlinkPath); err != nil {
		t.Fatalf("create outside-root symlink: %v", err)
	}
	seed := Seed{contents: []byte("seed database"), mode: 0o600}
	databaseRelativePath := filepath.Join("outside", "nested", "store.db")

	if err := seed.Materialize(persistenceRoot, databaseRelativePath); err == nil {
		t.Fatal("materialize database unexpectedly followed outside-root symlink")
	}
	if _, err := os.Stat(filepath.Join(outsideRoot, "nested")); !os.IsNotExist(err) {
		t.Fatalf("outside-root directory stat error = %v, want not exist", err)
	}
}

func TestSeedMaterializeRejectsLocationsOutsidePersistenceRoot(t *testing.T) {
	seed := Seed{contents: []byte("seed database"), mode: 0o600}
	absolutePath := filepath.Join(t.TempDir(), "outside.db")
	cases := map[string]struct {
		persistenceRoot      string
		databaseRelativePath string
	}{
		"missing persistence root": {
			databaseRelativePath: "metadata/store.db",
		},
		"missing database path": {
			persistenceRoot: t.TempDir(),
		},
		"persistence root itself": {
			persistenceRoot:      t.TempDir(),
			databaseRelativePath: ".",
		},
		"parent directory": {
			persistenceRoot:      t.TempDir(),
			databaseRelativePath: "..",
		},
		"parent traversal": {
			persistenceRoot:      t.TempDir(),
			databaseRelativePath: filepath.Join("..", "outside.db"),
		},
		"absolute path": {
			persistenceRoot:      t.TempDir(),
			databaseRelativePath: absolutePath,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if err := seed.Materialize(testCase.persistenceRoot, testCase.databaseRelativePath); err == nil {
				t.Fatal("materialize database unexpectedly succeeded")
			}
		})
	}
}
