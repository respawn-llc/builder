package pty

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrebuiltExecutable(t *testing.T) {
	const environmentName = "KENT_TEST_PTY_PREBUILT_EXECUTABLE"

	t.Run("absent", func(t *testing.T) {
		unsetEnvironment(t, environmentName)

		if _, configured, err := PrebuiltExecutable(environmentName); err != nil {
			t.Fatalf("resolve absent executable: %v", err)
		} else if configured {
			t.Fatal("expected executable to remain unconfigured")
		}
	})

	t.Run("executable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fixture")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write executable: %v", err)
		}
		t.Setenv(environmentName, path)

		got, configured, err := PrebuiltExecutable(environmentName)
		if err != nil {
			t.Fatalf("resolve executable: %v", err)
		}
		if !configured {
			t.Fatal("expected configured executable")
		}
		want, err := filepath.Abs(path)
		if err != nil {
			t.Fatalf("resolve executable path: %v", err)
		}
		if got != want {
			t.Fatalf("executable mismatch: got %q, want %q", got, want)
		}
	})
}

func TestBuildOrUsePrebuiltPackage(t *testing.T) {
	const environmentName = "KENT_TEST_PTY_PREBUILT_EXECUTABLE"
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	t.Setenv(environmentName, path)

	got, err := BuildOrUsePrebuiltPackage(
		context.Background(),
		environmentName,
		"core/does-not-exist",
		filepath.Join(t.TempDir(), "must-not-build"),
	)
	if err != nil {
		t.Fatalf("use prebuilt executable: %v", err)
	}
	want, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve executable path: %v", err)
	}
	if got != want {
		t.Fatalf("executable mismatch: got %q, want %q", got, want)
	}
}

func TestBuildOrUsePrebuiltKent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write Kent executable: %v", err)
	}
	t.Setenv(KentBinaryEnvName, path)

	got, err := BuildOrUsePrebuiltKent(context.Background(), filepath.Join(t.TempDir(), "must-not-build"))
	if err != nil {
		t.Fatalf("use prebuilt Kent executable: %v", err)
	}
	want, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve Kent executable path: %v", err)
	}
	if got != want {
		t.Fatalf("Kent executable mismatch: got %q, want %q", got, want)
	}
}

func unsetEnvironment(t *testing.T, name string) {
	t.Helper()

	previous, configured := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if configured {
			if err := os.Setenv(name, previous); err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
			return
		}
		if err := os.Unsetenv(name); err != nil {
			t.Errorf("clear %s: %v", name, err)
		}
	})
}
