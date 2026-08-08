package metadata_test

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"

	testharness "core/internal/testharness/testsetup"

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
	operation           string
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
		operation, err := sqliteTriggerOperation(source)
		if err != nil {
			_ = triggerRows.Close()
			return persistenceSchemaModel{}, fmt.Errorf("parse trigger operation on %s: %w", tableName, err)
		}
		relation.triggers = append(relation.triggers, persistenceTrigger{
			operation:           operation,
			referencedRelations: references,
		})
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
		associationName, associationFindings := retainedSessionNodeProvenanceRelation(
			model,
			sessionForeignKey.targetTable,
			currentName,
		)
		analysis.findings = append(analysis.findings, associationFindings...)
		if associationName != "" && len(associationFindings) == 0 {
			analysis.authorityRelations[associationName] = struct{}{}
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

func retainedSessionNodeProvenanceRelation(
	model persistenceSchemaModel,
	sessionsName string,
	currentName string,
) (string, []currentNodePersistenceFinding) {
	var candidates []string
	for name, relation := range model.relations {
		_, hasNodeIdentity := relation.columns["node_id"]
		if name == currentName ||
			foreignKeyTo(relation, sessionsName) == nil ||
			foreignKeyTo(relation, "tasks") != nil ||
			(!hasNodeIdentity && len(primaryColumns(relation)) != 0) {
			continue
		}
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)
	if len(candidates) != 1 {
		return "", []currentNodePersistenceFinding{malformedPersistenceFinding(
			sessionsName,
			fmt.Sprintf("retained Session-node provenance relation count = %d, relations = %v", len(candidates), candidates),
		)}
	}

	const (
		nodeColumn   = "node_id"
		branchColumn = "transition_branch_key"
	)
	name := candidates[0]
	relation := model.relations[name]
	sessionForeignKey := foreignKeyTo(relation, sessionsName)
	node, hasNode := relation.columns[nodeColumn]
	branch, hasBranch := relation.columns[branchColumn]
	var findings []currentNodePersistenceFinding
	sessionColumn := ""
	if sessionForeignKey != nil && len(sessionForeignKey.localColumns) == 1 {
		sessionColumn = sessionForeignKey.localColumns[0]
	}
	if sessionForeignKey == nil ||
		sessionColumn == "" ||
		!relation.columns[sessionColumn].notNull {
		findings = append(findings, malformedPersistenceFinding(name, "retained Session-node provenance must have one direct non-null Session owner"))
	}
	if !hasNode || !node.notNull {
		findings = append(findings, malformedPersistenceFinding(name, "retained Session-node provenance must store one non-null node identity"))
	}
	if foreignKeyTo(relation, "workflow_nodes") != nil {
		findings = append(findings, malformedPersistenceFinding(name, "retained Session-node provenance must not own Workflow Node lifetime"))
	}
	primary := primaryColumns(relation)
	if len(primary) != 0 &&
		!reflect.DeepEqual(primary, []string{sessionColumn, nodeColumn}) &&
		!reflect.DeepEqual(primary, []string{sessionColumn, nodeColumn, branchColumn}) {
		findings = append(findings, malformedPersistenceFinding(name, "retained Session-node provenance must not use a surrogate primary key"))
	}
	if !hasBranch || branch.notNull ||
		sessionColumn == "" ||
		!hasUniquePartialIndex(relation, []string{sessionColumn, nodeColumn}) ||
		!hasUniquePartialIndex(relation, []string{sessionColumn, nodeColumn, branchColumn}) {
		findings = append(findings, malformedPersistenceFinding(name, "retained Session-node provenance must enforce serial and branch natural uniqueness"))
	}
	for _, operation := range []string{"insert", "update"} {
		if !hasOwnerValidationTrigger(relation, operation, sessionsName) {
			findings = append(findings, malformedPersistenceFinding(name, "retained Session-node provenance writes must validate Workflow Node and Task ownership"))
			break
		}
	}
	return name, findings
}

func hasOwnerValidationTrigger(relation *persistenceRelation, operation string, sessionsName string) bool {
	for _, trigger := range relation.triggers {
		if trigger.operation != operation {
			continue
		}
		references := trigger.referencedRelations
		if _, hasSessions := references[sessionsName]; !hasSessions {
			continue
		}
		if _, hasNodes := references["workflow_nodes"]; !hasNodes {
			continue
		}
		if _, hasTasks := references["task_records"]; hasTasks {
			return true
		}
	}
	return false
}

func sqliteTriggerOperation(source string) (string, error) {
	tokens, err := testharness.SQLiteTokens(source)
	if err != nil {
		return "", err
	}
	for _, token := range tokens {
		switch token.GetTokenType() {
		case sqliteparser.SQLiteParserINSERT_:
			return "insert", nil
		case sqliteparser.SQLiteParserUPDATE_:
			return "update", nil
		case sqliteparser.SQLiteParserDELETE_:
			return "delete", nil
		}
	}
	return "", fmt.Errorf("trigger write operation is missing")
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
