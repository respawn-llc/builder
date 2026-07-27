package pty

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrebuiltExecutable(t *testing.T) {
	const environmentName = "KENT_TEST_PTY_PREBUILT_EXECUTABLE"

	t.Run("absent", func(t *testing.T) {
		unsetEnvironment(t, environmentName)

		executable, err := PrebuiltExecutable(environmentName)
		if err != nil {
			t.Fatalf("resolve absent executable: %v", err)
		}
		if executable != nil {
			t.Fatal("expected executable to remain unconfigured")
		}
	})

	t.Run("executable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fixture")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write executable: %v", err)
		}
		t.Setenv(environmentName, path)

		got, err := PrebuiltExecutable(environmentName)
		if err != nil {
			t.Fatalf("resolve executable: %v", err)
		}
		if got == nil {
			t.Fatal("expected configured executable")
		}
		want, err := filepath.Abs(path)
		if err != nil {
			t.Fatalf("resolve executable path: %v", err)
		}
		if *got != want {
			t.Fatalf("executable mismatch: got %q, want %q", *got, want)
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

func TestExecutablePathRejectsNonExecutableUnixFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows treats regular generated files as executable")
	}
	path := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write non-executable fixture: %v", err)
	}
	if _, err := executablePath(path); err == nil {
		t.Fatal("validate non-executable path unexpectedly succeeded")
	}
}

func TestExecutableModeUsesPlatformExecutableRules(t *testing.T) {
	cases := map[string]struct {
		mode            os.FileMode
		operatingSystem string
		want            bool
	}{
		"executable Unix file": {
			mode:            0o755,
			operatingSystem: "darwin",
			want:            true,
		},
		"non-executable Unix file": {
			mode:            0o644,
			operatingSystem: "linux",
		},
		"Windows regular file": {
			mode:            0o644,
			operatingSystem: "windows",
			want:            true,
		},
		"Windows directory": {
			mode:            os.ModeDir | 0o755,
			operatingSystem: "windows",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := executableMode(testCase.mode, testCase.operatingSystem); got != testCase.want {
				t.Fatalf("executable mode = %t, want %t", got, testCase.want)
			}
		})
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
