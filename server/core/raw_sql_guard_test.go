package core_test

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"core/internal/testharness"

	"golang.org/x/tools/go/packages"
)

func TestProductionGoUsesGeneratedDatabaseQuerySeams(t *testing.T) {
	t.Run("rejects constant query through typed DBTX helper", func(t *testing.T) {
		pkg, root := generatedQueryGuardFixture(t, `package fixture

import (
	"context"
	"database/sql"
)

type DBTX interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const query = "SELECT id FROM sessions"

func execute(ctx context.Context, db DBTX) {
	run(ctx, db, query)
}

func run(ctx context.Context, db DBTX, statement string) {
	_, _ = db.QueryContext(ctx, statement)
}
`)
		if violations := generatedQueryBoundaryViolations(pkg, root); len(violations) < 2 {
			t.Fatalf("typed DBTX helper must reject both the dynamic query sink and forwarded constant query, violations = %v", violations)
		}
	})

	t.Run("allows constants that do not reach a query seam", func(t *testing.T) {
		pkg, root := generatedQueryGuardFixture(t, `package fixture

const label = "SELECT a task in the UI"

func display() string {
	return label
}
`)
		if violations := generatedQueryBoundaryViolations(pkg, root); len(violations) > 0 {
			t.Fatalf("non-query constant violations = %v, want none", violations)
		}
	})

	repoRoot := findRepoRoot(t)
	pkgs := testharness.LoadTypedPackages(t, repoRoot, false, "./server/...", "./cli/...", "./shared/...")
	violations := make([]string, 0)
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) {
			continue
		}
		violations = append(violations, generatedQueryBoundaryViolations(pkg, repoRoot)...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("production database query boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

var generatedDatabaseQueryPackage = map[string]bool{
	"core/server/metadata/sqlitegen":          true,
	"core/server/metadata/sqlitelifecyclegen": true,
}

func generatedQueryGuardFixture(t *testing.T, source string) (*packages.Package, string) {
	t.Helper()
	root := t.TempDir()
	testharness.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	testharness.WriteFile(t, filepath.Join(root, "server/core/testfixture/fixture.go"), source)
	pkgs := testharness.LoadTypedPackages(t, root, false, "./server/core/testfixture")
	return testharness.PackageByPath(t, pkgs, "core/server/core/testfixture"), root
}

func generatedQueryBoundaryViolations(pkg *packages.Package, repoRoot string) []string {
	if generatedDatabaseQueryPackage[pkg.PkgPath] {
		return nil
	}
	violations := embeddedSQLViolations(pkg)
	violations = append(violations, databaseQueryFlowViolations(pkg, repoRoot)...)
	return violations
}

func embeddedSQLViolations(pkg *packages.Package) []string {
	if len(pkg.EmbedPatterns) == 0 {
		return nil
	}
	violations := make([]string, 0)
	for _, pattern := range pkg.EmbedPatterns {
		if filepath.Ext(pattern) != ".sql" {
			continue
		}
		if pkg.PkgPath == "core/server/metadata" && pattern == "migrations/*.up.sql" {
			continue
		}
		violations = append(violations, pkg.PkgPath+": production SQL embeds must be metadata migrations declared through the generated-query boundary")
	}
	return violations
}

type databaseQueryFlow struct {
	function       *types.Func
	parameters     map[*types.Var]int
	queryParameter map[int]struct{}
	directCalls    []databaseQueryArgument
}

type databaseQueryArgument struct {
	expression ast.Expr
	position   token.Pos
}

type databaseQueryForwardingCall struct {
	caller        *databaseQueryFlow
	callee        *types.Func
	argumentIndex int
	argument      ast.Expr
	position      token.Pos
}

func databaseQueryFlowViolations(pkg *packages.Package, repoRoot string) []string {
	flows, forwardingCalls := collectDatabaseQueryFlows(pkg)
	propagateDatabaseQueryParameters(pkg, flows, forwardingCalls)
	violations := make([]string, 0)
	seen := make(map[token.Pos]struct{})
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, queryCall := databaseQueryArgumentIndex(pkg.TypesInfo.Selections[selector]); queryCall {
				violations = appendDatabaseQuerySinkViolation(violations, seen, pkg, repoRoot, selector.Sel.Pos())
			}
			return true
		})
	}
	for _, flow := range flows {
		for _, call := range flow.directCalls {
			if isConstantStringExpression(pkg, call.expression) {
				violations = appendDatabaseQueryViolation(violations, seen, pkg, repoRoot, call.position)
			}
		}
	}
	for _, call := range forwardingCalls {
		callee, found := flows[call.callee]
		if !found {
			continue
		}
		if _, reachesQuery := callee.queryParameter[call.argumentIndex]; reachesQuery && isConstantStringExpression(pkg, call.argument) {
			violations = appendDatabaseQueryViolation(violations, seen, pkg, repoRoot, call.position)
		}
	}
	return violations
}

