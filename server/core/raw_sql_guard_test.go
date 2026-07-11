package core_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

func TestRawSQLGuardScannerFixtures(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		source    string
		wantParts []string
	}{
		{
			name: "direct database call with nonliteral query on typed receiver",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
import (
	"context"
	"database/sql"
)
func f(ctx context.Context, handle *sql.DB, query string) { _, _ = handle.QueryContext(ctx, query) }`,
			wantParts: []string{"direct database SQL call QueryContext"},
		},
		{
			name: "direct database call with inferred sql open receiver",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
import (
	"context"
	"database/sql"
)
func f(ctx context.Context, dsn string, query string) {
	handle, _ := sql.Open("sqlite", dsn)
	_, _ = handle.QueryContext(ctx, query)
}`,
			wantParts: []string{"direct database SQL call QueryContext"},
		},
		{
			name: "direct database call with inferred metadata db accessor",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
func f(ctx context.Context, store Store, query string) {
	handle := store.DB()
	_, _ = handle.ExecContext(ctx, query)
}`,
			wantParts: []string{"direct database SQL call ExecContext"},
		},
		{
			name: "helper sql literal",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
func f() { helper("SELECT id FROM sessions") }`,
			wantParts: []string{"raw SQL string literal"},
		},
		{
			name: "constant concatenated sql literal",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
func f() { helper("SELECT " + "id FROM sessions") }`,
			wantParts: []string{"raw SQL string literal"},
		},
		{
			name: "case insensitive select literals",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
func f() { helper("select id from sessions"); helper("select 1"); helper("SELECT COUNT(*)") }`,
			wantParts: []string{"raw SQL string literal"},
		},
		{
			name: "clause fragments",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
func f() { _, _, _ = "name = ?", "WHERE project_id = ?", "WHERE project_id=?" }`,
			wantParts: []string{"raw SQL string literal"},
		},
		{
			name: "lowercase clause fragments",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
func f() { _, _, _, _ = "from sessions", "join tasks t on t.id = s.task_id", "order by updated_at_unix_ms desc", "limit 10" }`,
			wantParts: []string{"raw SQL string literal"},
		},
		{
			name: "private query embed",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
import "embed"
//go:embed queries/list.sql
var q string
var _ embed.FS`,
			wantParts: []string{"production code must not embed private SQL query files"},
		},
		{
			name: "sqlite starters",
			path: filepath.Join("server", "example", "store.go"),
			source: `package example
func f() { _, _, _, _, _ = "EXPLAIN SELECT 1", "ANALYZE", "ATTACH DATABASE ? AS aux", "SAVEPOINT graph", "RELEASE graph" }`,
			wantParts: []string{"raw SQL string literal"},
		},
		{
			name:   "test sql ignored",
			path:   filepath.Join("server", "example", "store_test.go"),
			source: `package example; func f() { _ = "SELECT id FROM sessions" }`,
		},
		{
			name:   "approved generated package ignored",
			path:   filepath.Join("server", "metadata", "sqlitegen", "queries.sql.go"),
			source: `package sqlitegen; func f() { _ = "SELECT id FROM sessions" }`,
		},
		{
			name: "metadata migration embed allowed",
			path: filepath.Join("server", "metadata", "db.go"),
			source: `package metadata
import "embed"
//go:embed migrations/*.up.sql
var migrationsFS embed.FS`,
		},
		{
			name: "non sql api names ignored",
			path: filepath.Join("server", "example", "cache.go"),
			source: `package example
func f(u url.URL, c RequestCache) { _ = u.Query(); _ = c.Prepare("entry") }`,
		},
		{
			name: "ordinary prose and pragma dsn options ignored",
			path: filepath.Join("server", "metadata", "db.go"),
			source: `package metadata
func f() { _, _, _, _, _, _, _ = "select a task in the UI", "join node missing", "from the UI", "limit must be positive", "foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)" }`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations, err := scanGoSourceForRawSQL(tt.path, []byte(tt.source))
			if err != nil {
				t.Fatalf("scan source: %v", err)
			}
			for _, part := range tt.wantParts {
				if !containsViolationPart(violations, part) {
					t.Fatalf("violations %v do not contain %q", violations, part)
				}
			}
			if len(tt.wantParts) == 0 && len(violations) > 0 {
				t.Fatalf("unexpected violations: %v", violations)
			}
		})
	}
}

func TestProductionGoDoesNotContainRawSQL(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	if err := walkProductionGoFiles(repoRoot, func(path string, relPath string) error {
		if !isRawSQLScannedGoFile(relPath) {
			return nil
		}
		source, readErr := osReadFile(path)
		if readErr != nil {
			return readErr
		}
		fileViolations, scanErr := scanGoSourceForRawSQL(relPath, source)
		if scanErr != nil {
			return scanErr
		}
		violations = append(violations, fileViolations...)
		return nil
	}); err != nil {
		t.Fatalf("scan repository for raw SQL: %v", err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("production raw SQL violations:\n%s", strings.Join(violations, "\n"))
	}
}

var osReadFile = func(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func scanGoSourceForRawSQL(path string, source []byte) ([]string, error) {
	if !isRawSQLScannedGoFile(path) {
		return nil, nil
	}
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	context := newRawSQLScanContext(file)
	violations := make([]string, 0)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:embed") && embedsPrivateSQLQuery(comment.Text) {
				violations = append(violations, rawSQLViolation(path, fileSet.Position(comment.Pos()).Line, "production code must not embed private SQL query files"))
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.BasicLit:
			if n.Kind == token.STRING && rawSQLLiteral(n.Value) {
				violations = append(violations, rawSQLViolation(path, fileSet.Position(n.Pos()).Line, "raw SQL string literal"))
			}
		case *ast.BinaryExpr:
			if text, ok := constantStringExpression(n); ok && rawSQLText(text) {
				violations = append(violations, rawSQLViolation(path, fileSet.Position(n.Pos()).Line, "raw SQL string literal"))
			}
		case *ast.CallExpr:
			if selector, ok := n.Fun.(*ast.SelectorExpr); ok && context.isDirectDatabaseSQLCall(selector, n) {
				violations = append(violations, rawSQLViolation(path, fileSet.Position(selector.Sel.Pos()).Line, "direct database SQL call "+selector.Sel.Name))
			}
		}
		return true
	})
	return violations, nil
}

type rawSQLScanContext struct {
	databaseSQLImportNames map[string]struct{}
	databaseTypedNames     map[string]struct{}
}

func newRawSQLScanContext(file *ast.File) rawSQLScanContext {
	context := rawSQLScanContext{
		databaseSQLImportNames: map[string]struct{}{},
		databaseTypedNames:     map[string]struct{}{},
	}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "database/sql" {
			continue
		}
		name := "sql"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" {
			context.databaseSQLImportNames[name] = struct{}{}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncDecl:
			context.collectFieldListDatabaseTypes(n.Type.Params)
			context.collectFieldListDatabaseTypes(n.Type.Results)
		case *ast.FuncLit:
			context.collectFieldListDatabaseTypes(n.Type.Params)
			context.collectFieldListDatabaseTypes(n.Type.Results)
		case *ast.ValueSpec:
			if context.isDatabaseHandleType(n.Type) {
				for _, name := range n.Names {
					context.databaseTypedNames[name.Name] = struct{}{}
				}
			}
		case *ast.AssignStmt:
			context.collectAssignedDatabaseHandles(n)
		}
		return true
	})
	return context
}

func (c rawSQLScanContext) collectAssignedDatabaseHandles(assign *ast.AssignStmt) {
	for index, lhs := range assign.Lhs {
		name, ok := lhs.(*ast.Ident)
		if !ok || name.Name == "_" || index >= len(assign.Rhs) {
			continue
		}
		if c.isDatabaseHandleValue(assign.Rhs[index]) {
			c.databaseTypedNames[name.Name] = struct{}{}
		}
	}
}

func (c rawSQLScanContext) collectFieldListDatabaseTypes(fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		if !c.isDatabaseHandleType(field.Type) {
			continue
		}
		for _, name := range field.Names {
			c.databaseTypedNames[name.Name] = struct{}{}
		}
	}
}

func containsViolationPart(violations []string, part string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, part) {
			return true
		}
	}
	return false
}

func rawSQLViolation(path string, line int, reason string) string {
	return fmt.Sprintf("%s:%d: %s; declare production SQL in server/metadata/queries.sql or server/metadata/lifecycle.sql and consume it through an approved generated seam", path, line, reason)
}

func isRawSQLScannedGoFile(path string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	return !isApprovedGeneratedSQLPath(path)
}

func isApprovedGeneratedSQLPath(path string) bool {
	clean := filepath.Clean(path)
	approved := []string{
		filepath.Join("server", "metadata", "sqlitegen"),
		filepath.Join("server", "metadata", "sqlitelifecyclegen"),
	}
	for _, prefix := range approved {
		if clean == prefix || strings.HasPrefix(clean, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func embedsPrivateSQLQuery(text string) bool {
	fields := strings.Fields(strings.TrimPrefix(text, "//go:embed"))
	for _, field := range fields {
		clean := filepath.ToSlash(field)
		if strings.HasSuffix(clean, ".sql") && !strings.HasPrefix(clean, "migrations/") {
			return true
		}
	}
	return false
}

var directSQLSelectors = map[string]struct{}{
	"QueryContext":    {},
	"QueryRowContext": {},
	"ExecContext":     {},
	"PrepareContext":  {},
	"Query":           {},
	"QueryRow":        {},
	"Exec":            {},
	"Prepare":         {},
}

func (c rawSQLScanContext) isDirectDatabaseSQLCall(selector *ast.SelectorExpr, call *ast.CallExpr) bool {
	if _, ok := directSQLSelectors[selector.Sel.Name]; !ok {
		return false
	}
	if !hasSQLCallArity(selector.Sel.Name, len(call.Args)) {
		return false
	}
	return c.isTypedDatabaseReceiver(selector.X) || isDatabaseLikeReceiver(selector.X)
}

func hasSQLCallArity(name string, argCount int) bool {
	switch name {
	case "QueryContext", "QueryRowContext", "ExecContext", "PrepareContext":
		return argCount >= 2
	case "Query", "QueryRow", "Exec", "Prepare":
		return argCount >= 1
	default:
		return false
	}
}

func isDatabaseLikeReceiver(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return isDatabaseLikeName(x.Name)
	case *ast.SelectorExpr:
		return isDatabaseLikeName(x.Sel.Name)
	case *ast.CallExpr:
		selector, ok := x.Fun.(*ast.SelectorExpr)
		return ok && isDatabaseLikeName(selector.Sel.Name)
	case *ast.IndexExpr:
		return isDatabaseLikeReceiver(x.X)
	case *ast.ParenExpr:
		return isDatabaseLikeReceiver(x.X)
	default:
		return false
	}
}

func (c rawSQLScanContext) isTypedDatabaseReceiver(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		_, ok := c.databaseTypedNames[x.Name]
		return ok
	case *ast.ParenExpr:
		return c.isTypedDatabaseReceiver(x.X)
	case *ast.IndexExpr:
		return c.isTypedDatabaseReceiver(x.X)
	default:
		return false
	}
}

func (c rawSQLScanContext) isDatabaseHandleType(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.StarExpr:
		return c.isDatabaseHandleType(x.X)
	case *ast.SelectorExpr:
		if x.Sel.Name == "DBTX" {
			return true
		}
		if _, ok := x.X.(*ast.Ident); !ok {
			return false
		}
		qualifier := x.X.(*ast.Ident).Name
		if _, ok := c.databaseSQLImportNames[qualifier]; !ok {
			return false
		}
		switch x.Sel.Name {
		case "DB", "Tx", "Conn", "Stmt", "Rows", "Row":
			return true
		default:
			return false
		}
	case *ast.InterfaceType:
		for _, method := range x.Methods.List {
			for _, name := range method.Names {
				if _, ok := directSQLSelectors[name.Name]; ok {
					return true
				}
			}
		}
		return false
	default:
		return false
	}
}

func (c rawSQLScanContext) isDatabaseHandleValue(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.CallExpr:
		selector, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if qualifier, ok := selector.X.(*ast.Ident); ok {
			if _, imported := c.databaseSQLImportNames[qualifier.Name]; imported && selector.Sel.Name == "Open" {
				return true
			}
		}
		switch selector.Sel.Name {
		case "DB":
			return true
		case "Begin", "BeginTx", "Conn":
			return c.isTypedDatabaseReceiver(selector.X) || isDatabaseLikeReceiver(selector.X)
		default:
			return false
		}
	case *ast.ParenExpr:
		return c.isDatabaseHandleValue(x.X)
	default:
		return false
	}
}

func isDatabaseLikeName(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "db", "tx", "conn", "database", "databaseconn", "sqldb", "sqltx":
		return true
	}
	return strings.HasSuffix(name, "DB") || strings.HasSuffix(name, "Tx") || strings.HasSuffix(name, "Conn")
}

func rawSQLLiteral(quoted string) bool {
	value, err := strconv.Unquote(quoted)
	if err != nil {
		return false
	}
	return rawSQLText(value)
}

func constantStringExpression(expr ast.Expr) (string, bool) {
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(x.Value)
		if err != nil {
			return "", false
		}
		return value, true
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		left, ok := constantStringExpression(x.X)
		if !ok {
			return "", false
		}
		right, ok := constantStringExpression(x.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	case *ast.ParenExpr:
		return constantStringExpression(x.X)
	default:
		return "", false
	}
}

func rawSQLText(value string) bool {
	text := trimLeadingSQLTrivia(value)
	if text == "" {
		return false
	}
	if containsSQLPredicateFragment(strings.ToUpper(text)) {
		return true
	}
	upper := strings.ToUpper(text)
	if startsWithSQLStatement(upper) {
		first, _ := firstSQLWord(upper)
		if ambiguousSQLStatementStarterRequiresUppercase(first) && !startsWithUppercaseSQLKeyword(text) {
			return false
		}
		return true
	}
	return startsWithSQLClause(upper)
}

func ambiguousSQLStatementStarterRequiresUppercase(first string) bool {
	switch first {
	case "VACUUM", "BEGIN", "COMMIT", "ROLLBACK", "ANALYZE", "ATTACH", "DETACH", "SAVEPOINT", "RELEASE":
		return true
	default:
		return false
	}
}

func trimLeadingSQLTrivia(text string) string {
	trimmed := strings.TrimSpace(text)
	for {
		switch {
		case strings.HasPrefix(trimmed, "--"):
			newline := strings.IndexByte(trimmed, '\n')
			if newline < 0 {
				return ""
			}
			trimmed = strings.TrimSpace(trimmed[newline+1:])
		case strings.HasPrefix(trimmed, "/*"):
			end := strings.Index(trimmed, "*/")
			if end < 0 {
				return ""
			}
			trimmed = strings.TrimSpace(trimmed[end+2:])
		default:
			return trimmed
		}
	}
}

func startsWithSQLStatement(upper string) bool {
	first, rest := firstSQLWord(upper)
	switch first {
	case "SELECT":
		return containsSQLToken(rest, "FROM") || containsSQLToken(rest, "WHERE") || startsWithSQLSelectExpression(rest)
	case "WITH":
		return containsSQLToken(rest, "AS") && strings.Contains(rest, "(")
	case "INSERT", "REPLACE":
		next, _ := firstSQLWord(rest)
		return next == "INTO"
	case "UPDATE":
		return containsSQLToken(rest, "SET")
	case "DELETE":
		next, _ := firstSQLWord(rest)
		return next == "FROM"
	case "PRAGMA":
		return rest != ""
	case "CREATE", "ALTER", "DROP":
		next, _ := firstSQLWord(rest)
		switch next {
		case "TABLE", "INDEX", "VIEW", "TRIGGER":
			return true
		default:
			return false
		}
	case "VACUUM", "BEGIN", "COMMIT", "ROLLBACK", "ANALYZE":
		return rest == ""
	case "ATTACH":
		next, _ := firstSQLWord(rest)
		return next == "DATABASE"
	case "DETACH":
		next, _ := firstSQLWord(rest)
		return next == "DATABASE"
	case "SAVEPOINT", "RELEASE":
		return rest != ""
	case "EXPLAIN":
		next, _ := firstSQLWord(rest)
		switch next {
		case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "QUERY":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func startsWithSQLClause(upper string) bool {
	first, rest := firstSQLWord(upper)
	switch first {
	case "WHERE", "HAVING":
		return containsSQLPredicateFragment(rest) || containsSQLComparison(rest)
	case "FROM":
		return startsWithSQLTableReference(rest)
	case "JOIN":
		return startsWithSQLTableReference(rest) && containsSQLToken(rest, "ON")
	case "ON":
		return containsSQLPredicateFragment(rest) || containsSQLComparison(rest)
	case "SET":
		return containsSQLPredicateFragment(rest) || containsSQLEqualityAssignment(rest)
	case "VALUES":
		return strings.HasPrefix(strings.TrimSpace(rest), "(")
	case "RETURNING":
		return startsWithSQLIdentifierList(rest)
	case "LIMIT", "OFFSET":
		return startsWithSQLLimitValue(rest)
	case "ORDER", "GROUP":
		next, afterNext := firstSQLWord(strings.TrimSpace(rest))
		return next == "BY" && startsWithSQLIdentifierList(afterNext)
	case "AND", "OR":
		return containsSQLPredicateFragment(rest)
	default:
		return false
	}
}

func startsWithSQLTableReference(rest string) bool {
	word, remaining := firstSQLWord(rest)
	if word == "" || isCommonProseWord(word) {
		return false
	}
	if remaining == "" {
		return true
	}
	next, afterNext := firstSQLWord(remaining)
	switch next {
	case "AS":
		alias, _ := firstSQLWord(afterNext)
		return alias != "" && !isCommonProseWord(alias)
	case "WHERE", "JOIN", "LEFT", "RIGHT", "INNER", "OUTER", "CROSS", "ON", "ORDER", "GROUP", "LIMIT":
		return true
	default:
		return containsSQLToken(remaining, "ON") || containsSQLToken(remaining, "WHERE")
	}
}

func startsWithSQLIdentifierList(rest string) bool {
	word, remaining := firstSQLWord(rest)
	if word == "" || isCommonProseWord(word) {
		return false
	}
	if strings.Contains(remaining, ",") || containsSQLToken(remaining, "DESC") || containsSQLToken(remaining, "ASC") {
		return true
	}
	return remaining == ""
}

func startsWithSQLLimitValue(rest string) bool {
	trimmed := strings.TrimSpace(rest)
	if trimmed == "" {
		return false
	}
	first := rune(trimmed[0])
	return unicode.IsDigit(first) || first == '?' || first == ':' || first == '$'
}

func containsSQLComparison(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !startsWithIdentifier(trimmed) {
		return false
	}
	comparisonMarkers := []string{"=", "<>", "!=", ">=", "<=", ">", "<"}
	compact := removeSQLWhitespace(trimmed)
	for _, marker := range comparisonMarkers {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func containsSQLEqualityAssignment(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !startsWithIdentifier(trimmed) {
		return false
	}
	return strings.Contains(removeSQLWhitespace(trimmed), "=")
}

func removeSQLWhitespace(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		if !unicode.IsSpace(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func isCommonProseWord(word string) bool {
	switch word {
	case "A", "AN", "THE", "THIS", "THAT", "THESE", "THOSE", "NODE", "TASK", "RUN", "USER", "UI":
		return true
	default:
		return false
	}
}

func startsWithSQLSelectExpression(rest string) bool {
	trimmed := strings.TrimSpace(rest)
	if trimmed == "" {
		return false
	}
	first := rune(trimmed[0])
	if first == '*' || first == '\'' || first == '"' || unicode.IsDigit(first) {
		return true
	}
	word, afterWord := firstSQLWord(trimmed)
	if word == "" {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(afterWord), "(") {
		return true
	}
	expressionMarkers := []string{",", "||", "+", "-", "*", "/", "%"}
	for _, marker := range expressionMarkers {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}
	switch word {
	case "COUNT", "SUM", "AVG", "MIN", "MAX", "COALESCE", "CAST", "EXISTS":
		return true
	default:
		return false
	}
}

func containsSQLPredicateFragment(upper string) bool {
	trimmed := strings.TrimSpace(upper)
	if trimmed == "" || !startsWithIdentifier(trimmed) {
		return false
	}
	predicateMarkers := []string{
		"=?", "<>?", "!=?", ">?", "<?", ">=?", "<=?",
		"LIKE?", "IN(", "ISNULL", "ISNOTNULL",
	}
	compact := removeSQLWhitespace(trimmed)
	for _, marker := range predicateMarkers {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func firstSQLWord(text string) (string, string) {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	end := 0
	for end < len(trimmed) {
		r := rune(trimmed[end])
		if !isWordRune(r) {
			break
		}
		end++
	}
	if end == 0 {
		return "", trimmed
	}
	return trimmed[:end], strings.TrimSpace(trimmed[end:])
}

func startsWithUppercaseSQLKeyword(text string) bool {
	word, _ := firstSQLWord(text)
	if word == "" {
		return false
	}
	for _, r := range word {
		if unicode.IsLetter(r) && !unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

func containsSQLToken(text string, token string) bool {
	for {
		word, rest := firstSQLWord(text)
		if word == "" {
			return false
		}
		if word == token {
			return true
		}
		if rest == "" {
			return false
		}
		text = rest
	}
}

func startsWithIdentifier(text string) bool {
	if text == "" {
		return false
	}
	r := rune(text[0])
	return r == '_' || unicode.IsLetter(r)
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
