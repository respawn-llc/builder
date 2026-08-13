package workflowsvc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestWorkflowTrustedReadPathsDoNotRevalidate(t *testing.T) {
	assertWorkflowReadValidationCalls(t, workflowReadValidationExpectation{
		methodsByFile: map[string]map[string]bool{
			"labels.go": {
				"ListWorkflowProjectLabels": true,
				"GetWorkflowTaskLabels":     true,
			},
			"service.go": {
				"ListWorkflowAttention":      true,
				"ListWorkflowTaskAttention":  true,
				"ListWorkflowTaskComments":   true,
				"ListWorkflowTaskActivity":   true,
				"ListWorkflowTaskSessions":   true,
				"ListWorkflowTasks":          true,
				"SearchWorkflowTasks":        true,
				"GetWorkflowBoard":           true,
				"ListWorkflowBoardNodeCards": true,
				"GetWorkflowTask":            true,
			},
		},
		withValidatedCalls: 1,
	})
	assertWorkflowReadValidationCalls(t, workflowReadValidationExpectation{
		methodsByFile: map[string]map[string]bool{
			filepath.Join("..", "workflowview", "activity.go"): {
				"ReadActivity": true,
			},
			filepath.Join("..", "workflowview", "attention.go"): {
				"ReadAttention": true,
				"ListTaskByID":  true,
			},
			filepath.Join("..", "workflowview", "board.go"): {
				"ReadBoard":     true,
				"ReadNodeCards": true,
			},
			filepath.Join("..", "workflowview", "task_list.go"): {
				"ReadTasks": true,
			},
			filepath.Join("..", "workflowview", "task_search.go"): {
				"ReadSearch": true,
			},
			filepath.Join("..", "workflowview", "task_sessions.go"): {
				"ReadSessions": true,
			},
			"labels.go": {
				"ListWorkflowProjectLabelsValidated": true,
				"GetWorkflowTaskLabelsValidated":     true,
			},
			"service.go": {
				"ListWorkflowAttentionValidated":      true,
				"ListWorkflowTaskAttentionValidated":  true,
				"ListWorkflowTaskCommentsValidated":   true,
				"ListWorkflowTaskActivityValidated":   true,
				"ListWorkflowTaskSessionsValidated":   true,
				"ListWorkflowTasksValidated":          true,
				"SearchWorkflowTasksValidated":        true,
				"GetWorkflowBoardValidated":           true,
				"ListWorkflowBoardNodeCardsValidated": true,
				"GetWorkflowTaskValidated":            true,
			},
		},
		withValidatedCalls: 0,
		forbiddenCalls: map[string]bool{
			"ResolveWorkflowOffsetWindow": true,
		},
	})
}

type workflowReadValidationExpectation struct {
	methodsByFile      map[string]map[string]bool
	withValidatedCalls int
	forbiddenCalls     map[string]bool
}

func assertWorkflowReadValidationCalls(t *testing.T, expectation workflowReadValidationExpectation) {
	t.Helper()
	for fileName, methods := range expectation.methodsByFile {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", fileName), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", fileName, err)
		}
		found := make(map[string]bool, len(methods))
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || !methods[function.Name.Name] {
				continue
			}
			found[function.Name.Name] = true
			withValidatedCalls := 0
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calledName(call.Fun)
				switch name {
				case "WithValidated":
					withValidatedCalls++
				case "Validate", "ValidateRPC":
					t.Errorf("%s.%s directly invokes %s", fileName, function.Name.Name, name)
				default:
					if expectation.forbiddenCalls[name] {
						t.Errorf("%s.%s re-enters validation through %s", fileName, function.Name.Name, name)
					}
				}
				return true
			})
			if withValidatedCalls != expectation.withValidatedCalls {
				t.Errorf(
					"%s.%s WithValidated calls = %d, want %d",
					fileName,
					function.Name.Name,
					withValidatedCalls,
					expectation.withValidatedCalls,
				)
			}
		}
		for method := range methods {
			if !found[method] {
				t.Errorf("%s.%s was not found", fileName, method)
			}
		}
	}
}

func calledName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	default:
		return ""
	}
}
