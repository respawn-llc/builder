package testsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeGitRepositoryMaterializesIsolatedCleanRepositories(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatalf("MkdirAll first repository: %v", err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("MkdirAll second repository: %v", err)
	}

	InitializeGitRepository(t, first)
	InitializeGitRepository(t, second)

	RunGit(t, first, "branch", "fixture-isolation")
	RunGit(t, second, "branch", "fixture-isolation")
	if err := os.WriteFile(filepath.Join(first, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write first README: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(second, "README.md"))
	if err != nil {
		t.Fatalf("read second README: %v", err)
	}
	if string(body) != "root\n" {
		t.Fatalf("second repository README = %q, want seed contents", body)
	}
}

func TestSanitizedGitEnvironmentDropsExternalConfigOverrides(t *testing.T) {
	environment := sanitizedGitEnvironment([]string{
		"GIT_CONFIG_GLOBAL=/tmp/global.gitconfig",
		"GIT_CONFIG_SYSTEM=/tmp/system.gitconfig",
		"GIT_CONFIG_NOSYSTEM=1",
		"KEEP=value",
	})
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM":
			t.Fatalf("sanitized environment retains %q", entry)
		}
	}
	if len(environment) != 1 || environment[0] != "KEEP=value" {
		t.Fatalf("sanitized environment = %q, want [KEEP=value]", environment)
	}
}
