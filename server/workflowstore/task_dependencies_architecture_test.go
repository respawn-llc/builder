package workflowstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTaskDependencyMutationAdaptersHaveOneProductionOwner(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	repositoryRoot := filepath.Dir(filepath.Dir(filepath.Dir(currentFile)))
	owners := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "sqlitegen" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || filepath.Base(path) == "task_dependencies_architecture_test.go" || filepath.Base(path) == "task_dependencies_test.go" || filepath.Base(path) == "task_dependencies_schema_test.go" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			return err
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
			case "InsertTaskDependency", "DeleteTaskDependency":
				owners[path] = selector.Sel.Name
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk production Go sources: %v", err)
	}
	expected := filepath.Join(repositoryRoot, "server", "workflowstore", "task_dependencies.go")
	for path, operation := range owners {
		if path != expected {
			t.Fatalf("%s is called from %s; expected mutation owner %s", operation, path, expected)
		}
	}
}
