package metadata_test

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"
	"core/server/metadata"

	"github.com/antlr4-go/antlr/v4"
	"github.com/tursodatabase/libsql-client-go/sqliteparser"
)

type currentNodePersistenceFindingKind string

const (
	findingDuplicateCurrentStateRelation currentNodePersistenceFindingKind = "duplicate_current_state_relation"
	findingDuplicateExecutionDependency  currentNodePersistenceFindingKind = "duplicate_execution_dependency"
	findingMalformedCurrentStateGraph    currentNodePersistenceFindingKind = "malformed_current_state_graph"
	findingGeneratedQueryMismatch        currentNodePersistenceFindingKind = "generated_query_mismatch"
	findingForeignAggregateWriter        currentNodePersistenceFindingKind = "foreign_aggregate_writer"
)

type currentNodePersistenceFinding struct {
	kind   currentNodePersistenceFindingKind
	detail string
}

type persistenceSchemaModel struct {
	relations map[string]*persistenceRelation
}

type persistenceRelation struct {
	name        string
	columns     map[string]persistenceColumn
	foreignKeys []persistenceForeignKey
	indexes     []persistenceIndex
	triggers    []persistenceTrigger
}

type persistenceColumn struct {
	name     string
	notNull  bool
	primary  int
	position int
}

type persistenceForeignKey struct {
	targetTable   string
	localColumns  []string
	targetColumns []string
}

type persistenceIndex struct {
	unique  bool
	partial bool
	columns []string
}

type persistenceTrigger struct {
	referencedRelations map[string]struct{}
}

type persistenceAnalysis struct {
	findings           []currentNodePersistenceFinding
	authorityRelations map[string]struct{}
}

type namedSQLStatement struct {
	name   string
	source string
	shape  sqliteStatementShape
}

type sqliteStatementShape struct {
	operation  string
	target     string
	relations  []string
	projection []string
}

type authorityWriterCall struct {
	packagePath string
	queryName   string
	position    string
}

func TestCurrentNodePersistenceGraphHasOneAuthority(t *testing.T) {
	store, err := metadata.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	model, err := loadPersistenceSchemaModel(t.Context(), store.DB())
	if err != nil {
		t.Fatalf("load persistence schema model: %v", err)
	}
	analysis := analyzeCurrentNodePersistence(model)
	if len(analysis.findings) > 0 {
		t.Fatalf("Current Node persistence structure violations:\n%s", formatPersistenceFindings(analysis.findings))
	}
}

func TestCurrentNodeGeneratedQueriesMatchPersistenceGraph(t *testing.T) {
	store, err := metadata.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	model, err := loadPersistenceSchemaModel(t.Context(), store.DB())
	if err != nil {
		t.Fatalf("load persistence schema model: %v", err)
	}
	analysis := analyzeCurrentNodePersistence(model)
	if len(analysis.findings) > 0 {
		t.Fatalf("Current Node persistence structure violations:\n%s", formatPersistenceFindings(analysis.findings))
	}

	source, err := os.ReadFile("queries.sql")
	if err != nil {
		t.Fatalf("read queries.sql: %v", err)
	}
	sourceStatements, err := parseNamedSQLStatements(string(source))
	if err != nil {
		t.Fatalf("parse queries.sql: %v", err)
	}
	generatedStatements, generatedMethods, err := parseGeneratedSQLQueries(filepath.Join("sqlitegen", "queries.sql.go"))
	if err != nil {
		t.Fatalf("parse generated queries: %v", err)
	}
	queryFindings := compareGeneratedQueries(sourceStatements, generatedStatements, generatedMethods)

	authorityMutations := authorityMutationQueries(sourceStatements, analysis.authorityRelations)
	writerCalls := loadAuthorityWriterCalls(t, metadataRepositoryRoot(t), authorityMutations)
	queryFindings = append(queryFindings, analyzeAuthorityWriterCalls(writerCalls)...)
	if len(queryFindings) > 0 {
		t.Fatalf("Current Node generated-query structure violations:\n%s", formatPersistenceFindings(queryFindings))
	}
}

