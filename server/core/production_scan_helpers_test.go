package core_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestProductionGoDoesNotImportRegexp(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	err := walkProductionGoFiles(repoRoot, func(path string, relativePath string) error {
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, importSpec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(importSpec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if importPath == "regexp" {
				violations = append(violations, relativePath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go files: %v", err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("production Go files import regexp:\n%s", strings.Join(violations, "\n"))
	}
}

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
