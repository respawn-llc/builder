package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestExclusiveStepCallSitesDeclareActiveKind(t *testing.T) {
	repoRoot := runtimePackageRepoRoot(t)
	type activeKindPolicy struct {
		activeKind      string
		spinnerPolicy   string
		statusPolicy    string
		interruptPolicy string
		goalSuspension  string
		goalAutoResume  string
	}
	expected := map[string]activeKindPolicy{
		"server/runtime/background.go:runQueuedNotices": {
			activeKind: "ActiveKindBackground", spinnerPolicy: "background", statusPolicy: "background", interruptPolicy: "interruptible-if-step-cancelable", goalSuspension: "never", goalAutoResume: "never",
		},
		"server/runtime/compaction.go:compactContext": {
			activeKind: "call-site-specific-compaction-kind", spinnerPolicy: "compaction", statusPolicy: "compaction", interruptPolicy: "interruptible-if-step-cancelable", goalSuspension: "never", goalAutoResume: "never",
		},
		"server/runtime/engine.go:submitUserMessage": {
			activeKind: "ActiveKindUserTurn", spinnerPolicy: "model-turn", statusPolicy: "user-turn", interruptPolicy: "interruptible", goalSuspension: "never", goalAutoResume: "after-success-only",
		},
		"server/runtime/engine.go:SubmitWorkflowTurn": {
			activeKind: "ActiveKindWorkflowTurn", spinnerPolicy: "workflow-turn", statusPolicy: "workflow-turn", interruptPolicy: "interruptible", goalSuspension: "never", goalAutoResume: "never",
		},
		"server/runtime/engine.go:submitUserShellCommand": {
			activeKind: "ActiveKindUserShell", spinnerPolicy: "user-shell", statusPolicy: "user-shell", interruptPolicy: "interruptible", goalSuspension: "never", goalAutoResume: "never",
		},
		"server/runtime/engine_queue_submission.go:RunWhenIdle": {
			activeKind: "caller-provided", spinnerPolicy: "caller-provided", statusPolicy: "caller-provided", interruptPolicy: "caller-provided", goalSuspension: "caller-provided", goalAutoResume: "caller-provided",
		},
		"server/runtime/engine_queue_submission.go:submitQueuedUserMessages": {
			activeKind: "ActiveKindUserTurn", spinnerPolicy: "model-turn", statusPolicy: "user-turn", interruptPolicy: "interruptible", goalSuspension: "never", goalAutoResume: "after-success-only",
		},
		"server/runtime/goal.go:runGoalTurn": {
			activeKind: "ActiveKindGoalLoop", spinnerPolicy: "goal-loop", statusPolicy: "goal-loop", interruptPolicy: "interruptible", goalSuspension: "only-this-kind", goalAutoResume: "staged-after-idle-publication",
		},
	}
	found := map[string]string{}
	violations := make([]string, 0)
	for _, relPath := range []string{
		"server/runtime/background.go",
		"server/runtime/compaction.go",
		"server/runtime/engine.go",
		"server/runtime/engine_queue_submission.go",
		"server/runtime/goal.go",
	} {
		path := filepath.Join(repoRoot, relPath)
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relPath, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}
			ast.Inspect(fn.Body, func(child ast.Node) bool {
				literal, ok := child.(*ast.CompositeLit)
				if !ok || !isExclusiveStepOptionsLiteral(literal) {
					return true
				}
				key := relPath + ":" + fn.Name.Name
				kind, hasKind := activeKindFromLiteral(literal)
				if !hasKind {
					position := fileSet.Position(literal.Pos())
					violations = append(violations, position.String()+": exclusiveStepOptions must declare ActiveKind")
					return true
				}
				found[key] = kind
				return true
			})
			return false
		})
	}
	for key, want := range expected {
		got, ok := found[key]
		if !ok {
			violations = append(violations, key+" missing from exclusive step active-kind call-site table")
			continue
		}
		if want.activeKind != "caller-provided" && want.activeKind != "call-site-specific-compaction-kind" && got != want.activeKind {
			violations = append(violations, key+" active kind = "+got+", want "+want.activeKind)
		}
		if want.spinnerPolicy == "" || want.statusPolicy == "" || want.interruptPolicy == "" || want.goalSuspension == "" || want.goalAutoResume == "" {
			violations = append(violations, key+" must document spinner/status/interrupt/goal policy")
		}
	}
	for key := range found {
		if _, ok := expected[key]; !ok {
			violations = append(violations, key+" is a new exclusive step call site without spinner/interrupt/goal policy classification")
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("exclusive step active-kind guard violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestRunWhenIdleHelpersRequireExplicitActiveKind(t *testing.T) {
	file := parseRuntimeSource(t, "server/runtime/engine_queue_submission.go")
	for _, name := range []string{"RunWhenIdle", "RunWhenIdleBeforeQueuedUserWork"} {
		fn := findRuntimeFunc(t, file, name)
		if !functionHasActiveKindParameter(fn) {
			t.Fatalf("%s must require an explicit ActiveKind parameter", name)
		}
	}
}

func isExclusiveStepOptionsLiteral(literal *ast.CompositeLit) bool {
	ident, ok := literal.Type.(*ast.Ident)
	return ok && ident.Name == "exclusiveStepOptions"
}

func activeKindFromLiteral(literal *ast.CompositeLit) (string, bool) {
	for _, element := range literal.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "ActiveKind" {
			continue
		}
		switch value := kv.Value.(type) {
		case *ast.Ident:
			return value.Name, true
		default:
			return "", true
		}
	}
	return "", false
}

func functionHasActiveKindParameter(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Type.Params == nil {
		return false
	}
	for _, param := range fn.Type.Params.List {
		ident, ok := param.Type.(*ast.Ident)
		if ok && ident.Name == "ActiveKind" {
			return true
		}
	}
	return false
}

func parseRuntimeSource(t *testing.T, relPath string) *ast.File {
	t.Helper()
	path := filepath.Join(runtimePackageRepoRoot(t), relPath)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", relPath, err)
	}
	return file
}

func findRuntimeFunc(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

func runtimePackageRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}
