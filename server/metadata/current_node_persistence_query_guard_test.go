package metadata_test

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"github.com/antlr4-go/antlr/v4"
	"github.com/tursodatabase/libsql-client-go/sqliteparser"
)

func parseNamedSQLStatements(source string) (map[string]namedSQLStatement, error) {
	statements := make(map[string]namedSQLStatement)
	scanner := bufio.NewScanner(strings.NewReader(source))
	var name string
	var body strings.Builder
	flush := func() error {
		if name == "" {
			return nil
		}
		statementSource := strings.TrimSpace(body.String())
		shape, err := parseSQLiteStatementShape(statementSource)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, exists := statements[name]; exists {
			return fmt.Errorf("duplicate named query %s", name)
		}
		statements[name] = namedSQLStatement{name: name, source: statementSource, shape: shape}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "--" && fields[1] == "name:" {
			if err := flush(); err != nil {
				return nil, err
			}
			name = fields[2]
			body.Reset()
			body.WriteString(line)
			body.WriteByte('\n')
			continue
		}
		if name != "" {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return statements, nil
}

func parseGeneratedSQLQueries(path string) (map[string]namedSQLStatement, map[string]int, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		return nil, nil, err
	}
	statements := make(map[string]namedSQLStatement)
	methods := make(map[string]int)
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.GenDecl:
			if typed.Tok != token.CONST {
				continue
			}
			for _, spec := range typed.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, value := range valueSpec.Values {
					literal, ok := value.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					decoded, err := strconv.Unquote(literal.Value)
					if err != nil {
						return nil, nil, err
					}
					parsed, err := parseNamedSQLStatements(decoded)
					if err != nil {
						return nil, nil, err
					}
					for name, statement := range parsed {
						if _, exists := statements[name]; exists {
							return nil, nil, fmt.Errorf("duplicate generated query %s", name)
						}
						statements[name] = statement
					}
				}
			}
		case *ast.FuncDecl:
			if typed.Recv != nil {
				methods[typed.Name.Name]++
			}
		}
	}
	return statements, methods, nil
}

func compareGeneratedQueries(
	source map[string]namedSQLStatement,
	generated map[string]namedSQLStatement,
	methods map[string]int,
) []currentNodePersistenceFinding {
	var findings []currentNodePersistenceFinding
	for name, statement := range source {
		generatedStatement, ok := generated[name]
		if !ok {
			findings = append(findings, queryMismatchFinding(name, "generated SQL constant is missing"))
			continue
		}
		if !sqliteShapesEquivalent(statement.shape, generatedStatement.shape) {
			findings = append(findings, queryMismatchFinding(name, fmt.Sprintf("source shape %#v differs from generated shape %#v", statement.shape, generatedStatement.shape)))
		}
		if methods[name] != 1 {
			findings = append(findings, queryMismatchFinding(name, fmt.Sprintf("generated method count = %d, want 1", methods[name])))
		}
	}
	for name := range generated {
		if _, ok := source[name]; !ok {
			findings = append(findings, queryMismatchFinding(name, "generated SQL has no source statement"))
		}
	}
	sortPersistenceFindings(findings)
	return findings
}

func sqliteShapesEquivalent(source, generated sqliteStatementShape) bool {
	if source.operation != generated.operation ||
		source.target != generated.target ||
		!reflect.DeepEqual(source.relations, generated.relations) {
		return false
	}
	if len(source.projection) == 1 && source.projection[0] == "*" {
		return len(generated.projection) > 0
	}
	return reflect.DeepEqual(source.projection, generated.projection)
}

