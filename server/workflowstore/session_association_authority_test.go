package workflowstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
)

func TestOnlyCurrentNodeBindingDesignatesCurrentSessionAssociations(t *testing.T) {
	methods := map[string]bool{"DesignateSerialCurrentSessionWorkflowNodeAssociation": false, "DesignateBranchCurrentSessionWorkflowNodeAssociation": false}
	root := filepath.Join(testsetup.RepositoryRoot(t), "server")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(root, "metadata", "sqlitegen") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if _, guarded := methods[selector.Sel.Name]; guarded {
					if methods[selector.Sel.Name] || function.Name.Name != "designateCurrentTaskSessionAssociation" {
						t.Errorf("current designation %s has a second caller", selector.Sel.Name)
					}
					methods[selector.Sel.Name] = true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for method, found := range methods {
		if !found {
			t.Errorf("current designation %s has no binding owner", method)
		}
	}
}
