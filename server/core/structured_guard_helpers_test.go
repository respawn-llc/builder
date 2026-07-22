package core_test

import (
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func loadStructuredGuardPackages(t *testing.T, repoRoot string, tests bool, patterns ...string) []*packages.Package {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{
		Dir: repoRoot,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo |
			packages.NeedEmbedPatterns,
		Tests: tests,
	}, patterns...)
	if err != nil {
		t.Fatalf("load structured guard packages: %v", err)
	}
	if errors := structuredGuardPackageErrors(pkgs); len(errors) > 0 {
		t.Fatalf("structured guard packages must type-check before scanning:\n%s", strings.Join(errors, "\n"))
	}
	return pkgs
}

func structuredGuardPackageErrors(pkgs []*packages.Package) []string {
	var errors []string
	for _, pkg := range pkgs {
		for _, packageErr := range pkg.Errors {
			errors = append(errors, packageErr.Error())
		}
	}
	sort.Strings(errors)
	return errors
}

func structuredGuardPackageByPath(t *testing.T, pkgs []*packages.Package, packagePath string) *packages.Package {
	t.Helper()
	for _, pkg := range pkgs {
		if pkg.PkgPath == packagePath && pkg.ForTest == "" {
			return pkg
		}
	}
	t.Fatalf("structured guard package %s was not loaded", packagePath)
	return nil
}

func structuredGuardRelativePath(repoRoot string, filename string) string {
	relativePath, err := filepath.Rel(repoRoot, filename)
	if err != nil {
		return filename
	}
	return filepath.ToSlash(relativePath)
}

func structuredGuardPosition(pkg *packages.Package, position token.Pos) string {
	if pkg.Fset == nil {
		return ""
	}
	return pkg.Fset.Position(position).String()
}

func joinStructuredGuardLines(lines []string) string {
	return strings.Join(lines, "\n")
}