func collectDatabaseQueryFlows(pkg *packages.Package) (map[*types.Func]*databaseQueryFlow, []databaseQueryForwardingCall) {
	flows := make(map[*types.Func]*databaseQueryFlow)
	for _, file := range pkg.Syntax {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			object, ok := pkg.TypesInfo.Defs[function.Name].(*types.Func)
			if !ok {
				continue
			}
			signature, ok := object.Type().(*types.Signature)
			if !ok {
				continue
			}
			parameters := make(map[*types.Var]int, signature.Params().Len())
			for index := 0; index < signature.Params().Len(); index++ {
				parameters[signature.Params().At(index)] = index
			}
			flows[object] = &databaseQueryFlow{
				function:       object,
				parameters:     parameters,
				queryParameter: make(map[int]struct{}),
			}
		}
	}

	var forwardingCalls []databaseQueryForwardingCall
	for _, file := range pkg.Syntax {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			object, ok := pkg.TypesInfo.Defs[function.Name].(*types.Func)
			if !ok {
				continue
			}
			flow, found := flows[object]
			if !found {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok {
					if argumentIndex, queryCall := databaseQueryArgumentIndex(pkg.TypesInfo.Selections[selector]); queryCall && argumentIndex < len(call.Args) {
						argument := call.Args[argumentIndex]
						flow.directCalls = append(flow.directCalls, databaseQueryArgument{
							expression: argument,
							position:   argument.Pos(),
						})
						if parameterIndex, parameter := flow.parameterIndex(pkg, argument); parameter {
							flow.queryParameter[parameterIndex] = struct{}{}
						}
						return true
					}
				}
				callee := calledPackageFunction(pkg, call.Fun)
				if callee == nil {
					return true
				}
				if _, local := flows[callee]; !local {
					return true
				}
				for index, argument := range call.Args {
					forwardingCalls = append(forwardingCalls, databaseQueryForwardingCall{
						caller:        flow,
						callee:        callee,
						argumentIndex: index,
						argument:      argument,
						position:      argument.Pos(),
					})
				}
				return true
			})
		}
	}
	return flows, forwardingCalls
}

func (f *databaseQueryFlow) parameterIndex(pkg *packages.Package, expression ast.Expr) (int, bool) {
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return 0, false
	}
	parameter, ok := pkg.TypesInfo.Uses[identifier].(*types.Var)
	if !ok {
		return 0, false
	}
	index, found := f.parameters[parameter]
	return index, found
}

func propagateDatabaseQueryParameters(pkg *packages.Package, flows map[*types.Func]*databaseQueryFlow, calls []databaseQueryForwardingCall) {
	for changed := true; changed; {
		changed = false
		for _, call := range calls {
			callee, found := flows[call.callee]
			if !found {
				continue
			}
			if _, reachesQuery := callee.queryParameter[call.argumentIndex]; !reachesQuery {
				continue
			}
			parameterIndex, parameter := call.caller.parameterIndex(pkg, call.argument)
			if !parameter {
				continue
			}
			if _, found := call.caller.queryParameter[parameterIndex]; found {
				continue
			}
			call.caller.queryParameter[parameterIndex] = struct{}{}
			changed = true
		}
	}
}

