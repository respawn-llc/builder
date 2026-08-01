package worktree

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedWorktreePathOwnershipGuard(t *testing.T) {
	root := "."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "managed_root_allocator.go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.FuncDecl:
				switch current.Name.Name {
				case "defaultWorktreeRoot", "defaultWorktreePathSeed", "shortRefName", "nextAvailableWorktreeRoot":
					t.Errorf("%s reintroduced obsolete path constructor %s", path, current.Name.Name)
				}
			case *ast.Field:
				for _, name := range current.Names {
					if name.Name == "baseDir" {
						if structType, ok := current.Type.(*ast.Ident); ok && structType.Name == "string" {
							t.Errorf("%s retains direct Service baseDir state", path)
						}
					}
				}
			case *ast.CallExpr:
				if isPathJoinCall(current) {
					for _, argument := range current.Args {
						if containsWorkspaceID(argument) {
							t.Errorf("%s constructs an automatic path from WorkspaceID outside managed_root_allocator.go", path)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isPathJoinCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Join" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && (packageName.Name == "filepath" || packageName.Name == "path")
}

func containsWorkspaceID(expression ast.Expr) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.Ident:
			found = found || current.Name == "workspaceID" || current.Name == "workspaceId"
		case *ast.SelectorExpr:
			found = found || current.Sel.Name == "WorkspaceID"
		}
		return !found
	})
	return found
}

func TestManagedWorktreePathOwnershipGuardDetectsWorkspaceIDJoinFixture(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
import "path/filepath"
func automatic(base string, workspaceID string) string {
	return filepath.Join(base, workspaceID, "leaf")
}`, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && isPathJoinCall(call) {
			for _, argument := range call.Args {
				found = found || containsWorkspaceID(argument)
			}
		}
		return true
	})
	if !found {
		t.Fatal("ownership guard fixture did not detect WorkspaceID path construction")
	}
}
