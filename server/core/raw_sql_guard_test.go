package core_test

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestProductionGoUsesGeneratedDatabaseQuerySeams(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkgs := loadStructuredGuardPackages(t, repoRoot, false, "./server/...", "./cli/...", "./shared/...")
	violations := make([]string, 0)
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) {
			continue
		}
		if !generatedDatabaseQueryPackage[pkg.PkgPath] {
			violations = append(violations, embeddedSQLViolations(pkg)...)
			violations = append(violations, directDatabaseQueryViolations(pkg, repoRoot)...)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("production database query boundary violations:\n%s", joinStructuredGuardLines(violations))
	}
}

var generatedDatabaseQueryPackage = map[string]bool{
	"core/server/metadata/sqlitegen":          true,
	"core/server/metadata/sqlitelifecyclegen": true,
}

var directDatabaseQueryMethod = map[string]bool{
	"Query":           true,
	"QueryContext":    true,
	"QueryRow":        true,
	"QueryRowContext": true,
	"Exec":            true,
	"ExecContext":     true,
	"Prepare":         true,
	"PrepareContext":  true,
}

func embeddedSQLViolations(pkg *packages.Package) []string {
	if len(pkg.EmbedPatterns) == 0 {
		return nil
	}
	violations := make([]string, 0)
	for _, pattern := range pkg.EmbedPatterns {
		if filepath.Ext(pattern) != ".sql" {
			continue
		}
		if pkg.PkgPath == "core/server/metadata" && pattern == "migrations/*.up.sql" {
			continue
		}
		violations = append(violations, pkg.PkgPath+": production SQL embeds must be metadata migrations declared through the generated-query boundary")
	}
	return violations
}

func directDatabaseQueryViolations(pkg *packages.Package, repoRoot string) []string {
	violations := make([]string, 0)
	for _, file := range pkg.Syntax {
		filename := pkg.Fset.Position(file.Pos()).Filename
		relPath := structuredGuardRelativePath(repoRoot, filename)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selection := pkg.TypesInfo.Selections[selector]
			if !isDirectDatabaseQuerySelection(selection) {
				return true
			}
			position := pkg.Fset.Position(selector.Sel.Pos())
			violations = append(violations, relPath+":"+position.String()+": direct database/sql "+selection.Obj().Name()+" call bypasses generated query seams")
			return true
		})
	}
	return violations
}

func isDirectDatabaseQuerySelection(selection *types.Selection) bool {
	if selection == nil {
		return false
	}
	method, ok := selection.Obj().(*types.Func)
	if !ok || method.Pkg() == nil || method.Pkg().Path() != "database/sql" {
		return false
	}
	return directDatabaseQueryMethod[method.Name()]
}
