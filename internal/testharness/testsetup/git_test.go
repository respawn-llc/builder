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

func TestInitializeRepositoryPinsInitialBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(home, ".gitconfig"),
		[]byte("[init]\n\tdefaultBranch = environment-default\n"),
		0o600,
	); err != nil {
		t.Fatalf("write test Git config: %v", err)
	}
	root := t.TempDir()
	if err := initializeRepository(root); err != nil {
		t.Fatalf("initializeRepository: %v", err)
	}
	if branch := RunGit(t, root, "branch", "--show-current"); branch != "main" {
		t.Fatalf("initial branch = %q, want main", branch)
	}
}

func TestInitializeRepositoryDisablesConfiguredCommitSigning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(home, ".gitconfig"),
		[]byte("[commit]\n\tgpgsign = true\n"),
		0o600,
	); err != nil {
		t.Fatalf("write test Git config: %v", err)
	}
	if err := initializeRepository(t.TempDir()); err != nil {
		t.Fatalf("initializeRepository with configured commit signing: %v", err)
	}
}

func TestRunGitDisablesAutomaticMaintenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(home, ".gitconfig"),
		[]byte("[maintenance]\n\tauto = true\n"),
		0o600,
	); err != nil {
		t.Fatalf("write test Git config: %v", err)
	}

	value, err := runGit(t.TempDir(), "config", "--bool", "maintenance.auto")
	if err != nil {
		t.Fatalf("read effective maintenance.auto: %v", err)
	}
	if value != "false" {
		t.Fatalf("effective maintenance.auto = %q, want false", value)
	}
}

func TestRunGitDisablesCommitSigning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(home, ".gitconfig"),
		[]byte("[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = kent-test-missing-gpg\n"),
		0o600,
	); err != nil {
		t.Fatalf("write test Git config: %v", err)
	}

	root := t.TempDir()
	if _, err := runGit(root, "init", "-q", "--initial-branch=main"); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if _, err := runGit(root, "add", "README.md"); err != nil {
		t.Fatalf("stage README: %v", err)
	}
	if _, err := runGit(root, "commit", "-q", "-m", "init"); err != nil {
		t.Fatalf("commit with signing configured globally: %v", err)
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