func TestCurrentNodePersistenceGuardRejectsDuplicateAuthorityFixtures(t *testing.T) {
	t.Run("second task node state relation and attempt dependency", func(t *testing.T) {
		model := baselinePersistenceFixture()
		model.relations["placement_state"] = &persistenceRelation{
			name: "placement_state",
			columns: map[string]persistenceColumn{
				"id":      {name: "id", primary: 1},
				"task_id": {name: "task_id"},
				"node_id": {name: "node_id"},
			},
			foreignKeys: []persistenceForeignKey{
				{targetTable: "tasks", localColumns: []string{"task_id"}, targetColumns: []string{"id"}},
				{targetTable: "workflow_nodes", localColumns: []string{"node_id"}, targetColumns: []string{"id"}},
			},
		}
		model.relations["execution_attempts"] = &persistenceRelation{
			name: "execution_attempts",
			columns: map[string]persistenceColumn{
				"id":           {name: "id", primary: 1},
				"placement_id": {name: "placement_id"},
			},
			foreignKeys: []persistenceForeignKey{{
				targetTable:   "placement_state",
				localColumns:  []string{"placement_id"},
				targetColumns: []string{"id"},
			}},
		}
		findings := analyzeCurrentNodePersistence(model).findings
		assertPersistenceFinding(t, findings, findingDuplicateCurrentStateRelation)
		assertPersistenceFinding(t, findings, findingDuplicateExecutionDependency)
	})

	t.Run("duplicate relation query mutation", func(t *testing.T) {
		queries := authorityMutationQueries(
			map[string]namedSQLStatement{
				"InsertPlacementState": {
					name:  "InsertPlacementState",
					shape: sqliteStatementShape{operation: "insert", target: "placement_state"},
				},
			},
			map[string]struct{}{"placement_state": {}},
		)
		if _, ok := queries["InsertPlacementState"]; !ok {
			t.Fatal("duplicate authority mutation was not classified")
		}
		calls := []authorityWriterCall{{
			packagePath: "core/server/rogue",
			queryName:   "InsertPlacementState",
			position:    "rogue.go:1",
		}}
		assertPersistenceFinding(t, analyzeAuthorityWriterCalls(calls), findingForeignAggregateWriter)
	})
}

func loadPersistenceSchemaModel(ctx context.Context, db *sql.DB) (persistenceSchemaModel, error) {
	model := persistenceSchemaModel{relations: make(map[string]*persistenceRelation)}
	rows, err := db.QueryContext(ctx, `
SELECT name
FROM sqlite_master
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
ORDER BY name`)
	if err != nil {
		return persistenceSchemaModel{}, err
	}
	var tableNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return persistenceSchemaModel{}, err
		}
		tableNames = append(tableNames, name)
	}
	if err := rows.Close(); err != nil {
		return persistenceSchemaModel{}, err
	}
	if err := rows.Err(); err != nil {
		return persistenceSchemaModel{}, err
	}

	for _, tableName := range tableNames {
		relation, err := loadPersistenceRelation(ctx, db, tableName)
		if err != nil {
			return persistenceSchemaModel{}, fmt.Errorf("load relation %s: %w", tableName, err)
		}
		model.relations[tableName] = relation
	}

	triggerRows, err := db.QueryContext(ctx, `
SELECT tbl_name, sql
FROM sqlite_master
WHERE type = 'trigger'
ORDER BY name`)
	if err != nil {
		return persistenceSchemaModel{}, err
	}
	for triggerRows.Next() {
		var tableName string
		var source string
		if err := triggerRows.Scan(&tableName, &source); err != nil {
			_ = triggerRows.Close()
			return persistenceSchemaModel{}, err
		}
		relation := model.relations[tableName]
		if relation == nil {
			_ = triggerRows.Close()
			return persistenceSchemaModel{}, fmt.Errorf("trigger owner relation %s is missing", tableName)
		}
		references, err := sqliteReferencedRelations(source)
		if err != nil {
			_ = triggerRows.Close()
			return persistenceSchemaModel{}, fmt.Errorf("parse trigger on %s: %w", tableName, err)
		}
		relation.triggers = append(relation.triggers, persistenceTrigger{referencedRelations: references})
	}
	if err := triggerRows.Close(); err != nil {
		return persistenceSchemaModel{}, err
	}
	if err := triggerRows.Err(); err != nil {
		return persistenceSchemaModel{}, err
	}
	return model, nil
}

