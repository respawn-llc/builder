package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

func TestWorkflowExecutionHardCutsRemainEnforced(t *testing.T) {
	forbidden := map[string]struct{}{
		"ClientRequestID":                     {},
		"CompleteIdleCurrentNode":             {},
		"RunWhenIdle":                         {},
		"RunWhenIdleBeforeQueuedUserWork":     {},
		"RunWorktreeTransition":               {},
		"RuntimeClientRequestID":              {},
		"RuntimeStepOrigin":                   {},
		"ScheduleIfIdle":                      {},
		"StartCurrentNode":                    {},
		"WorkflowExecutionLease":              {},
		"currentNodeAdmissionGate":            {},
		"rollbackRetargetedSessions":          {},
		"rollbackSessionTargetWithSync":       {},
		"worktreeSessionRetargetCompensation": {},
	}
	var violations []string
	if err := walkProductionGoFiles(findRepoRoot(t), func(path, relative string) error {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				if _, blocked := forbidden[value.Name]; blocked {
					violations = append(violations, relative+": "+value.Name)
				}
			case *ast.ImportSpec:
				if strings.Trim(value.Path.Value, `"`) == "core/server/requestmemo" {
					violations = append(violations, relative+": requestmemo")
				}
			}
			return true
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("obsolete Workflow execution mechanisms remain:\n%s", strings.Join(violations, "\n"))
	}
}
