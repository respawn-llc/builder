package metadata_test

import (
	"strings"
	"testing"
)

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

}

func TestCurrentNodePersistenceGuardRejectsInvalidSessionProvenanceFixtures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*persistenceRelation)
	}{
		{
			name: "missing node identity",
			mutate: func(relation *persistenceRelation) {
				delete(relation.columns, "node_id")
			},
		},
		{
			name: "missing owner validation",
			mutate: func(relation *persistenceRelation) {
				relation.triggers = nil
			},
		},
		{
			name: "surrogate primary key",
			mutate: func(relation *persistenceRelation) {
				relation.columns["id"] = persistenceColumn{name: "id", primary: 1}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := baselinePersistenceFixture()
			test.mutate(model.relations["session_node_association"])
			assertPersistenceFinding(
				t,
				analyzeCurrentNodePersistence(model).findings,
				findingMalformedCurrentStateGraph,
			)
		})
	}
}

func TestCurrentNodePersistenceGuardAcceptsNaturalSessionProvenancePrimaryKey(t *testing.T) {
	t.Parallel()

	model := baselinePersistenceFixture()
	association := model.relations["session_node_association"]
	sessionColumn := association.columns["session_id"]
	sessionColumn.primary = 1
	association.columns["session_id"] = sessionColumn
	nodeColumn := association.columns["node_id"]
	nodeColumn.primary = 2
	association.columns["node_id"] = nodeColumn

	if findings := analyzeCurrentNodePersistence(model).findings; len(findings) != 0 {
		t.Fatalf("natural Session-node provenance identity was rejected:\n%s", formatPersistenceFindings(findings))
	}
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
			"task_id":               {name: "task_id", notNull: true},
			"session_id":            {name: "session_id", notNull: true},
			"node_id":               {name: "node_id", notNull: true},
			"transition_branch_key": {name: "transition_branch_key"},
			"association_status":    {name: "association_status", notNull: true},
			"source_session_id":     {name: "source_session_id"},
		},
		foreignKeys: []persistenceForeignKey{
			{
				targetTable:   "tasks",
				localColumns:  []string{"task_id"},
				targetColumns: []string{"id"},
			},
			{
				targetTable:   "sessions",
				localColumns:  []string{"session_id"},
				targetColumns: []string{"id"},
			},
			{
				targetTable:   "sessions",
				localColumns:  []string{"source_session_id"},
				targetColumns: []string{"id"},
			},
		},
		indexes: []persistenceIndex{
			{unique: true, partial: true, columns: []string{"session_id", "node_id"}},
			{unique: true, partial: true, columns: []string{"session_id", "node_id", "transition_branch_key"}},
			{unique: true, partial: true, columns: []string{"task_id", "node_id"}},
			{unique: true, partial: true, columns: []string{"task_id", "node_id", "transition_branch_key"}},
		},
		triggers: []persistenceTrigger{
			{
				operation: "insert",
				referencedRelations: map[string]struct{}{
					"sessions":       {},
					"task_records":   {},
					"workflow_nodes": {},
				},
			},
			{
				operation: "update",
				referencedRelations: map[string]struct{}{
					"sessions":       {},
					"task_records":   {},
					"workflow_nodes": {},
				},
			},
		},
	}
	return model
}
