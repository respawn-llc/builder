package workflowrunner

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestWorkflowRunnerDoesNotReadFullSessionEventLog(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob workflowrunner files: %v", err)
	}
	for _, file := range files {
		if filepath.Base(file) == "architecture_test.go" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "ReadEvents", "WalkRecords", "SnapshotFromDir":
				t.Fatalf("workflowrunner must not scan session event logs; found %s in %s", selector.Sel.Name, file)
			}
			return true
		})
	}
}
