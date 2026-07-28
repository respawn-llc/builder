package core

import (
	"go/ast"
	"path/filepath"
	"sort"
	"testing"
)

func TestWorkflowReadModelsShareOneCanonicalStatusSnapshotCoordinator(t *testing.T) {
	file := parseGoFile(t, filepath.Join("..", "core", "composition.go"))
	want := map[string]bool{
		"NewBoard":      false,
		"NewTaskDetail": false,
		"NewTaskList":   false,
		"NewTaskSearch": false,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		owner, ok := selector.X.(*ast.Ident)
		if !ok || owner.Name != "workflowview" {
			return true
		}
		if _, tracked := want[selector.Sel.Name]; !tracked {
			return true
		}
		if len(call.Args) == 0 {
			t.Fatalf("workflowview.%s must receive the canonical task-status snapshot coordinator", selector.Sel.Name)
		}
		coordinator, ok := call.Args[len(call.Args)-1].(*ast.Ident)
		if !ok || coordinator.Name != "workflowStatusSnapshots" {
			t.Fatalf("workflowview.%s status snapshot argument = %#v, want workflowStatusSnapshots", selector.Sel.Name, call.Args[len(call.Args)-1])
		}
		want[selector.Sel.Name] = true
		return true
	})
	missing := make([]string, 0)
	for constructor, found := range want {
		if !found {
			missing = append(missing, constructor)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("workflow read models must share workflowStatusSnapshots; missing %v", missing)
	}
}
