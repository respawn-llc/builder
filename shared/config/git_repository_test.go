package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectGitRepositoryReturnsStructuralResults(t *testing.T) {
	t.Run("present in ancestor", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatalf("Mkdir .git: %v", err)
		}
		nested := filepath.Join(root, "nested", "directory")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("MkdirAll nested: %v", err)
		}

		result := InspectGitRepository(nested)
		if _, ok := result.(GitRepositoryPresent); !ok {
			t.Fatalf("repository inspection result = %T, want present", result)
		}
	})

	t.Run("not repository", func(t *testing.T) {
		result := InspectGitRepository(t.TempDir())
		if _, ok := result.(GitNotRepository); !ok {
			t.Fatalf("repository inspection result = %T, want not repository", result)
		}
	})

	t.Run("inspection failed", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		result := InspectGitRepository(missing)
		failed, ok := result.(GitRepositoryInspectionFailed)
		if !ok {
			t.Fatalf("repository inspection result = %T, want inspection failed", result)
		}
		if !errors.Is(failed.Cause, os.ErrNotExist) {
			t.Fatalf("inspection failure cause = %v, want not-exist cause", failed.Cause)
		}
	})
}
