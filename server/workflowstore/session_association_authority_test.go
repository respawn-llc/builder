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
	root := filepath.Join(testsetup.RepositoryRoot(t), "server")
	const owner = "designateCurrentTaskSessionAssociation"
	designationMethods := map[string]struct{}{
		"DesignateSerialCurrentSessionWorkflowNodeAssociation": {},
		"DesignateBranchCurrentSessionWorkflowNodeAssociation": {},
	}
	var calls int
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
				if _, designation := designationMethods[selector.Sel.Name]; !designation {
					return true
				}
				calls++
				if function.Name.Name != owner || filepath.Base(path) != "task_session_associations.go" {
					t.Errorf("%s calls %s outside %s", path, selector.Sel.Name, owner)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan current-association writers: %v", err)
	}
	if calls != len(designationMethods) {
		t.Fatalf("current-association designation calls = %d, want %d", calls, len(designationMethods))
	}
}
