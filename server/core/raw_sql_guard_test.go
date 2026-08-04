package core_test

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"github.com/antlr4-go/antlr/v4"
	"github.com/tursodatabase/libsql-client-go/sqliteparser"
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

	t.Run("accepts database syntax and rejects ambiguous prose", func(t *testing.T) {
		cases := map[string]bool{
			"SELECT id FROM sessions":           true,
			"SELECT 1":                          true,
			"select 1":                          true,
			"DELETE FROM sessions WHERE id = ?": true,
			"WHERE id = ?":                      true,
			"EXPLAIN SELECT 1":                  true,
			"ANALYZE":                           true,
			"ATTACH DATABASE ? AS aux":          true,
			"SAVEPOINT graph":                   true,
			"RELEASE graph":                     true,
			"VACUUM":                            true,
			"BEGIN":                             true,
			"COMMIT":                            true,
			"ROLLBACK":                          true,
			"DROP INDEX stale_sessions":         true,
			"SELECT a task in the UI":           false,
			"Select Workspace":                  false,
			"AGENT=kent":                        false,
			"end":                               false,
			"rollback":                          false,
			"SELECT id FROM":                    false,
		}
		for source, want := range cases {
			if got := isSQLiteStatementOrFragment(source); got != want {
				t.Errorf("isSQLiteStatementOrFragment(%q) = %t, want %t", source, got, want)
			}
		}
	})

	t.Run("rejects standalone and externally forwarded raw SQL constants", func(t *testing.T) {
		pkg, root := generatedQueryGuardFixture(t, `package fixture

const standalone = "select 1"

var packageQuery = "DELETE FROM sessions WHERE id = ?"

func external(statement string) {}

func forward() {
	external(packageQuery)
}
`)
		if violations := generatedQueryBoundaryViolations(pkg, root); len(violations) < 2 {
			t.Fatalf("standalone and external-helper raw SQL constants must violate, violations = %v", violations)
		}
	})
}

func TestProductionDarwinGoUsesGeneratedDatabaseQuerySeams(t *testing.T) {
	t.Parallel()
	assertProductionGeneratedQueryBoundaries(t, "darwin", "arm64")
}

func TestProductionLinuxGoUsesGeneratedDatabaseQuerySeams(t *testing.T) {
	t.Parallel()
	assertProductionGeneratedQueryBoundaries(t, "linux", "amd64")
}

func TestProductionWindowsGoUsesGeneratedDatabaseQuerySeams(t *testing.T) {
	t.Parallel()
	assertProductionGeneratedQueryBoundaries(t, "windows", "amd64")
}

func assertProductionGeneratedQueryBoundaries(t *testing.T, goos string, goarch string) {
	t.Helper()
	repoRoot := findRepoRoot(t)
	pkgs := testharness.LoadTypedPackagesForPlatform(t, repoRoot, false, goos, goarch, "./server/...", "./cli/...", "./shared/...")
	assertCoreRepositoryModule(t, pkgs)
	violations := make([]string, 0)
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) {
			continue
		}
		violations = append(violations, generatedQueryBoundaryViolations(pkg, repoRoot)...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("%s/%s production database query boundary violations:\n%s", goos, goarch, strings.Join(violations, "\n"))
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
	violations = append(violations, rawSQLConstantViolations(pkg, repoRoot)...)
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
		if isMetadataMigrationEmbed(pkg, pattern) {
			continue
		}
		violations = append(violations, pkg.PkgPath+": production SQL embeds must be metadata migrations declared through the generated-query boundary")
	}
	return violations
}

func isMetadataMigrationEmbed(pkg *packages.Package, pattern string) bool {
	if pkg.PkgPath == "core/server/metadata/migrations" {
		return filepath.Base(filepath.Clean(pattern)) == "*.up.sql"
	}
	if pkg.PkgPath != "core/server/metadata" || len(pkg.CompiledGoFiles) == 0 {
		return false
	}
	want := filepath.Join(filepath.Dir(pkg.CompiledGoFiles[0]), "migrations", "*.up.sql")
	return filepath.Clean(pattern) == filepath.Clean(want)
}

