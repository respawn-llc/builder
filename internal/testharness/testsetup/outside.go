package testsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func NonTemporaryDirectory(
	t testing.TB,
	prefix string,
	isTemporary func(string) bool,
) string {
	t.Helper()
	if strings.TrimSpace(prefix) == "" {
		t.Fatal("non-temporary directory prefix is required")
	}
	if isTemporary == nil {
		t.Fatal("temporary-path classifier is required")
	}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve test working directory: %v", err)
	}
	moduleRoot, found := findModuleRoot(workingDir)
	if !found {
		t.Fatalf("find Go module root from %q", workingDir)
	}

	bases := []string{filepath.Dir(moduleRoot)}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && strings.TrimSpace(home) != "" {
		bases = append(bases, home)
	}
	for _, base := range bases {
		dir, mkdirErr := os.MkdirTemp(base, prefix)
		if mkdirErr != nil {
			continue
		}
		abs, absErr := filepath.Abs(dir)
		if absErr != nil || isTemporary(abs) || pathWithinRoot(abs, moduleRoot) {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				t.Fatalf("cleanup unsuitable outside directory %q: %v", dir, cleanupErr)
			}
			continue
		}
		t.Cleanup(func() {
			if cleanupErr := os.RemoveAll(abs); cleanupErr != nil {
				t.Errorf("cleanup outside directory %q: %v", abs, cleanupErr)
			}
		})
		return abs
	}

	t.Skip("unable to create a non-temporary directory outside the repository")
	return ""
}

func findModuleRoot(start string) (string, bool) {
	for current := filepath.Clean(start); ; current = filepath.Dir(current) {
		info, err := os.Stat(filepath.Join(current, "go.mod"))
		if err == nil && !info.IsDir() {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
	}
}

func pathWithinRoot(path string, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	return len(parts) > 0 && parts[0] != ".."
}