func loadPersistenceRelation(ctx context.Context, db *sql.DB, tableName string) (*persistenceRelation, error) {
	relation := &persistenceRelation{
		name:    tableName,
		columns: make(map[string]persistenceColumn),
	}
	columnRows, err := db.QueryContext(ctx, `
SELECT cid, name, "notnull", pk
FROM pragma_table_info(?)
ORDER BY cid`, tableName)
	if err != nil {
		return nil, err
	}
	for columnRows.Next() {
		var column persistenceColumn
		var notNull int
		if err := columnRows.Scan(&column.position, &column.name, &notNull, &column.primary); err != nil {
			_ = columnRows.Close()
			return nil, err
		}
		column.notNull = notNull != 0
		relation.columns[column.name] = column
	}
	if err := columnRows.Close(); err != nil {
		return nil, err
	}
	if err := columnRows.Err(); err != nil {
		return nil, err
	}

	foreignKeyRows, err := db.QueryContext(ctx, `
SELECT id, seq, "table", "from", "to"
FROM pragma_foreign_key_list(?)
ORDER BY id, seq`, tableName)
	if err != nil {
		return nil, err
	}
	foreignKeys := make(map[int]*persistenceForeignKey)
	var foreignKeyOrder []int
	for foreignKeyRows.Next() {
		var id int
		var sequence int
		var targetTable string
		var localColumn string
		var targetColumn string
		if err := foreignKeyRows.Scan(&id, &sequence, &targetTable, &localColumn, &targetColumn); err != nil {
			_ = foreignKeyRows.Close()
			return nil, err
		}
		foreignKey := foreignKeys[id]
		if foreignKey == nil {
			foreignKey = &persistenceForeignKey{targetTable: targetTable}
			foreignKeys[id] = foreignKey
			foreignKeyOrder = append(foreignKeyOrder, id)
		}
		foreignKey.localColumns = append(foreignKey.localColumns, localColumn)
		foreignKey.targetColumns = append(foreignKey.targetColumns, targetColumn)
	}
	if err := foreignKeyRows.Close(); err != nil {
		return nil, err
	}
	if err := foreignKeyRows.Err(); err != nil {
		return nil, err
	}
	for _, id := range foreignKeyOrder {
		relation.foreignKeys = append(relation.foreignKeys, *foreignKeys[id])
	}

	indexRows, err := db.QueryContext(ctx, `
SELECT name, "unique", partial
FROM pragma_index_list(?)
ORDER BY seq`, tableName)
	if err != nil {
		return nil, err
	}
	for indexRows.Next() {
		var name string
		var unique int
		var partial int
		if err := indexRows.Scan(&name, &unique, &partial); err != nil {
			_ = indexRows.Close()
			return nil, err
		}
		index := persistenceIndex{unique: unique != 0, partial: partial != 0}
		columnIndexRows, err := db.QueryContext(ctx, `
SELECT name
FROM pragma_index_info(?)
ORDER BY seqno`, name)
		if err != nil {
			_ = indexRows.Close()
			return nil, err
		}
		for columnIndexRows.Next() {
			var columnName string
			if err := columnIndexRows.Scan(&columnName); err != nil {
				_ = columnIndexRows.Close()
				_ = indexRows.Close()
				return nil, err
			}
			index.columns = append(index.columns, columnName)
		}
		if err := columnIndexRows.Close(); err != nil {
			_ = indexRows.Close()
			return nil, err
		}
		if err := columnIndexRows.Err(); err != nil {
			_ = indexRows.Close()
			return nil, err
		}
		relation.indexes = append(relation.indexes, index)
	}
	if err := indexRows.Close(); err != nil {
		return nil, err
	}
	if err := indexRows.Err(); err != nil {
		return nil, err
	}
	return relation, nil
}

