package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCoreStructOnlyExposesCompositionState(t *testing.T) {
	file := parseGoFile(t, filepath.Join("..", "core", "core.go"))
	coreStruct := findStruct(t, file, "Core")

	got := structFieldNames(coreStruct)
	want := []string{"bundles", "closeErr", "closeOnce"}
	assertStringSet(t, "Core fields", got, want)
}

func TestBundlesStructOnlyExposesCohesiveBundleSlots(t *testing.T) {
	file := parseGoFile(t, filepath.Join("..", "core", "bundles.go"))
	bundlesStruct := findStruct(t, file, "Bundles")

	got := structFieldNames(bundlesStruct)
	want := []string{"Auth", "Capability", "Persistence", "Processes", "Projects", "Prompts", "Runtime", "Sessions", "Updates", "Workflows", "Worktrees", "cleanup"}
	assertStringSet(t, "Bundles fields", got, want)
}

func structFieldNames(structType *ast.StructType) []string {
	names := make([]string, 0, len(structType.Fields.List))
	for _, field := range structType.Fields.List {
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	sort.Strings(names)
	return names
}

func assertStringSet(t *testing.T, label string, got []string, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestTransportPackageDoesNotImportCore(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "transport", "*.go"))
	if err != nil {
		t.Fatalf("glob transport files: %v", err)
	}
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file := parseGoFile(t, path)
		for _, importSpec := range file.Imports {
			if importSpec.Path.Value == `"core/server/core"` {
				t.Fatalf("%s imports core/server/core; transport must depend on narrow gateway dependencies", path)
			}
		}
	}
}

func parseGoFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func findStruct(t *testing.T, file *ast.File, name string) *ast.StructType {
	t.Helper()
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != name {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("%s is not a struct", name)
			}
			return structType
		}
	}
	t.Fatalf("struct %s not found", name)
	return nil
}