func rawSQLConstantViolations(pkg *packages.Package, repoRoot string) []string {
	violations := make([]string, 0)
	seen := make(map[token.Pos]struct{})
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			expression, ok := node.(ast.Expr)
			if !ok {
				return true
			}
			if binary, ok := expression.(*ast.BinaryExpr); ok && isConstantStringExpression(pkg, binary) {
				if isSQLiteStatementOrFragment(constantStringValue(pkg, binary)) {
					violations = appendRawSQLConstantViolation(violations, seen, pkg, repoRoot, binary.Pos())
				}
				return false
			}
			literal, ok := expression.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING || !isConstantStringExpression(pkg, literal) {
				return true
			}
			if isSQLiteStatementOrFragment(constantStringValue(pkg, literal)) {
				violations = appendRawSQLConstantViolation(violations, seen, pkg, repoRoot, literal.Pos())
			}
			return true
		})
	}
	return violations
}

func constantStringValue(pkg *packages.Package, expression ast.Expr) string {
	value := pkg.TypesInfo.Types[expression].Value
	return constant.StringVal(value)
}

func isSQLiteStatementOrFragment(source string) bool {
	tokens, valid := sqliteTokens(source)
	if !valid || len(tokens) == 0 {
		return false
	}
	if hasNonProseRelationTarget(tokens) && parsesSQLiteStatement(source) {
		return true
	}
	if hasStandaloneSQLiteStatementStart(tokens) && parsesSQLiteStatement(source) {
		return true
	}
	switch tokens[0].GetTokenType() {
	case sqliteparser.SQLiteParserFROM_:
		return hasNonProseRelationTarget(tokens) && parsesSQLiteStatement("SELECT 1 "+source)
	case sqliteparser.SQLiteParserWHERE_, sqliteparser.SQLiteParserHAVING_, sqliteparser.SQLiteParserON_:
		return hasSQLBoundOrQuotedValue(tokens) && parsesSQLiteStatement("SELECT 1 "+source)
	case sqliteparser.SQLiteParserORDER_, sqliteparser.SQLiteParserGROUP_:
		return parsesSQLiteStatement("SELECT 1 " + source)
	case sqliteparser.SQLiteParserLIMIT_, sqliteparser.SQLiteParserOFFSET_:
		return hasSQLValueSyntax(tokens) && parsesSQLiteStatement("SELECT 1 "+source)
	case sqliteparser.SQLiteParserJOIN_, sqliteparser.SQLiteParserLEFT_, sqliteparser.SQLiteParserRIGHT_, sqliteparser.SQLiteParserINNER_, sqliteparser.SQLiteParserCROSS_:
		return hasNonProseRelationTarget(tokens) && parsesSQLiteStatement("SELECT 1 FROM raw_sql_guard "+source)
	case sqliteparser.SQLiteParserSET_:
		return hasSQLBoundOrQuotedValue(tokens) && parsesSQLiteStatement("UPDATE raw_sql_guard "+source)
	case sqliteparser.SQLiteParserVALUES_:
		return hasSQLValueSyntax(tokens) && parsesSQLiteStatement("INSERT INTO raw_sql_guard(value) "+source)
	}
	return hasComparisonOperator(tokens) &&
		hasSQLBoundOrQuotedValue(tokens) &&
		parsesSQLiteStatement("SELECT 1 WHERE "+source)
}

func hasStandaloneSQLiteStatementStart(tokens []antlr.Token) bool {
	first := tokens[0]
	switch first.GetTokenType() {
	case sqliteparser.SQLiteParserANALYZE_,
		sqliteparser.SQLiteParserATTACH_,
		sqliteparser.SQLiteParserBEGIN_,
		sqliteparser.SQLiteParserCOMMIT_,
		sqliteparser.SQLiteParserDETACH_,
		sqliteparser.SQLiteParserEXPLAIN_,
		sqliteparser.SQLiteParserREINDEX_,
		sqliteparser.SQLiteParserRELEASE_,
		sqliteparser.SQLiteParserROLLBACK_,
		sqliteparser.SQLiteParserSAVEPOINT_,
		sqliteparser.SQLiteParserVACUUM_:
		return isUppercaseSQLiteKeyword(first)
	case sqliteparser.SQLiteParserSELECT_:
		return isUppercaseSQLiteKeyword(first) || hasSQLValueSyntax(tokens)
	case sqliteparser.SQLiteParserINSERT_,
		sqliteparser.SQLiteParserUPDATE_,
		sqliteparser.SQLiteParserDELETE_,
		sqliteparser.SQLiteParserREPLACE_:
		return hasNonProseRelationTarget(tokens)
	case sqliteparser.SQLiteParserPRAGMA_:
		return len(tokens) > 1
	case sqliteparser.SQLiteParserCREATE_,
		sqliteparser.SQLiteParserALTER_,
		sqliteparser.SQLiteParserDROP_:
		return hasSQLiteStatementQualifier(
			tokens,
			sqliteparser.SQLiteParserTABLE_,
			sqliteparser.SQLiteParserINDEX_,
			sqliteparser.SQLiteParserVIEW_,
			sqliteparser.SQLiteParserTRIGGER_,
		)
	default:
		return false
	}
}