func analyzeCurrentNodePersistence(model persistenceSchemaModel) persistenceAnalysis {
	analysis := persistenceAnalysis{authorityRelations: make(map[string]struct{})}
	currentCandidates := relationsWithForeignTargets(model, "tasks", "workflow_nodes")
	if len(currentCandidates) != 1 {
		analysis.findings = append(analysis.findings, currentNodePersistenceFinding{
			kind:   findingDuplicateCurrentStateRelation,
			detail: fmt.Sprintf("task-to-node current-state relation count = %d, relations = %v", len(currentCandidates), currentCandidates),
		})
	}
	if len(currentCandidates) == 0 {
		return analysis
	}
	currentName := selectNaturalCurrentRelation(model, currentCandidates)
	current := model.relations[currentName]
	analysis.authorityRelations[currentName] = struct{}{}

	taskForeignKey := foreignKeyTo(current, "tasks")
	nodeForeignKey := foreignKeyTo(current, "workflow_nodes")
	if taskForeignKey == nil || nodeForeignKey == nil ||
		len(taskForeignKey.localColumns) != 1 ||
		len(nodeForeignKey.localColumns) != 1 ||
		len(primaryColumns(current)) != 0 {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(currentName, "Current Node relation must use natural task/node identity without a surrogate primary key"))
		return analysis
	}
	taskColumn := taskForeignKey.localColumns[0]
	branchForeignKeys := make([]persistenceForeignKey, 0)
	for _, foreignKey := range current.foreignKeys {
		if len(foreignKey.localColumns) == 2 && slicesContains(foreignKey.localColumns, taskColumn) {
			branchForeignKeys = append(branchForeignKeys, foreignKey)
		}
	}
	if len(branchForeignKeys) != 1 {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(currentName, "Current Node relation must have one composite branch dependency"))
		return analysis
	}
	branchForeignKey := branchForeignKeys[0]
	branchColumn := otherColumn(branchForeignKey.localColumns, taskColumn)
	if branchColumn == "" ||
		!hasUniquePartialIndex(current, []string{taskColumn}) ||
		!hasUniquePartialIndex(current, []string{taskColumn, branchColumn}) {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(currentName, "Current Node relation must enforce serial and branch aggregate uniqueness"))
	}

	branchRelation := model.relations[branchForeignKey.targetTable]
	if branchRelation == nil || !reflect.DeepEqual(primaryColumns(branchRelation), branchForeignKey.targetColumns) {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(currentName, "Current Node branch dependency must target one natural branch primary key"))
		return analysis
	}
	analysis.authorityRelations[branchRelation.name] = struct{}{}
	fanoutForeignKey := foreignKeyExcept(branchRelation, "tasks", "workflow_nodes", "sessions")
	if fanoutForeignKey == nil || len(fanoutForeignKey.localColumns) != 1 {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(branchRelation.name, "branch relation must depend on one Task-owned fan-out"))
		return analysis
	}
	fanoutRelation := model.relations[fanoutForeignKey.targetTable]
	if fanoutRelation == nil ||
		foreignKeyTo(fanoutRelation, "tasks") == nil ||
		len(primaryColumns(fanoutRelation)) != 1 {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(branchRelation.name, "fan-out owner must be keyed directly by Task"))
		return analysis
	}
	analysis.authorityRelations[fanoutRelation.name] = struct{}{}

	approvalCandidates := mutualTriggerRelations(model, currentName)
	delete(approvalCandidates, fanoutRelation.name)
	delete(approvalCandidates, branchRelation.name)
	for approvalName := range approvalCandidates {
		approval := model.relations[approvalName]
		if approval == nil || len(primaryColumns(approval)) != 1 || len(foreignKeyChildren(model, approvalName)) != 1 {
			delete(approvalCandidates, approvalName)
		}
	}
	if len(approvalCandidates) != 1 {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(
			currentName,
			fmt.Sprintf("pending Approval aggregate count = %d, relations = %v", len(approvalCandidates), sortedStringSet(approvalCandidates)),
		))
	} else {
		for approvalName := range approvalCandidates {
			children := foreignKeyChildren(model, approvalName)
			analysis.authorityRelations[approvalName] = struct{}{}
			analysis.authorityRelations[children[0]] = struct{}{}
		}
	}

	sessionForeignKey := foreignKeyExcept(current, "tasks", "workflow_nodes", branchRelation.name)
	if sessionForeignKey == nil || len(sessionForeignKey.localColumns) != 1 {
		analysis.findings = append(analysis.findings, malformedPersistenceFinding(currentName, "Agent Current Nodes must bind directly to nullable Sessions"))
	} else {
		sessions := model.relations[sessionForeignKey.targetTable]
		taskOwner := foreignKeyTo(sessions, "tasks")
		if sessions == nil || taskOwner == nil || len(taskOwner.localColumns) != 1 || sessions.columns[taskOwner.localColumns[0]].notNull {
			analysis.findings = append(analysis.findings, malformedPersistenceFinding(sessionForeignKey.targetTable, "Sessions must own an optional direct Task relation"))
		}
		associationCandidates := relationsWithForeignTargets(model, sessionForeignKey.targetTable, "workflow_nodes")
		filtered := associationCandidates[:0]
		for _, candidate := range associationCandidates {
			if foreignKeyTo(model.relations[candidate], "tasks") == nil {
				filtered = append(filtered, candidate)
			}
		}
		if len(filtered) != 1 || len(primaryColumns(model.relations[filtered[0]])) != 0 {
			analysis.findings = append(analysis.findings, malformedPersistenceFinding(sessionForeignKey.targetTable, "one natural Session-to-Workflow-Node association is required"))
		}
	}

	for relationName, relation := range model.relations {
		if _, authority := analysis.authorityRelations[relationName]; authority {
			continue
		}
		for _, foreignKey := range relation.foreignKeys {
			if slicesContains(currentCandidates, foreignKey.targetTable) {
				analysis.findings = append(analysis.findings, currentNodePersistenceFinding{
					kind:   findingDuplicateExecutionDependency,
					detail: relationName + " depends on task/node execution state " + foreignKey.targetTable,
				})
			}
		}
	}
	sortPersistenceFindings(analysis.findings)
	return analysis
}

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

