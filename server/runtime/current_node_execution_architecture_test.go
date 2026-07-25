package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowRuntimeControlContractsDoNotExposeWorkflowRun(t *testing.T) {
	repoRoot := runtimePackageRepoRoot(t)
	paths := []string{
		"server/runtime",
		"server/runtimewire",
		"server/sessionruntime",
		"server/workflowrunner",
		"server/workflowruntime",
		"server/runtimecommand",
		"server/tools",
	}
	for _, relative := range paths {
		root := filepath.Join(repoRoot, relative)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || filepath.Base(path) == "migration_legacy.go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				switch ident.Name {
				case "WorkflowRun", "WorkflowRunConfigured", "RequiresWorkflowRun", "workflowRunActive":
					t.Fatalf("workflow runtime control identifier %q remains in %s", ident.Name, path)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", relative, err)
		}
	}
}
