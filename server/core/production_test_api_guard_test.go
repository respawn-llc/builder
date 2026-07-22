package core_test

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

func TestProductionGoFilesDoNotExposeTestOnlyAPIs(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkgs := testharness.LoadTypedPackages(t, repoRoot, true, "./server/...", "./cli/...", "./shared/...")
	assertCoreRepositoryModule(t, pkgs)
	declarations := productionAPIDeclarations(pkgs)
	productionReferences, testReferences := productionAPIReferences(pkgs)
	violations := make([]string, 0)
	for key, declaration := range declarations {
		if !testReferences[key] || productionReferences[key] {
			continue
		}
		violations = append(violations, declaration.position+": production API "+declaration.object.Name()+" is referenced only by tests")
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("test-only production API violations:\n%s", strings.Join(violations, "\n"))
	}
}

type productionAPIObjectKey struct {
	packagePath string
	filename    string
	line        int
	column      int
}

type productionAPIDeclaration struct {
	object   types.Object
	position string
}

func productionAPIDeclarations(pkgs []*packages.Package) map[productionAPIObjectKey]productionAPIDeclaration {
	declarations := make(map[productionAPIObjectKey]productionAPIDeclaration)
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				switch declaration := node.(type) {
				case *ast.FuncDecl:
					recordProductionAPIDeclaration(declarations, pkg, pkg.TypesInfo.Defs[declaration.Name])
					return false
				case *ast.TypeSpec:
					recordProductionAPIDeclaration(declarations, pkg, pkg.TypesInfo.Defs[declaration.Name])
					return false
				case *ast.ValueSpec:
					for _, name := range declaration.Names {
						recordProductionAPIDeclaration(declarations, pkg, pkg.TypesInfo.Defs[name])
					}
					return false
				default:
					return true
				}
			})
		}
	}
	return declarations
}

func recordProductionAPIDeclaration(declarations map[productionAPIObjectKey]productionAPIDeclaration, pkg *packages.Package, object types.Object) {
	if object == nil || !object.Exported() {
		return
	}
	key, ok := productionAPIKey(pkg, object)
	if !ok {
		return
	}
	declarations[key] = productionAPIDeclaration{
		object:   object,
		position: testharness.SourcePosition(pkg, object.Pos()).String(),
	}
}

func productionAPIReferences(pkgs []*packages.Package) (map[productionAPIObjectKey]bool, map[productionAPIObjectKey]bool) {
	productionReferences := make(map[productionAPIObjectKey]bool)
	testReferences := make(map[productionAPIObjectKey]bool)
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil || !isRepositoryPackage(pkg) {
			continue
		}
		for _, object := range pkg.TypesInfo.Uses {
			if object == nil || !object.Exported() {
				continue
			}
			key, ok := productionAPIKey(pkg, object)
			if !ok {
				continue
			}
			if pkg.ForTest == "" {
				productionReferences[key] = true
			} else {
				testReferences[key] = true
			}
		}
	}
	return productionReferences, testReferences
}

func productionAPIKey(pkg *packages.Package, object types.Object) (productionAPIObjectKey, bool) {
	if object.Pkg() == nil || object.Pos().IsValid() == false {
		return productionAPIObjectKey{}, false
	}
	position := pkg.Fset.Position(object.Pos())
	if position.Filename == "" {
		return productionAPIObjectKey{}, false
	}
	return productionAPIObjectKey{
		packagePath: object.Pkg().Path(),
		filename:    position.Filename,
		line:        position.Line,
		column:      position.Column,
	}, true
}

func isProductionRepositoryPackage(pkg *packages.Package) bool {
	return pkg.ForTest == "" && isRepositoryPackage(pkg)
}

func isRepositoryPackage(pkg *packages.Package) bool {
	return pkg.Module != nil && pkg.Module.Path == "core"
}

func assertCoreRepositoryModule(t testing.TB, pkgs []*packages.Package) {
	t.Helper()
	pkg := testharness.PackageByPath(t, pkgs, "core/server/core")
	if pkg.Module == nil || pkg.Module.Path != "core" {
		t.Fatalf("typed core package module = %#v, want module path core", pkg.Module)
	}
}
