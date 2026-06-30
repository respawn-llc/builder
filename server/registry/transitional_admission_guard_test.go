package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestTransitionalRunStartAdmissionAllowlist(t *testing.T) {
	repoRoot := registryTestRepoRoot(t)
	got := map[string][]string{}
	for _, rel := range productionGoFiles(t, repoRoot) {
		path := filepath.Join(repoRoot, rel)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "BeginSessionRun", "BeginExclusiveSessionRun", "SessionRunsBlocked":
				got[rel] = append(got[rel], selector.Sel.Name)
			}
			return true
		})
	}
	for rel := range got {
		sort.Strings(got[rel])
	}
	want := map[string][]string{
		"server/runtimecontrol/service.go": {"SessionRunsBlocked"},
		"server/sessionruntime/service.go": {"BeginExclusiveSessionRun", "BeginSessionRun"},
	}
	for rel := range want {
		sort.Strings(want[rel])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transitional run-start admission calls = %#v, want %#v", got, want)
	}
}

func registryTestRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func productionGoFiles(t *testing.T, repoRoot string) []string {
	t.Helper()
	roots := []string{
		"server/registry",
		"server/runtimeactivity",
		"server/runtimecontrol",
		"server/runtimeview",
		"server/sessionruntime",
		"server/sessionview",
		"server/worktree",
		"cli/app",
	}
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(files)
	return files
}