func parseSQLiteStatementShape(source string) (sqliteStatementShape, error) {
	tokens, err := testharness.SQLiteTokens(source)
	if err != nil {
		return sqliteStatementShape{}, err
	}
	parserSource, err := normalizeSQLCExpressions(tokens)
	if err != nil {
		return sqliteStatementShape{}, err
	}
	if err := testharness.ParseSQLite(parserSource); err != nil {
		return sqliteStatementShape{}, err
	}
	tokens, err = testharness.SQLiteTokens(parserSource)
	if err != nil {
		return sqliteStatementShape{}, err
	}
	shape := sqliteStatementShape{}
	relations := make(map[string]struct{})
	depth := 0
	for index, token := range tokens {
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserOPEN_PAR:
			depth++
		case sqliteparser.SQLiteParserCLOSE_PAR:
			depth--
		case sqliteparser.SQLiteParserINSERT_:
			if depth == 0 {
				shape.operation = "insert"
				shape.target = relationAfter(tokens, index, sqliteparser.SQLiteParserINTO_)
			}
		case sqliteparser.SQLiteParserUPDATE_:
			if depth == 0 && shape.operation == "" {
				shape.operation = "update"
				shape.target = identifierAfter(tokens, index)
			}
		case sqliteparser.SQLiteParserDELETE_:
			if depth == 0 {
				shape.operation = "delete"
				shape.target = relationAfter(tokens, index, sqliteparser.SQLiteParserFROM_)
			}
		case sqliteparser.SQLiteParserSELECT_:
			if depth == 0 && shape.operation == "" {
				shape.operation = "select"
				shape.projection = projectionTokens(tokens, index+1, sqliteparser.SQLiteParserFROM_)
			}
		case sqliteparser.SQLiteParserRETURNING_:
			if depth == 0 {
				shape.projection = projectionTokens(tokens, index+1, -1)
			}
		}
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserFROM_, sqliteparser.SQLiteParserJOIN_, sqliteparser.SQLiteParserINTO_, sqliteparser.SQLiteParserUPDATE_:
			if relation := identifierAfter(tokens, index); relation != "" {
				relations[relation] = struct{}{}
			}
		}
	}
	for relation := range relations {
		shape.relations = append(shape.relations, relation)
	}
	sort.Strings(shape.relations)
	return shape, nil
}

func normalizeSQLCExpressions(tokens []antlr.Token) (string, error) {
	var normalized strings.Builder
	for index := 0; index < len(tokens); {
		if index+3 < len(tokens) &&
			tokens[index].GetTokenType() == sqliteparser.SQLiteParserIDENTIFIER &&
			strings.EqualFold(tokens[index].GetText(), "sqlc") &&
			tokens[index+1].GetTokenType() == sqliteparser.SQLiteParserDOT &&
			tokens[index+2].GetTokenType() == sqliteparser.SQLiteParserIDENTIFIER &&
			sqlcExpressionFunction(tokens[index+2].GetText()) &&
			tokens[index+3].GetTokenType() == sqliteparser.SQLiteParserOPEN_PAR {
			depth := 0
			closed := false
			for index += 3; index < len(tokens); index++ {
				switch tokens[index].GetTokenType() {
				case sqliteparser.SQLiteParserOPEN_PAR:
					depth++
				case sqliteparser.SQLiteParserCLOSE_PAR:
					depth--
					if depth == 0 {
						index++
						closed = true
					}
				}
				if closed {
					break
				}
			}
			if !closed {
				return "", fmt.Errorf("unterminated sqlc expression")
			}
			normalized.WriteString("? ")
			continue
		}
		normalized.WriteString(tokens[index].GetText())
		normalized.WriteByte(' ')
		index++
	}
	return normalized.String(), nil
}

func sqlcExpressionFunction(name string) bool {
	switch strings.ToLower(name) {
	case "arg", "narg", "slice":
		return true
	default:
		return false
	}
}

func projectionTokens(tokens []antlr.Token, start int, stopType int) []string {
	var projection []string
	depth := 0
	for index := start; index < len(tokens); index++ {
		token := tokens[index]
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserOPEN_PAR:
			depth++
		case sqliteparser.SQLiteParserCLOSE_PAR:
			depth--
		}
		if depth == 0 && stopType >= 0 && token.GetTokenType() == stopType {
			break
		}
		if depth == 0 && token.GetTokenType() == sqliteparser.SQLiteParserSCOL {
			break
		}
		if token.GetTokenType() == sqliteparser.SQLiteParserBIND_PARAMETER {
			projection = append(projection, "?")
		} else {
			projection = append(projection, strings.ToLower(token.GetText()))
		}
	}
	return projection
}