func relationsWithForeignTargets(model persistenceSchemaModel, targets ...string) []string {
	var relations []string
	for name, relation := range model.relations {
		matches := true
		for _, target := range targets {
			if foreignKeyTo(relation, target) == nil {
				matches = false
				break
			}
		}
		if matches {
			relations = append(relations, name)
		}
	}
	sort.Strings(relations)
	return relations
}

func selectNaturalCurrentRelation(model persistenceSchemaModel, candidates []string) string {
	for _, candidate := range candidates {
		if len(primaryColumns(model.relations[candidate])) == 0 {
			return candidate
		}
	}
	return candidates[0]
}

func foreignKeyTo(relation *persistenceRelation, target string) *persistenceForeignKey {
	if relation == nil {
		return nil
	}
	for index := range relation.foreignKeys {
		if relation.foreignKeys[index].targetTable == target {
			return &relation.foreignKeys[index]
		}
	}
	return nil
}

func foreignKeyExcept(relation *persistenceRelation, excluded ...string) *persistenceForeignKey {
	if relation == nil {
		return nil
	}
	for index := range relation.foreignKeys {
		if !slicesContains(excluded, relation.foreignKeys[index].targetTable) {
			return &relation.foreignKeys[index]
		}
	}
	return nil
}

func primaryColumns(relation *persistenceRelation) []string {
	if relation == nil {
		return nil
	}
	columns := make([]persistenceColumn, 0)
	for _, column := range relation.columns {
		if column.primary > 0 {
			columns = append(columns, column)
		}
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].primary < columns[j].primary })
	out := make([]string, 0, len(columns))
	for _, column := range columns {
		out = append(out, column.name)
	}
	return out
}

