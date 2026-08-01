package worktree

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

func TestManagedWorktreePathOwnershipGuard(t *testing.T) {
	root := repositoryRoot(t)
	pkgs := testharness.LoadTypedPackages(t, root, false, "./server/worktree")
	pkg := testharness.PackageByPath(t, pkgs, "core/server/worktree")
	violations := typedPathOwnershipViolations(pkg)
	if len(violations) != 0 {
		t.Fatalf("managed worktree path ownership violations:\n%s", strings.Join(violations, "\n"))
	}
}

func typedPathOwnershipViolations(pkg *packages.Package) []string {
	workspaceType := workspaceTypeForPackage(pkg)
	violations := make([]string, 0)
	for _, file := range pkg.Syntax {
		if filepath.Base(pkg.Fset.Position(file.Pos()).Filename) == "managed_root_allocator.go" {
			continue
		}
		identities := workspaceIdentityObjects(pkg, file, workspaceType)
		separators := pathSeparatorObjects(pkg, file)
		ast.Inspect(file, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.FuncDecl:
				switch current.Name.Name {
				case "defaultWorktreeRoot", "defaultWorktreePathSeed", "shortRefName", "nextAvailableWorktreeRoot":
					violations = append(violations, "obsolete path constructor: "+current.Name.Name)
				}
			case *ast.Field:
				for _, name := range current.Names {
					if name.Name == "baseDir" {
						if typ, ok := current.Type.(*ast.Ident); ok && typ.Name == "string" {
							violations = append(violations, "direct Service baseDir state")
						}
					}
				}
			case *ast.CallExpr:
				if typedForbiddenPathCall(pkg, current, identities, separators) {
					violations = append(violations, "typed Workspace identity path construction")
				}
			case *ast.BinaryExpr:
				if current.Op == token.ADD &&
					typedContainsWorkspaceIdentity(pkg, current, identities, workspaceType) &&
					typedContainsPathSeparator(pkg, current, separators) {
					violations = append(violations, "typed Workspace identity path concatenation")
				}
			}
			return true
		})
	}
	return violations
}

func workspaceTypeForPackage(pkg *packages.Package) *types.Named {
	if imported := pkg.Imports["core/server/metadata/sqlitegen"]; imported != nil && imported.Types != nil {
		if object, ok := imported.Types.Scope().Lookup("Workspace").(*types.TypeName); ok {
			if named, ok := object.Type().(*types.Named); ok {
				return named
			}
		}
	}
	if object, ok := pkg.Types.Scope().Lookup("Workspace").(*types.TypeName); ok {
		if named, ok := object.Type().(*types.Named); ok {
			return named
		}
	}
	return nil
}

func workspaceIdentityObjects(pkg *packages.Package, file *ast.File, workspaceType *types.Named) map[types.Object]bool {
	objects := make(map[types.Object]bool)
	for _, object := range pkg.TypesInfo.Defs {
		if isWorkspaceIDSource(object) {
			objects[object] = true
		}
	}
	for _, object := range pkg.TypesInfo.Uses {
		if isWorkspaceIDSource(object) {
			objects[object] = true
		}
	}
	for {
		changed := false
		ast.Inspect(file, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.AssignStmt:
				if len(current.Lhs) == 1 && len(current.Rhs) == 1 {
					if lhs, ok := current.Lhs[0].(*ast.Ident); ok &&
						typedContainsWorkspaceIdentity(pkg, current.Rhs[0], objects, workspaceType) {
						object := pkg.TypesInfo.Defs[lhs]
						if object == nil {
							object = pkg.TypesInfo.Uses[lhs]
						}
						if object != nil && !objects[object] {
							objects[object] = true
							changed = true
						}
					}
				}
			case *ast.ValueSpec:
				if len(current.Names) == 1 && len(current.Values) == 1 &&
					typedContainsWorkspaceIdentity(pkg, current.Values[0], objects, workspaceType) {
					object := pkg.TypesInfo.Defs[current.Names[0]]
					if object != nil && !objects[object] {
						objects[object] = true
						changed = true
					}
				}
			}
			return true
		})
		if !changed {
			return objects
		}
	}
}

func typedContainsWorkspaceIdentity(pkg *packages.Package, expression ast.Expr, objects map[types.Object]bool, workspaceType *types.Named) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.Ident:
			if object := pkg.TypesInfo.Uses[current]; object != nil && objects[object] {
				found = true
			}
			if object := pkg.TypesInfo.Defs[current]; object != nil && objects[object] {
				found = true
			}
		case *ast.SelectorExpr:
			if object := pkg.TypesInfo.Uses[current.Sel]; object != nil && objects[object] {
				found = true
			}
			selection := pkg.TypesInfo.Selections[current]
			if selection != nil && workspaceType != nil && sameNamedType(selection.Recv(), workspaceType) && selection.Obj().Name() == "ID" {
				found = true
			}
		}
		return !found
	})
	return found
}

