package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIHasNoLegacyClientPromptFilesystemAuthority(t *testing.T) {
	root := filepath.Join(findRepoRoot(t), "cli", "app")
	forbiddenNames := map[string]struct{}{
		"ClientPromptRoots":                       {},
		"NewClientPromptRoots":                    {},
		"NewDefaultRegistryWithClientPromptRoots": {},
		"NewDefaultRegistryWithFilePrompts":       {},
		"loadFilePromptCommands":                  {},
		"loadPromptCommands":                      {},
		"normalizeFilePromptCommandID":            {},
	}
	err := filepath.WalkDir(root, func(path string, entryDir fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entryDir.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, declaration := range file.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if _, forbidden := forbiddenNames[typed.Name.Name]; forbidden {
					t.Errorf("%s declares obsolete symbol %s", path, typed.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range typed.Specs {
					if typeSpec, ok := spec.(*ast.TypeSpec); ok {
						if _, forbidden := forbiddenNames[typeSpec.Name.Name]; forbidden {
							t.Errorf("%s declares obsolete symbol %s", path, typeSpec.Name.Name)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