func hasUniquePartialIndex(relation *persistenceRelation, columns []string) bool {
	for _, index := range relation.indexes {
		if index.unique && index.partial && reflect.DeepEqual(index.columns, columns) {
			return true
		}
	}
	return false
}

func mutualTriggerRelations(model persistenceSchemaModel, relationName string) map[string]struct{} {
	out := make(map[string]struct{})
	relation := model.relations[relationName]
	if relation == nil {
		return out
	}
	references := triggerReferences(relation)
	for referenced := range references {
		target := model.relations[referenced]
		if target == nil {
			continue
		}
		if _, mutual := triggerReferences(target)[relationName]; mutual {
			out[referenced] = struct{}{}
		}
	}
	return out
}

func triggerReferences(relation *persistenceRelation) map[string]struct{} {
	out := make(map[string]struct{})
	if relation == nil {
		return out
	}
	for _, trigger := range relation.triggers {
		for referenced := range trigger.referencedRelations {
			out[referenced] = struct{}{}
		}
	}
	return out
}

func foreignKeyChildren(model persistenceSchemaModel, parent string) []string {
	var children []string
	for name, relation := range model.relations {
		if foreignKeyTo(relation, parent) != nil {
			children = append(children, name)
		}
	}
	sort.Strings(children)
	return children
}

func otherColumn(columns []string, known string) string {
	for _, column := range columns {
		if column != known {
			return column
		}
	}
	return ""
}

func slicesContains[T comparable](values []T, want T) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func malformedPersistenceFinding(relation, detail string) currentNodePersistenceFinding {
	return currentNodePersistenceFinding{
		kind:   findingMalformedCurrentStateGraph,
		detail: relation + ": " + detail,
	}
}

func queryMismatchFinding(name, detail string) currentNodePersistenceFinding {
	return currentNodePersistenceFinding{
		kind:   findingGeneratedQueryMismatch,
		detail: name + ": " + detail,
	}
}

func sortPersistenceFindings(findings []currentNodePersistenceFinding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].kind != findings[j].kind {
			return findings[i].kind < findings[j].kind
		}
		return findings[i].detail < findings[j].detail
	})
}

func formatPersistenceFindings(findings []currentNodePersistenceFinding) string {
	lines := make([]string, 0, len(findings))
	for _, finding := range findings {
		lines = append(lines, string(finding.kind)+": "+finding.detail)
	}
	return strings.Join(lines, "\n")
}

func assertPersistenceFinding(t *testing.T, findings []currentNodePersistenceFinding, want currentNodePersistenceFindingKind) {
	t.Helper()
	for _, finding := range findings {
		if finding.kind == want {
			return
		}
	}
	t.Fatalf("findings =\n%s\nwant category %s", formatPersistenceFindings(findings), want)
}

func metadataRepositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s: %v", root, err)
	}
	return root
}

