package app

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeUICtrlCCallSitesRouteThroughCommonHelper(t *testing.T) {
	repoRoot := mainSurfaceGuardRepositoryRoot(t)
	violations := map[string]struct{}{}
	for _, pkg := range loadMainSurfaceGuardPackages(t, repoRoot) {
		for _, file := range pkg.Syntax {
			position := pkg.Fset.Position(file.Pos())
			relPath, ok := mainSurfaceGuardRelativePath(repoRoot, position.Filename)
			if !ok || !isRuntimeCtrlCGuardFile(relPath) {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || !functionHandlesRuntimeCtrlC(fn, pkg.TypesInfo) {
					continue
				}
				if functionCallsRuntimeCtrlCHelper(fn, pkg.TypesInfo) {
					continue
				}
				position := pkg.Fset.Position(fn.Pos())
				violations[fmt.Sprintf("%s:%d:%d handles Ctrl+C without handleRuntimeCtrlC", relPath, position.Line, position.Column)] = struct{}{}
			}
		}
	}
	if len(violations) == 0 {
		return
	}
	lines := make([]string, 0, len(violations))
	for violation := range violations {
		lines = append(lines, violation)
	}
	sort.Strings(lines)
	t.Fatalf("runtime UI Ctrl+C handlers must route through handleRuntimeCtrlC:\n%s", strings.Join(lines, "\n"))
}

func isRuntimeCtrlCGuardFile(relPath string) bool {
	if !strings.HasPrefix(relPath, "cli/app/ui_") || strings.HasSuffix(relPath, "_test.go") {
		return false
	}
	switch filepath.Base(relPath) {
	case "ui_key_adapter.go", "ui_keymap.go", "ui_runtime_ctrl_c.go":
		return false
	default:
		return true
	}
}

func functionHandlesRuntimeCtrlC(fn *ast.FuncDecl, info *types.Info) bool {
	handlesCtrlC := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CaseClause:
			if caseHasBubbleTeaCtrlC(typed, info) {
				handlesCtrlC = true
				return false
			}
		case *ast.SwitchStmt:
			if switchHasKeyMessageCtrlCText(typed, info) {
				handlesCtrlC = true
				return false
			}
		case *ast.BinaryExpr:
			if binaryComparesKeyMessageToCtrlC(typed, info) {
				handlesCtrlC = true
				return false
			}
		}
		return true
	})
	return handlesCtrlC
}

func caseHasBubbleTeaCtrlC(clause *ast.CaseClause, info *types.Info) bool {
	for _, expr := range clause.List {
		selector, ok := expr.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "KeyCtrlC" {
			continue
		}
		constant, ok := info.Uses[selector.Sel].(*types.Const)
		if ok && constant.Pkg() != nil && constant.Pkg().Path() == "github.com/charmbracelet/bubbletea" {
			return true
		}
	}
	return false
}

func switchHasKeyMessageCtrlCText(stmt *ast.SwitchStmt, info *types.Info) bool {
	if !isKeyMessageTextExpression(stmt.Tag, info) || stmt.Body == nil {
		return false
	}
	for _, statement := range stmt.Body.List {
		clause, ok := statement.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range clause.List {
			if stringLiteralValue(expr) == "ctrl+c" {
				return true
			}
		}
	}
	return false
}

func binaryComparesKeyMessageToCtrlC(expr *ast.BinaryExpr, info *types.Info) bool {
	if expr.Op != token.EQL {
		return false
	}
	return (isKeyMessageTextExpression(expr.X, info) && stringLiteralValue(expr.Y) == "ctrl+c") ||
		(isKeyMessageTextExpression(expr.Y, info) && stringLiteralValue(expr.X) == "ctrl+c")
}

func isKeyMessageTextExpression(expr ast.Expr, info *types.Info) bool {
	if isKeyMessageStringCall(expr, info) {
		return true
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || !isPackageFunction(call.Fun, info, "strings", "ToLower") {
		return false
	}
	return isKeyMessageStringCall(call.Args[0], info)
}

func isKeyMessageStringCall(expr ast.Expr, info *types.Info) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "String" || !isBubbleTeaKeyMessage(info.TypeOf(selector.X)) {
		return false
	}
	selection := info.Selections[selector]
	return selection != nil && selection.Obj().Name() == "String"
}

func isBubbleTeaKeyMessage(typ types.Type) bool {
	for {
		pointer, ok := typ.(*types.Pointer)
		if !ok {
			break
		}
		typ = pointer.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "github.com/charmbracelet/bubbletea" && named.Obj().Name() == "KeyMsg"
}

func isPackageFunction(expr ast.Expr, info *types.Info, packagePath, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[selector.Sel].(*types.Func)
	return ok && fn.Name() == name && fn.Pkg() != nil && fn.Pkg().Path() == packagePath
}

func stringLiteralValue(expr ast.Expr) string {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	return value
}

func functionCallsRuntimeCtrlCHelper(fn *ast.FuncDecl, info *types.Info) bool {
	callsHelper := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method, ok := info.Uses[selector.Sel].(*types.Func)
		if ok && method.Name() == "handleRuntimeCtrlC" && method.Pkg() != nil && method.Pkg().Path() == "core/cli/app" {
			callsHelper = true
			return false
		}
		return true
	})
	return callsHelper
}
