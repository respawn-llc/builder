package scrollback

// UNDER NO CIRCUMSTANCE MAY AN AGENT RELAX, REMOVE, DISABLE OR SKIP ANY TESTS IN THIS FILE. Change results in immediate shutdown.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	nativeOngoingSurfaceName        = "NativeOngoingSurface"
	nativeOngoingSurfacePackagePath = "core/cli/tui/scrollback"
	ongoingScrollbackBufferImplName = "OngoingScrollbackBufferImpl"
)

var nativeOngoingSurfaceMethods = map[string]struct{}{
	"Steer":                          {},
	"StreamMarkdownAssistantContent": {},
	"FinishAssistantStreaming":       {},
	"DiscardAssistantStreaming":      {},
	"RenderLive":                     {},
	"AssistantStreaming":             {},
	"AssistantStreamTailLines":       {},
	"FlushHoldoff":                   {},
}

func TestNativeOngoingSurfaceInterfaceShape(t *testing.T) {
	repoRoot := repositoryRoot(t)
	methodNames := nativeOngoingSurfaceMethodNames(t, repoRoot)

	assertExactMethodNameSet(t, methodNames)
}

func TestOngoingScrollbackBufferImplExportsOnlyNativeOngoingSurfaceMethods(t *testing.T) {
	implType := reflect.TypeOf(&OngoingScrollbackBufferImpl{})
	methodNames := make([]string, 0, implType.NumMethod())
	for index := 0; index < implType.NumMethod(); index++ {
		methodNames = append(methodNames, implType.Method(index).Name)
	}

	assertExactMethodNameSet(t, methodNames)
}

func TestExactlyOneNativeOngoingSurfaceImplementation(t *testing.T) {
	repoRoot := repositoryRoot(t)
	pkgs := loadRepositoryPackages(t, repoRoot)
	iface := nativeOngoingSurfaceInterface(t, pkgs)
	implementations := nativeOngoingSurfaceImplementations(pkgs, iface, repoRoot)

	if len(implementations) != 1 {
		t.Fatalf("expected exactly one %s implementation, got %d: %s", nativeOngoingSurfaceName, len(implementations), strings.Join(implementations, ", "))
	}
	if implementations[0] != ongoingScrollbackBufferImplName {
		t.Fatalf("expected the only %s implementation to be %s, got %s", nativeOngoingSurfaceName, ongoingScrollbackBufferImplName, implementations[0])
	}
}

func nativeOngoingSurfaceMethodNames(t *testing.T, repoRoot string) []string {
	t.Helper()

	fileSet := token.NewFileSet()
	scrollbackDir := filepath.Join(repoRoot, "cli", "tui", "scrollback")
	pattern := filepath.Join(scrollbackDir, "*.go")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsedFile, err := parser.ParseFile(fileSet, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}

		for _, decl := range parsedFile.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != nativeOngoingSurfaceName {
					continue
				}

				interfaceType, ok := typeSpec.Type.(*ast.InterfaceType)
				if !ok {
					t.Fatalf("%s must be an interface", nativeOngoingSurfaceName)
				}

				methodNames := make([]string, 0, len(interfaceType.Methods.List))
				for _, field := range interfaceType.Methods.List {
					if len(field.Names) == 0 {
						t.Fatalf("%s must declare methods directly, got an embedded interface/type", nativeOngoingSurfaceName)
					}
					for _, name := range field.Names {
						methodNames = append(methodNames, name.Name)
					}
				}
				return methodNames
			}
		}
	}

	t.Fatalf("could not find %s in %s", nativeOngoingSurfaceName, scrollbackDir)
	return nil
}

func assertExactMethodNameSet(t *testing.T, methodNames []string) {
	t.Helper()

	if len(methodNames) != len(nativeOngoingSurfaceMethods) {
		t.Fatalf("expected %s to have exactly %d methods, got %d: %s", nativeOngoingSurfaceName, len(nativeOngoingSurfaceMethods), len(methodNames), strings.Join(methodNames, ", "))
	}

	seen := make(map[string]struct{}, len(methodNames))
	for _, methodName := range methodNames {
		if _, ok := nativeOngoingSurfaceMethods[methodName]; !ok {
			t.Fatalf("unexpected %s method %q; expected exactly: %s", nativeOngoingSurfaceName, methodName, expectedMethodNames())
		}
		if _, ok := seen[methodName]; ok {
			t.Fatalf("duplicate %s method %q", nativeOngoingSurfaceName, methodName)
		}
		seen[methodName] = struct{}{}
	}

	for expectedMethodName := range nativeOngoingSurfaceMethods {
		if _, ok := seen[expectedMethodName]; !ok {
			t.Fatalf("missing %s method %q; expected exactly: %s", nativeOngoingSurfaceName, expectedMethodName, expectedMethodNames())
		}
	}
}