func baselinePersistenceFixture() persistenceSchemaModel {
	model := persistenceSchemaModel{relations: make(map[string]*persistenceRelation)}
	model.relations["tasks"] = &persistenceRelation{
		name:    "tasks",
		columns: map[string]persistenceColumn{"id": {name: "id", primary: 1}},
	}
	model.relations["workflow_nodes"] = &persistenceRelation{
		name:    "workflow_nodes",
		columns: map[string]persistenceColumn{"id": {name: "id", primary: 1}},
	}
	model.relations["sessions"] = &persistenceRelation{
		name: "sessions",
		columns: map[string]persistenceColumn{
			"id":      {name: "id", primary: 1},
			"task_id": {name: "task_id"},
		},
		foreignKeys: []persistenceForeignKey{{
			targetTable:   "tasks",
			localColumns:  []string{"task_id"},
			targetColumns: []string{"id"},
		}},
	}
	model.relations["parallel_owner"] = &persistenceRelation{
		name: "parallel_owner",
		columns: map[string]persistenceColumn{
			"task_id": {name: "task_id", primary: 1},
		},
		foreignKeys: []persistenceForeignKey{{
			targetTable:   "tasks",
			localColumns:  []string{"task_id"},
			targetColumns: []string{"id"},
		}},
		triggers: []persistenceTrigger{{referencedRelations: map[string]struct{}{"current_state": {}}}},
	}
	model.relations["parallel_branch"] = &persistenceRelation{
		name: "parallel_branch",
		columns: map[string]persistenceColumn{
			"task_id":    {name: "task_id", primary: 1},
			"branch_key": {name: "branch_key", primary: 2},
		},
		foreignKeys: []persistenceForeignKey{{
			targetTable:   "parallel_owner",
			localColumns:  []string{"task_id"},
			targetColumns: []string{"task_id"},
		}},
	}
	model.relations["current_state"] = &persistenceRelation{
		name: "current_state",
		columns: map[string]persistenceColumn{
			"task_id":    {name: "task_id"},
			"node_id":    {name: "node_id"},
			"branch_key": {name: "branch_key"},
			"session_id": {name: "session_id"},
		},
		foreignKeys: []persistenceForeignKey{
			{targetTable: "tasks", localColumns: []string{"task_id"}, targetColumns: []string{"id"}},
			{targetTable: "workflow_nodes", localColumns: []string{"node_id"}, targetColumns: []string{"id"}},
			{targetTable: "sessions", localColumns: []string{"session_id"}, targetColumns: []string{"id"}},
			{targetTable: "parallel_branch", localColumns: []string{"task_id", "branch_key"}, targetColumns: []string{"task_id", "branch_key"}},
		},
		indexes: []persistenceIndex{
			{unique: true, partial: true, columns: []string{"task_id"}},
			{unique: true, partial: true, columns: []string{"task_id", "branch_key"}},
		},
		triggers: []persistenceTrigger{{referencedRelations: map[string]struct{}{
			"parallel_owner": {},
			"approval":       {},
		}}},
	}
	model.relations["approval"] = &persistenceRelation{
		name:    "approval",
		columns: map[string]persistenceColumn{"id": {name: "id", primary: 1}},
		triggers: []persistenceTrigger{{referencedRelations: map[string]struct{}{
			"current_state": {},
		}}},
	}
	model.relations["approval_branch"] = &persistenceRelation{
		name: "approval_branch",
		columns: map[string]persistenceColumn{
			"approval_id": {name: "approval_id", primary: 1},
			"branch_key":  {name: "branch_key", primary: 2},
		},
		foreignKeys: []persistenceForeignKey{{
			targetTable:   "approval",
			localColumns:  []string{"approval_id"},
			targetColumns: []string{"id"},
		}},
	}
	model.relations["session_node_association"] = &persistenceRelation{
		name: "session_node_association",
		columns: map[string]persistenceColumn{
			"session_id": {name: "session_id"},
			"node_id":    {name: "node_id"},
			"branch_key": {name: "branch_key"},
		},
		foreignKeys: []persistenceForeignKey{
			{targetTable: "sessions", localColumns: []string{"session_id"}, targetColumns: []string{"id"}},
			{targetTable: "workflow_nodes", localColumns: []string{"node_id"}, targetColumns: []string{"id"}},
		},
	}
	return model
}
