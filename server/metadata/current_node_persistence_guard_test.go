package metadata_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/server/metadata"
)

func TestCurrentNodePersistenceGraphHasOneAuthority(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