func sqliteReferencedRelations(source string) (map[string]struct{}, error) {
	tokens, err := testharness.SQLiteTokens(source)
	if err != nil {
		return nil, err
	}
	references := make(map[string]struct{})
	for index, token := range tokens {
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserFROM_, sqliteparser.SQLiteParserJOIN_, sqliteparser.SQLiteParserINTO_:
			if relation := identifierAfter(tokens, index); relation != "" {
				references[relation] = struct{}{}
			}
		case sqliteparser.SQLiteParserUPDATE_:
			if relation := identifierAfter(tokens, index); relation != "" {
				references[relation] = struct{}{}
			}
		}
	}
	return references, nil
}

func identifierAfter(tokens []antlr.Token, index int) string {
	if index+1 >= len(tokens) {
		return ""
	}
	token := tokens[index+1]
	if token.GetTokenType() != sqliteparser.SQLiteParserIDENTIFIER {
		return ""
	}
	return normalizeSQLiteIdentifier(token.GetText())
}

func relationAfter(tokens []antlr.Token, index int, qualifier int) string {
	if index+1 >= len(tokens) || tokens[index+1].GetTokenType() != qualifier {
		return ""
	}
	return identifierAfter(tokens, index+1)
}

func normalizeSQLiteIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		switch {
		case value[0] == '"' && value[len(value)-1] == '"',
			value[0] == '`' && value[len(value)-1] == '`',
			value[0] == '[' && value[len(value)-1] == ']':
			value = value[1 : len(value)-1]
		}
	}
	return strings.ToLower(value)
}

func authorityMutationQueries(
	statements map[string]namedSQLStatement,
	authorityRelations map[string]struct{},
) map[string]struct{} {
	queries := make(map[string]struct{})
	for name, statement := range statements {
		if statement.shape.operation != "insert" &&
			statement.shape.operation != "update" &&
			statement.shape.operation != "delete" {
			continue
		}
		if _, authority := authorityRelations[statement.shape.target]; authority {
			queries[name] = struct{}{}
		}
	}
	return queries
}

func loadAuthorityWriterCalls(t *testing.T, repoRoot string, authorityQueries map[string]struct{}) []authorityWriterCall {
	t.Helper()
	pkgs := testharness.LoadTypedPackages(t, repoRoot, false, "./...")
	var calls []authorityWriterCall
	for _, pkg := range pkgs {
		if pkg.ForTest != "" || pkg.Module == nil || pkg.Module.Path != "core" {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				function, ok := pkg.TypesInfo.Uses[selector.Sel].(*types.Func)
				if !ok || function.Pkg() == nil || function.Pkg().Path() != "core/server/metadata/sqlitegen" {
					return true
				}
				if _, authority := authorityQueries[function.Name()]; !authority {
					return true
				}
				calls = append(calls, authorityWriterCall{
					packagePath: pkg.PkgPath,
					queryName:   function.Name(),
					position:    testharness.SourcePosition(pkg, selector.Sel.Pos()).String(),
				})
				return true
			})
		}
	}
	return calls
}

func analyzeAuthorityWriterCalls(calls []authorityWriterCall) []currentNodePersistenceFinding {
	var findings []currentNodePersistenceFinding
	for _, call := range calls {
		if call.packagePath == "core/server/workflowstore" {
			continue
		}
		findings = append(findings, currentNodePersistenceFinding{
			kind:   findingForeignAggregateWriter,
			detail: call.position + ": " + call.queryName + " is called from " + call.packagePath,
		})
	}
	sortPersistenceFindings(findings)
	return findings
}