func expectedMethodNames() string {
	methodNames := make([]string, 0, len(nativeOngoingSurfaceMethods))
	for methodName := range nativeOngoingSurfaceMethods {
		methodNames = append(methodNames, methodName)
	}
	sort.Strings(methodNames)
	return strings.Join(methodNames, ", ")
}

func loadRepositoryPackages(t *testing.T, repoRoot string) []*packages.Package {
	t.Helper()

	pkgs, err := packages.Load(&packages.Config{
		Dir:   repoRoot,
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: true,
	}, "./...")
	if err != nil {
		t.Fatalf("load repository packages: %v", err)
	}
	if errors := packageErrors(pkgs); len(errors) > 0 {
		t.Fatalf("repository packages must type-check before scanning %s implementations:\n%s", nativeOngoingSurfaceName, strings.Join(errors, "\n"))
	}

	return pkgs
}

func packageErrors(pkgs []*packages.Package) []string {
	var errors []string
	for _, pkg := range pkgs {
		for _, err := range pkg.Errors {
			errors = append(errors, err.Error())
		}
	}
	sort.Strings(errors)
	return errors
}

func nativeOngoingSurfaceInterface(t *testing.T, pkgs []*packages.Package) *types.Interface {
	t.Helper()

	for _, pkg := range pkgs {
		if normalizedPackagePath(pkg.PkgPath) != nativeOngoingSurfacePackagePath || pkg.Types == nil {
			continue
		}

		obj := pkg.Types.Scope().Lookup(nativeOngoingSurfaceName)
		typeName, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			continue
		}
		iface, ok := named.Underlying().(*types.Interface)
		if !ok {
			continue
		}
		return iface.Complete()
	}

	t.Fatalf("could not find %s.%s", nativeOngoingSurfacePackagePath, nativeOngoingSurfaceName)
	return nil
}

func nativeOngoingSurfaceImplementations(pkgs []*packages.Package, iface *types.Interface, repoRoot string) []string {
	implementationsBySource := map[string]string{}

	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}

		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || obj.IsAlias() {
				continue
			}

			named, ok := obj.Type().(*types.Named)
			if !ok {
				continue
			}
			if _, ok := named.Underlying().(*types.Interface); ok {
				continue
			}
			if !namedTypeImplementsInterface(named, iface) {
				continue
			}

			sourceKey := typeSourceKey(pkg, obj, repoRoot)
			implementationsBySource[sourceKey] = obj.Name()
		}
	}

	implementations := make([]string, 0, len(implementationsBySource))
	for _, implementation := range implementationsBySource {
		implementations = append(implementations, implementation)
	}
	sort.Strings(implementations)
	return implementations
}

func namedTypeImplementsInterface(named *types.Named, iface *types.Interface) bool {
	return types.Implements(named, iface) || types.Implements(types.NewPointer(named), iface)
}

func typeSourceKey(pkg *packages.Package, obj *types.TypeName, repoRoot string) string {
	if pkg.Fset == nil {
		if obj.Pkg() == nil {
			return obj.Name()
		}
		return obj.Pkg().Path() + "." + obj.Name()
	}

	position := pkg.Fset.Position(obj.Pos())
	if position.Filename == "" {
		if obj.Pkg() == nil {
			return obj.Name()
		}
		return obj.Pkg().Path() + "." + obj.Name()
	}

	relPath, err := filepath.Rel(repoRoot, position.Filename)
	if err != nil {
		relPath = position.Filename
	}
	return fmt.Sprintf("%s:%d:%d:%s", filepath.ToSlash(relPath), position.Line, position.Column, obj.Name())
}

func normalizedPackagePath(pkgPath string) string {
	if index := strings.Index(pkgPath, " ["); index >= 0 {
		return pkgPath[:index]
	}
	return pkgPath
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return dir
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", goModPath, err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repository root from %s", dir)
		}
		dir = parent
	}
}
