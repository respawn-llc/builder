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
		identityNames := workspaceIdentityNames(file)
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
				if isForbiddenAutomaticPathConstruction(current, identityNames) {
					t.Errorf("%s constructs an automatic path from Workspace identity outside managed_root_allocator.go", path)
				}
			case *ast.BinaryExpr:
				if current.Op == token.ADD &&
					containsWorkspaceID(current, identityNames) &&
					containsPathSeparator(current) {
					t.Errorf("%s constructs an automatic path by concatenating Workspace identity outside managed_root_allocator.go", path)
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

func workspaceIdentityNames(file *ast.File) map[string]bool {
	names := map[string]bool{"workspaceID": true, "workspaceId": true}
	for {
		changed := false
		ast.Inspect(file, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.AssignStmt:
				if len(current.Lhs) == 1 && len(current.Rhs) == 1 {
					if lhs, ok := current.Lhs[0].(*ast.Ident); ok && containsWorkspaceID(current.Rhs[0], names) && !names[lhs.Name] {
						names[lhs.Name] = true
						changed = true
					}
				}
			case *ast.ValueSpec:
				if len(current.Names) == 1 && len(current.Values) == 1 {
					if containsWorkspaceID(current.Values[0], names) && !names[current.Names[0].Name] {
						names[current.Names[0].Name] = true
						changed = true
					}
				}
			}
			return true
		})
		if !changed {
			return names
		}
	}
}

func containsWorkspaceID(expression ast.Expr, names map[string]bool) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.Ident:
			found = found || names[current.Name]
		case *ast.SelectorExpr:
			found = found || current.Sel.Name == "WorkspaceID" || current.Sel.Name == "ID"
		}
		return !found
	})
	return found
}

func isForbiddenAutomaticPathConstruction(call *ast.CallExpr, names map[string]bool) bool {
	if isPathJoinCall(call) {
		for _, argument := range call.Args {
			if containsWorkspaceID(argument, names) {
				return true
			}
		}
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Sprintf" {
		return false
	}
	if len(call.Args) < 2 {
		return false
	}
	format, ok := call.Args[0].(*ast.BasicLit)
	if !ok || !strings.Contains(format.Value, "/") && !strings.Contains(format.Value, `\`) {
		return false
	}
	for _, argument := range call.Args[1:] {
		if containsWorkspaceID(argument, names) {
			return true
		}
	}
	return false
}

func containsPathSeparator(expression ast.Expr) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if ok && literal.Kind == token.STRING &&
			(strings.Contains(literal.Value, "/") || strings.Contains(literal.Value, `\`)) {
			found = true
		}
		return !found
	})
	return found
}

func TestManagedWorktreePathOwnershipGuardDetectsWorkspaceIDJoinFixture(t *testing.T) {
	fixtures := []string{
		`package fixture
import "path/filepath"
func automatic(base string, workspace Workspace) string { return filepath.Join(base, workspace.ID, "leaf") }`,
		`package fixture
import "path/filepath"
func automatic(base string, workspace Workspace) string { key := workspace.ID; return filepath.Join(base, key, "leaf") }`,
		`package fixture
func automatic(base string, workspace Workspace) string { return base + "/" + workspace.ID + "/leaf" }`,
		`package fixture
import "fmt"
func automatic(base string, workspace Workspace) string { return fmt.Sprintf("%s/%s/leaf", base, workspace.ID) }`,
	}
	for index, source := range fixtures {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
		if err != nil {
			t.Fatalf("parse fixture %d: %v", index, err)
		}
		names := workspaceIdentityNames(file)
		found := false
		ast.Inspect(file, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.CallExpr:
				found = found || isForbiddenAutomaticPathConstruction(current, names)
			case *ast.BinaryExpr:
				found = found || current.Op == token.ADD &&
					containsWorkspaceID(current, names) &&
					containsPathSeparator(current)
			}
			return true
		})
		if !found {
			t.Fatalf("ownership guard fixture %d did not detect Workspace identity path construction", index)
		}
	}
}
