package core_test

import (
	"io/fs"
	"path/filepath"
	"strings"
)

func walkProductionGoFiles(repoRoot string, visit func(path string, relativePath string) error) error {
	return filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, relativeErr := filepath.Rel(repoRoot, path)
		if relativeErr != nil {
			return relativeErr
		}
		if entry.IsDir() {
			if skipProductionGoScanDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(relativePath, ".go") || strings.HasSuffix(relativePath, "_test.go") {
			return nil
		}
		return visit(path, relativePath)
	})
}

func skipProductionGoScanDir(name string) bool {
	switch name {
	case ".git", "node_modules", "bin", "dist", "target", "vendor":
		return true
	default:
		return strings.HasPrefix(name, ".") && name != "."
	}
}
