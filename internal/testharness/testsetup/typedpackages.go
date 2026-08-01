package testsetup

import (
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func LoadTypedPackages(t testing.TB, dir string, tests bool, patterns ...string) []*packages.Package {
	return loadTypedPackages(t, dir, tests, nil, patterns...)
}

func LoadTypedPackagesForPlatform(t testing.TB, dir string, tests bool, goos string, goarch string, patterns ...string) []*packages.Package {
	env := append([]string(nil), os.Environ()...)
	env = append(env, "GOOS="+goos, "GOARCH="+goarch)
	return loadTypedPackages(t, dir, tests, env, patterns...)
}

func loadTypedPackages(t testing.TB, dir string, tests bool, env []string, patterns ...string) []*packages.Package {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{
		Dir: dir,
		Env: env,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedModule |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo |
			packages.NeedEmbedPatterns,
		Tests: tests,
	}, patterns...)
	if err != nil {
		t.Fatalf("load typed packages: %v", err)
	}
	if errors := typedPackageErrors(pkgs); len(errors) > 0 {
		t.Fatalf("typed packages must type-check before scanning:\n%s", strings.Join(errors, "\n"))
	}
	return pkgs
}

func PackageByPath(t testing.TB, pkgs []*packages.Package, packagePath string) *packages.Package {
	t.Helper()
	for _, pkg := range pkgs {
		if pkg.PkgPath == packagePath && pkg.ForTest == "" {
			return pkg
		}
	}
	t.Fatalf("typed package %s was not loaded", packagePath)
	return nil
}

func RepositoryRelativePath(repoRoot, filename string) (string, bool) {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(filename) == "" {
		return "", false
	}
	relativePath, err := filepath.Rel(repoRoot, filename)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, "../") {
		return "", false
	}
	return filepath.ToSlash(relativePath), true
}

func SourcePosition(pkg *packages.Package, position token.Pos) token.Position {
	if pkg == nil || pkg.Fset == nil {
		return token.Position{}
	}
	return pkg.Fset.Position(position)
}

func WriteFile(t testing.TB, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func typedPackageErrors(pkgs []*packages.Package) []string {
	var errors []string
	for _, pkg := range pkgs {
		for _, packageErr := range pkg.Errors {
			errors = append(errors, packageErr.Error())
		}
	}
	sort.Strings(errors)
	return errors
}
