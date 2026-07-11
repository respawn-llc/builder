package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionGoFilesDoNotExposeTestNamedAPIs(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	searchRoots := []string{"server", "cli", "shared", "prompts"}
	for _, root := range searchRoots {
		rootPath := filepath.Join(repoRoot, root)
		err := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" || entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				checkDeclForTestNamedAPI(t, path, decl)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", rootPath, err)
		}
	}
}

func checkDeclForTestNamedAPI(t *testing.T, path string, decl ast.Decl) {
	t.Helper()
	switch typed := decl.(type) {
	case *ast.FuncDecl:
		if productionAPITestName(typed.Name.Name) {
			t.Fatalf("%s declares test-named production function %s", path, typed.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range typed.Specs {
			switch typedSpec := spec.(type) {
			case *ast.TypeSpec:
				if productionAPITestName(typedSpec.Name.Name) {
					t.Fatalf("%s declares test-named production type %s", path, typedSpec.Name.Name)
				}
			case *ast.ValueSpec:
				for _, name := range typedSpec.Names {
					if productionAPITestName(name.Name) {
						t.Fatalf("%s declares test-named production value %s", path, name.Name)
					}
				}
			}
		}
	}
}

func productionAPITestName(name string) bool {
	return strings.Contains(name, "ForTest") ||
		strings.HasPrefix(name, "ReserveTest") ||
		strings.HasPrefix(name, "ReleaseTest") ||
		(strings.HasPrefix(name, "Set") && strings.HasSuffix(name, "ForTest"))
}
