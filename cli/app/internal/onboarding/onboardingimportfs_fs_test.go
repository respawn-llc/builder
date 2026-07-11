package onboarding

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteSymlinkValidatesSourceBeforeReplacingEmptyTarget(t *testing.T) {
	globalRoot := t.TempDir()
	targetPath := filepath.Join(globalRoot, "skills")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	_, err := ExecuteSymlink(targetPath, filepath.Join(t.TempDir(), "missing"), "skills", "skills source Codex")
	if err == nil {
		t.Fatal("expected missing source to fail")
	}
	info, statErr := os.Lstat(targetPath)
	if statErr != nil {
		t.Fatalf("expected target to remain after source validation failure: %v", statErr)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected target to remain plain directory, got mode %v", info.Mode())
	}
}

func TestRollbackCreatedPathsRemovesPartialSkillImportAfterCommandFailure(t *testing.T) {
	globalRoot := t.TempDir()
	skillSource := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(skillSource, 0o755); err != nil {
		t.Fatalf("mkdir skill source: %v", err)
	}
	created, err := ExecuteSymlink(filepath.Join(globalRoot, "skills"), skillSource, "skills", "skills source Claude Code")
	if err != nil {
		t.Fatalf("execute skill symlink: %v", err)
	}
	_, commandErr := ExecuteSymlink(filepath.Join(globalRoot, "prompts"), filepath.Join(t.TempDir(), "missing-prompts"), "slash command", "slash commands from Claude Code")
	if commandErr == nil {
		t.Fatal("expected command symlink to fail")
	}
	if err := RollbackCreatedPaths(created); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(globalRoot, "skills")); !os.IsNotExist(err) {
		t.Fatalf("expected partial skills symlink to be removed, got %v", err)
	}
}
