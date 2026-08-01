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
	identity := workspaceIdentityObjects(pkg, workspaceType)
	violations := make([]string, 0)
	for _, file := range pkg.Syntax {
		if filepath.Base(pkg.Fset.Position(file.Pos()).Filename) == "managed_root_allocator.go" {
			continue
		}
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
				if typedForbiddenPathCall(pkg, current, identity, separators) {
					violations = append(violations, "typed Workspace identity path construction")
				}
			case *ast.BinaryExpr:
				if current.Op == token.ADD &&
					typedContainsWorkspaceIdentity(pkg, current, identity, workspaceType) &&
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

type workspaceIdentityAnalysis struct {
	objects   map[types.Object]bool
	returning map[*types.Func]bool
}

func workspaceIdentityObjects(pkg *packages.Package, workspaceType *types.Named) workspaceIdentityAnalysis {
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
	returning := make(map[*types.Func]bool)
	for {
		changed := false
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				switch current := node.(type) {
				case *ast.AssignStmt:
					if len(current.Lhs) == 1 && len(current.Rhs) == 1 {
						if lhs, ok := current.Lhs[0].(*ast.Ident); ok &&
							typedCarriesWorkspaceIdentity(pkg, current.Rhs[0], workspaceIdentityAnalysis{objects: objects, returning: returning}, workspaceType) {
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
						typedCarriesWorkspaceIdentity(pkg, current.Values[0], workspaceIdentityAnalysis{objects: objects, returning: returning}, workspaceType) {
						object := pkg.TypesInfo.Defs[current.Names[0]]
						if object != nil && !objects[object] {
							objects[object] = true
							changed = true
						}
					}
				case *ast.CallExpr:
					callee := calledFunction(pkg, current)
					if callee != nil {
						signature, ok := callee.Type().(*types.Signature)
						if ok {
							for index, argument := range current.Args {
								if index >= signature.Params().Len() ||
									!typedCarriesWorkspaceIdentity(pkg, argument, workspaceIdentityAnalysis{objects: objects, returning: returning}, workspaceType) {
									continue
								}
								parameter := signature.Params().At(index)
								if !objects[parameter] {
									objects[parameter] = true
									changed = true
								}
							}
						}
					}
				case *ast.FuncDecl:
					callee, ok := pkg.TypesInfo.Defs[current.Name].(*types.Func)
					if !ok || current.Body == nil {
						break
					}
					ast.Inspect(current.Body, func(bodyNode ast.Node) bool {
						returnStmt, ok := bodyNode.(*ast.ReturnStmt)
						if !ok {
							return true
						}
						for _, result := range returnStmt.Results {
							if typedCarriesWorkspaceIdentity(pkg, result, workspaceIdentityAnalysis{objects: objects, returning: returning}, workspaceType) &&
								!returning[callee] {
								returning[callee] = true
								changed = true
							}
						}
						return true
					})
				}
				return true
			})
		}
		if !changed {
			return workspaceIdentityAnalysis{objects: objects, returning: returning}
		}
	}
}

func calledFunction(pkg *packages.Package, call *ast.CallExpr) *types.Func {
	switch function := call.Fun.(type) {
	case *ast.Ident:
		result, _ := pkg.TypesInfo.Uses[function].(*types.Func)
		return result
	case *ast.SelectorExpr:
		result, _ := pkg.TypesInfo.Uses[function.Sel].(*types.Func)
		return result
	default:
		return nil
	}
}

func typedCarriesWorkspaceIdentity(pkg *packages.Package, expression ast.Expr, identity workspaceIdentityAnalysis, workspaceType *types.Named) bool {
	switch current := expression.(type) {
	case *ast.Ident:
		return identity.objects[pkg.TypesInfo.Uses[current]] || identity.objects[pkg.TypesInfo.Defs[current]]
	case *ast.SelectorExpr:
		if object := pkg.TypesInfo.Uses[current.Sel]; identity.objects[object] {
			return true
		}
		selection := pkg.TypesInfo.Selections[current]
		return selection != nil &&
			workspaceType != nil &&
			sameNamedType(selection.Recv(), workspaceType) &&
			selection.Obj().Name() == "ID"
	case *ast.CallExpr:
		if callee := calledFunction(pkg, current); callee != nil {
			return identity.returning[callee]
		}
		return len(current.Args) == 1 &&
			isStringType(pkg.TypesInfo.TypeOf(current)) &&
			typedCarriesWorkspaceIdentity(pkg, current.Args[0], identity, workspaceType)
	case *ast.BinaryExpr:
		return current.Op == token.ADD &&
			(typedCarriesWorkspaceIdentity(pkg, current.X, identity, workspaceType) ||
				typedCarriesWorkspaceIdentity(pkg, current.Y, identity, workspaceType))
	case *ast.ParenExpr:
		return typedCarriesWorkspaceIdentity(pkg, current.X, identity, workspaceType)
	default:
		return false
	}
}

func typedContainsWorkspaceIdentity(pkg *packages.Package, expression ast.Expr, identity workspaceIdentityAnalysis, workspaceType *types.Named) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.Ident:
			if object := pkg.TypesInfo.Uses[current]; object != nil && identity.objects[object] {
				found = true
			}
			if object := pkg.TypesInfo.Defs[current]; object != nil && identity.objects[object] {
				found = true
			}
		case *ast.SelectorExpr:
			if object := pkg.TypesInfo.Uses[current.Sel]; object != nil && identity.objects[object] {
				found = true
			}
			selection := pkg.TypesInfo.Selections[current]
			if selection != nil && workspaceType != nil && sameNamedType(selection.Recv(), workspaceType) && selection.Obj().Name() == "ID" {
				found = true
			}
		case *ast.CallExpr:
			if callee := calledFunction(pkg, current); callee != nil && identity.returning[callee] {
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

func typedForbiddenPathCall(pkg *packages.Package, call *ast.CallExpr, identity workspaceIdentityAnalysis, separators map[types.Object]bool) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	function, ok := pkg.TypesInfo.Uses[selector.Sel].(*types.Func)
	if !ok || function.Pkg() == nil {
		return false
	}
	switch function.Pkg().Path() {
	case "path/filepath", "path", "strings":
		if function.Name() != "Join" {
			return false
		}
	case "fmt":
		if function.Name() != "Sprintf" {
			return false
		}
	default:
		return false
	}
	for _, argument := range call.Args {
		if typedContainsWorkspaceIdentity(pkg, argument, identity, workspaceTypeForPackage(pkg)) &&
			(function.Pkg().Path() == "path/filepath" ||
				function.Pkg().Path() == "path" ||
				typedContainsPathSeparator(pkg, call, separators)) {
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
import ("path/filepath"; "strings")
type Workspace struct { ID string }
func automatic(base string, workspace Workspace) string {
	return strings.Join([]string{base, workspace.ID, "leaf"}, string(filepath.Separator))
}`,
		`package fixture
import "fmt"
type Workspace struct { ID string }
func automatic(base string, workspace Workspace) string { return fmt.Sprintf("%s%c%s", base, '/', workspace.ID) }`,
		`package fixture
import "path/filepath"
func caller(base string, workspaceID string) string { return managedRoot(base, workspaceID) }
func managedRoot(base string, id string) string { return filepath.Join(base, id, "leaf") }`,
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
