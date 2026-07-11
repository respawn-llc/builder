//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/config"
)

func TestWindowsServiceRunArgumentsBakesRoot(t *testing.T) {
	got := windowsServiceRunArguments(`C:\Roots\kent`)
	want := []string{"service", "run", "--persistence-root", `C:\Roots\kent`}
	if !commandArgsEqual(got, want) {
		t.Fatalf("windowsServiceRunArguments = %#v, want %#v", got, want)
	}
}

func TestWindowsServiceRunArgumentsOmitsEmptyRoot(t *testing.T) {
	got := windowsServiceRunArguments("   ")
	want := []string{"service", "run"}
	if !commandArgsEqual(got, want) {
		t.Fatalf("windowsServiceRunArguments = %#v, want %#v", got, want)
	}
}

func TestWindowsServerPIDPathUnderServiceDir(t *testing.T) {
	spec := serviceSpec{Config: config.App{PersistenceRoot: `C:\Roots\kent`}}
	got := windowsServerPIDPath(spec)
	want := filepath.Join(`C:\Roots\kent`, "service", "server.pid")
	if got != want {
		t.Fatalf("windowsServerPIDPath = %q, want %q", got, want)
	}
}

func TestRemoveWindowsRuntimeMetadata(t *testing.T) {
	t.Run("removes both files", func(t *testing.T) {
		spec := windowsMetadataTestSpec(t)
		writeWindowsMetadataFile(t, windowsServerPIDPath(spec))
		writeWindowsMetadataFile(t, installUserSIDPath(spec))

		if err := removeWindowsRuntimeMetadata(spec); err != nil {
			t.Fatalf("removeWindowsRuntimeMetadata: %v", err)
		}
		assertWindowsMetadataAbsent(t, windowsServerPIDPath(spec))
		assertWindowsMetadataAbsent(t, installUserSIDPath(spec))
	})

	for _, metadataPath := range []struct {
		name string
		path func(serviceSpec) string
	}{
		{name: "server PID absent", path: windowsServerPIDPath},
		{name: "install user SID absent", path: installUserSIDPath},
	} {
		t.Run(metadataPath.name, func(t *testing.T) {
			spec := windowsMetadataTestSpec(t)
			paths := []string{windowsServerPIDPath(spec), installUserSIDPath(spec)}
			for _, path := range paths {
				if path != metadataPath.path(spec) {
					writeWindowsMetadataFile(t, path)
				}
			}

			if err := removeWindowsRuntimeMetadata(spec); err != nil {
				t.Fatalf("removeWindowsRuntimeMetadata: %v", err)
			}
			for _, path := range paths {
				assertWindowsMetadataAbsent(t, path)
			}
		})
	}

	t.Run("non-removable server PID still removes install user SID", func(t *testing.T) {
		spec := windowsMetadataTestSpec(t)
		createNonEmptyWindowsMetadataDirectory(t, windowsServerPIDPath(spec))
		writeWindowsMetadataFile(t, installUserSIDPath(spec))

		if err := removeWindowsRuntimeMetadata(spec); err == nil {
			t.Fatal("expected metadata cleanup error")
		}
		assertWindowsMetadataAbsent(t, installUserSIDPath(spec))
	})

	t.Run("non-removable install user SID still removes server PID", func(t *testing.T) {
		spec := windowsMetadataTestSpec(t)
		writeWindowsMetadataFile(t, windowsServerPIDPath(spec))
		createNonEmptyWindowsMetadataDirectory(t, installUserSIDPath(spec))

		if err := removeWindowsRuntimeMetadata(spec); err == nil {
			t.Fatal("expected metadata cleanup error")
		}
		assertWindowsMetadataAbsent(t, windowsServerPIDPath(spec))
	})

	t.Run("joins simultaneous removal failures", func(t *testing.T) {
		spec := windowsMetadataTestSpec(t)
		createNonEmptyWindowsMetadataDirectory(t, windowsServerPIDPath(spec))
		createNonEmptyWindowsMetadataDirectory(t, installUserSIDPath(spec))

		err := removeWindowsRuntimeMetadata(spec)
		if err == nil {
			t.Fatal("expected metadata cleanup error")
		}
		joined, ok := err.(interface{ Unwrap() []error })
		if !ok {
			t.Fatalf("error %T does not expose joined failures", err)
		}
		if len(joined.Unwrap()) != 2 {
			t.Fatalf("joined failures = %d, want 2", len(joined.Unwrap()))
		}
	})
}

func windowsMetadataTestSpec(t *testing.T) serviceSpec {
	t.Helper()
	return serviceSpec{Config: config.App{PersistenceRoot: t.TempDir()}}
}

func writeWindowsMetadataFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create metadata directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("metadata"), 0o644); err != nil {
		t.Fatalf("write metadata file: %v", err)
	}
}

func createNonEmptyWindowsMetadataDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("create non-empty metadata directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "entry"), []byte("metadata"), 0o644); err != nil {
		t.Fatalf("write non-empty metadata directory entry: %v", err)
	}
}

func assertWindowsMetadataAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata path %q stat error = %v, want not exist", path, err)
	}
}
