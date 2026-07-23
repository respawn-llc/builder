package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenFullHistoryMaterializers = map[string]struct{}{"ReadEvents": {}, "SnapshotFromDir": {}}

const sessionTestSupportImport = "core/server/session/sessiontest"

func TestProductionCodeDoesNotMaterializeFullSessionEventLog(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	if err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipMaterializationScanDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, "\"") == sessionTestSupportImport {
				violations = append(violations, relPath+": production code must not import session test-support package "+sessionTestSupportImport)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, forbidden := forbiddenFullHistoryMaterializers[selector.Sel.Name]; forbidden {
				violations = append(violations, relPath+": production code must not call full session-history materializer "+selector.Sel.Name+" (use bounded reverse-read windows)")
			}
			return true
		})
		return nil
	}); err != nil {
		t.Fatalf("scan repository for full session-history materializers: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("full session-history materialization violations:\n%s", strings.Join(violations, "\n"))
	}
}
func skipMaterializationScanDir(name string) bool {
	switch name {
	case ".git", "node_modules", "bin", "dist", "target", "vendor":
		return true
	default:
		return strings.HasPrefix(name, ".") && name != "."
	}
}
