package runtime

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestEngineStateAccessorsAreNotCalledWhileEngineMutexHeld(t *testing.T) {
	t.Parallel()
	forbidden := map[string]struct{}{
		"compactionPlanningSnapshot": {},
		"lockedContractState":        {},
		"modelRequests":              {},
		"transcriptPersistence":      {},
		"transcriptRuntimeState":     {},
	}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob runtime go files: %v", err)
	}
	var failures []string
	for _, path := range files {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			scanEngineMutexHeldAccessors(fset, fn.Body.List, map[string]bool{}, forbidden, &failures)
		}
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		t.Fatalf("state accessors called while Engine.mu may be held:\n%s", strings.Join(failures, "\n"))
	}
}

func scanEngineMutexHeldAccessors(
	fset *token.FileSet,
	stmts []ast.Stmt,
	locked map[string]bool,
	forbidden map[string]struct{},
	failures *[]string,
) {
	for _, stmt := range stmts {
		if receiver, ok := engineMutexCall(stmt, "Lock"); ok {
			locked[receiver] = true
			continue
		}
		if receiver, ok := engineMutexCall(stmt, "Unlock"); ok {
			locked[receiver] = false
			continue
		}

		ast.Inspect(stmt, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, blocked := forbidden[selector.Sel.Name]; !blocked {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if !ok || !locked[receiver.Name] {
				return true
			}
			*failures = append(*failures, fmt.Sprintf(
				"%s: %s.%s()",
				fset.Position(call.Pos()),
				receiver.Name,
				selector.Sel.Name,
			))
			return true
		})

		switch statement := stmt.(type) {
		case *ast.BlockStmt:
			scanEngineMutexHeldAccessors(fset, statement.List, cloneLockState(locked), forbidden, failures)
		case *ast.IfStmt:
			if statement.Init != nil {
				scanEngineMutexHeldAccessors(fset, []ast.Stmt{statement.Init}, locked, forbidden, failures)
			}
			scanEngineMutexHeldAccessors(fset, statement.Body.List, cloneLockState(locked), forbidden, failures)
			if statement.Else != nil {
				scanEngineMutexHeldAccessors(fset, []ast.Stmt{statement.Else}, cloneLockState(locked), forbidden, failures)
			}
		case *ast.ForStmt:
			scanEngineMutexHeldAccessors(fset, statement.Body.List, cloneLockState(locked), forbidden, failures)
		case *ast.RangeStmt:
			scanEngineMutexHeldAccessors(fset, statement.Body.List, cloneLockState(locked), forbidden, failures)
		case *ast.SwitchStmt:
			for _, clauseStatement := range statement.Body.List {
				if clause, ok := clauseStatement.(*ast.CaseClause); ok {
					scanEngineMutexHeldAccessors(fset, clause.Body, cloneLockState(locked), forbidden, failures)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clauseStatement := range statement.Body.List {
				if clause, ok := clauseStatement.(*ast.CaseClause); ok {
					scanEngineMutexHeldAccessors(fset, clause.Body, cloneLockState(locked), forbidden, failures)
				}
			}
		case *ast.SelectStmt:
			for _, clauseStatement := range statement.Body.List {
				if clause, ok := clauseStatement.(*ast.CommClause); ok {
					scanEngineMutexHeldAccessors(fset, clause.Body, cloneLockState(locked), forbidden, failures)
				}
			}
		}
	}
}

func engineMutexCall(stmt ast.Stmt, method string) (string, bool) {
	expression, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return "", false
	}
	call, ok := expression.X.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != method {
		return "", false
	}
	mutex, ok := selector.X.(*ast.SelectorExpr)
	if !ok || mutex.Sel.Name != "mu" {
		return "", false
	}
	receiver, ok := mutex.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return receiver.Name, true
}

func cloneLockState(input map[string]bool) map[string]bool {
	output := make(map[string]bool, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