func isUppercaseSQLiteKeyword(token antlr.Token) bool {
	return token.GetText() == strings.ToUpper(token.GetText())
}

func hasSQLiteStatementQualifier(tokens []antlr.Token, qualifiers ...int) bool {
	if len(tokens) < 2 {
		return false
	}
	for _, qualifier := range qualifiers {
		if tokens[1].GetTokenType() == qualifier {
			return true
		}
	}
	return false
}

func sqliteTokens(source string) ([]antlr.Token, bool) {
	tokens, err := testharness.SQLiteTokens(source)
	return tokens, err == nil
}

func parsesSQLiteStatement(source string) bool {
	return testharness.ParseSQLite(source) == nil
}

func hasComparisonOperator(tokens []antlr.Token) bool {
	for _, token := range tokens {
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserASSIGN,
			sqliteparser.SQLiteParserEQ,
			sqliteparser.SQLiteParserNOT_EQ1,
			sqliteparser.SQLiteParserNOT_EQ2,
			sqliteparser.SQLiteParserLT,
			sqliteparser.SQLiteParserLT_EQ,
			sqliteparser.SQLiteParserGT,
			sqliteparser.SQLiteParserGT_EQ,
			sqliteparser.SQLiteParserLIKE_,
			sqliteparser.SQLiteParserGLOB_,
			sqliteparser.SQLiteParserMATCH_,
			sqliteparser.SQLiteParserREGEXP_:
			return true
		}
	}
	return false
}

func hasSQLValueSyntax(tokens []antlr.Token) bool {
	for _, token := range tokens {
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserBIND_PARAMETER,
			sqliteparser.SQLiteParserNUMERIC_LITERAL,
			sqliteparser.SQLiteParserSTRING_LITERAL,
			sqliteparser.SQLiteParserNULL_:
			return true
		}
	}
	return false
}

func hasSQLBoundOrQuotedValue(tokens []antlr.Token) bool {
	for _, token := range tokens {
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserBIND_PARAMETER, sqliteparser.SQLiteParserSTRING_LITERAL:
			return true
		}
	}
	return false
}

func hasNonProseRelationTarget(tokens []antlr.Token) bool {
	for index, token := range tokens {
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserFROM_,
			sqliteparser.SQLiteParserJOIN_,
			sqliteparser.SQLiteParserINTO_,
			sqliteparser.SQLiteParserUPDATE_,
			sqliteparser.SQLiteParserTABLE_,
			sqliteparser.SQLiteParserVIEW_:
			if index+1 >= len(tokens) {
				return false
			}
			if isNonProseRelationIdentifier(tokens[index+1]) {
				return true
			}
		}
	}
	return false
}

func isNonProseRelationIdentifier(token antlr.Token) bool {
	if token.GetTokenType() != sqliteparser.SQLiteParserIDENTIFIER {
		return false
	}
	switch strings.ToLower(token.GetText()) {
	case "a", "an", "the", "this", "that", "these", "those", "node", "task", "run", "user", "ui":
		return false
	default:
		return true
	}
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

func appendRawSQLConstantViolation(violations []string, seen map[token.Pos]struct{}, pkg *packages.Package, repoRoot string, position token.Pos) []string {
	if _, duplicate := seen[position]; duplicate {
		return violations
	}
	seen[position] = struct{}{}
	sourcePosition := testharness.SourcePosition(pkg, position)
	relPath, found := testharness.RepositoryRelativePath(repoRoot, sourcePosition.Filename)
	if !found {
		relPath = sourcePosition.Filename
	}
	return append(violations, relPath+":"+sourcePosition.String()+": raw SQL constant must be declared in a generated query seam")
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
