package core_test

import (
	"go/ast"
	"testing"
)

func TestCLIHasNoLegacyClientPromptFilesystemAuthority(t *testing.T) {
	forbiddenNames := map[string]struct{}{
		"ClientPromptRoots":                       {},
		"NewClientPromptRoots":                    {},
		"NewDefaultRegistryWithClientPromptRoots": {},
		"NewDefaultRegistryWithFilePrompts":       {},
		"loadFilePromptCommands":                  {},
		"loadPromptCommands":                      {},
		"normalizeFilePromptCommandID":            {},
	}
	walkRepositoryGoSources(t, findRepoRoot(t), repositoryGoSourceScan{
		Operation:    "scan CLI prompt ownership",
		Root:         "cli/app",
		Recursive:    true,
		IncludeTests: false,
		Selection:    allRepositoryGoSources{},
	}, func(source parsedGoSource) {
		for _, declaration := range source.File.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if _, forbidden := forbiddenNames[typed.Name.Name]; forbidden {
					t.Errorf("%s declares obsolete symbol %s", source.RelPath, typed.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range typed.Specs {
					if typeSpec, ok := spec.(*ast.TypeSpec); ok {
						if _, forbidden := forbiddenNames[typeSpec.Name.Name]; forbidden {
							t.Errorf("%s declares obsolete symbol %s", source.RelPath, typeSpec.Name.Name)
						}
					}
				}
			}
		}
	})
}