func calledPackageFunction(pkg *packages.Package, expression ast.Expr) *types.Func {
	switch call := expression.(type) {
	case *ast.Ident:
		function, _ := pkg.TypesInfo.Uses[call].(*types.Func)
		return function
	case *ast.SelectorExpr:
		function, _ := pkg.TypesInfo.Uses[call.Sel].(*types.Func)
		return function
	default:
		return nil
	}
}

func databaseQueryArgumentIndex(selection *types.Selection) (int, bool) {
	if selection == nil {
		return 0, false
	}
	function, ok := selection.Obj().(*types.Func)
	if !ok {
		return 0, false
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || !returnsDatabaseQueryResult(signature.Results()) {
		return 0, false
	}
	if signature.Params().Len() > 1 && isContextType(signature.Params().At(0).Type()) && isStringType(signature.Params().At(1).Type()) {
		return 1, true
	}
	if signature.Params().Len() > 0 && isStringType(signature.Params().At(0).Type()) {
		return 0, true
	}
	return 0, false
}

func returnsDatabaseQueryResult(results *types.Tuple) bool {
	switch results.Len() {
	case 1:
		return isDatabaseSQLNamedType(results.At(0).Type(), "Row")
	case 2:
		return isErrorType(results.At(1).Type()) &&
			(isDatabaseSQLNamedType(results.At(0).Type(), "Rows") ||
				isDatabaseSQLNamedType(results.At(0).Type(), "Result") ||
				isDatabaseSQLNamedType(results.At(0).Type(), "Stmt"))
	default:
		return false
	}
}

func isDatabaseSQLNamedType(typ types.Type, name string) bool {
	switch typed := types.Unalias(typ).(type) {
	case *types.Pointer:
		return isDatabaseSQLNamedType(typed.Elem(), name)
	case *types.Named:
		return typed.Obj().Pkg() != nil && typed.Obj().Pkg().Path() == "database/sql" && typed.Obj().Name() == name
	default:
		return false
	}
}

func isConstantStringExpression(pkg *packages.Package, expression ast.Expr) bool {
	value, found := pkg.TypesInfo.Types[expression]
	if !found || value.Value == nil {
		return false
	}
	basic, ok := types.Unalias(value.Type).Underlying().(*types.Basic)
	if !ok {
		return false
	}
	return basic.Kind() == types.String || basic.Kind() == types.UntypedString
}

func appendDatabaseQueryViolation(violations []string, seen map[token.Pos]struct{}, pkg *packages.Package, repoRoot string, position token.Pos) []string {
	if _, duplicate := seen[position]; duplicate {
		return violations
	}
	seen[position] = struct{}{}
	sourcePosition := testharness.SourcePosition(pkg, position)
	relPath, found := testharness.RepositoryRelativePath(repoRoot, sourcePosition.Filename)
	if !found {
		relPath = sourcePosition.Filename
	}
	return append(violations, relPath+":"+sourcePosition.String()+": constant query text bypasses generated query seams")
}

func appendDatabaseQuerySinkViolation(violations []string, seen map[token.Pos]struct{}, pkg *packages.Package, repoRoot string, position token.Pos) []string {
	if _, duplicate := seen[position]; duplicate {
		return violations
	}
	seen[position] = struct{}{}
	sourcePosition := testharness.SourcePosition(pkg, position)
	relPath, found := testharness.RepositoryRelativePath(repoRoot, sourcePosition.Filename)
	if !found {
		relPath = sourcePosition.Filename
	}
	return append(violations, relPath+":"+sourcePosition.String()+": database query call bypasses generated query seams")
}