func pathSeparatorObjects(pkg *packages.Package, file *ast.File) map[types.Object]bool {
	objects := make(map[types.Object]bool)
	for {
		changed := false
		ast.Inspect(file, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.AssignStmt:
				if len(current.Lhs) == 1 && len(current.Rhs) == 1 {
					lhs, ok := current.Lhs[0].(*ast.Ident)
					if !ok || !typedContainsPathSeparator(pkg, current.Rhs[0], objects) {
						return true
					}
					object := pkg.TypesInfo.Defs[lhs]
					if object == nil {
						object = pkg.TypesInfo.Uses[lhs]
					}
					if object != nil && !objects[object] {
						objects[object] = true
						changed = true
					}
				}
			case *ast.ValueSpec:
				if len(current.Names) == 1 && len(current.Values) == 1 &&
					typedContainsPathSeparator(pkg, current.Values[0], objects) {
					object := pkg.TypesInfo.Defs[current.Names[0]]
					if object != nil && !objects[object] {
						objects[object] = true
						changed = true
					}
				}
			}
			return true
		})
		if !changed {
			return objects
		}
	}
}

func typedContainsPathSeparator(pkg *packages.Package, expression ast.Expr, objects map[types.Object]bool) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.BasicLit:
			if current.Kind == token.STRING || current.Kind == token.CHAR {
				value, err := strconv.Unquote(current.Value)
				if err == nil {
					for _, r := range value {
						if r == '/' || r == '\\' {
							found = true
							break
						}
					}
				}
			}
		case *ast.Ident:
			if object := pkg.TypesInfo.Uses[current]; object != nil && objects[object] {
				found = true
			}
			if object := pkg.TypesInfo.Defs[current]; object != nil && objects[object] {
				found = true
			}
		case *ast.SelectorExpr:
			object := pkg.TypesInfo.Uses[current.Sel]
			if object != nil && object.Pkg() != nil &&
				object.Pkg().Path() == "path/filepath" && object.Name() == "Separator" {
				found = true
			}
		}
		return !found
	})
	return found
}

func sameNamedType(typ types.Type, want *types.Named) bool {
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = pointer.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named == want
}

func typedForbiddenPathCall(pkg *packages.Package, call *ast.CallExpr, objects map[types.Object]bool, separators map[types.Object]bool) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	function, ok := pkg.TypesInfo.Uses[selector.Sel].(*types.Func)
	if !ok || function.Pkg() == nil {
		return false
	}
	if function.Pkg().Path() != "path/filepath" && function.Pkg().Path() != "path" && function.Pkg().Path() != "fmt" {
		return false
	}
	if function.Name() != "Join" && function.Name() != "Sprintf" {
		return false
	}
	for _, argument := range call.Args {
		if typedContainsWorkspaceIdentity(pkg, argument, objects, workspaceTypeForPackage(pkg)) &&
			(function.Name() == "Join" || typedContainsPathSeparator(pkg, call, separators)) {
			return true
		}
	}
	return false
}

func isWorkspaceIDSource(object types.Object) bool {
	variable, ok := object.(*types.Var)
	if !ok || !isStringType(variable.Type()) {
		return false
	}
	if variable.IsField() {
		return variable.Name() == "WorkspaceID" || variable.Name() == "SourceWorkspaceID"
	}
	return variable.Name() == "workspaceID" || variable.Name() == "workspaceId"
}

func isStringType(typ types.Type) bool {
	basic, ok := typ.Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsString != 0
}

func TestManagedWorktreePathOwnershipGuardDetectsTypedFixtures(t *testing.T) {
	fixtures := []string{
		`package fixture
import "path/filepath"
type Workspace struct { ID string }
func automatic(base string, workspace Workspace) string { return filepath.Join(base, workspace.ID, "leaf") }`,
		`package fixture
import "path/filepath"
type Workspace struct { ID string }
func automatic(base string, workspace Workspace) string { key := workspace.ID; return filepath.Join(base, key, "leaf") }`,
		`package fixture
import "path/filepath"
type Workspace struct { ID string }
func automatic(base string, workspace Workspace) string { return base + string(filepath.Separator) + workspace.ID }`,
		`package fixture
import "fmt"
type Workspace struct { ID string }
func automatic(base string, workspace Workspace) string { return fmt.Sprintf("%s%c%s", base, '/', workspace.ID) }`,
		`package fixture
import "path/filepath"
func managedRoot(base string, workspaceID string) string { return filepath.Join(base, workspaceID, "leaf") }`,
		`package fixture
import "path/filepath"
type Binding struct { WorkspaceID string }
func managedRoot(base string, binding Binding) string { return filepath.Join(base, binding.WorkspaceID, "leaf") }`,
	}
	for index, source := range fixtures {
		root := t.TempDir()
		testharness.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
		testharness.WriteFile(t, filepath.Join(root, "fixture", "fixture.go"), source)
		pkgs := testharness.LoadTypedPackages(t, root, false, "./fixture")
		pkg := testharness.PackageByPath(t, pkgs, "core/fixture")
		if len(typedPathOwnershipViolations(pkg)) == 0 {
			t.Fatalf("typed path ownership fixture %d was not rejected", index)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(workingDirectory, "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}
