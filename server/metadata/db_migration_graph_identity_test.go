package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	metadatamigrations "core/server/metadata/migrations"
	"core/shared/runtimeids"

	"github.com/pressly/goose/v3"
	goosedatabase "github.com/pressly/goose/v3/database"
)

const (
	graphMigrationWorkflowID = "761eb776-1cad-4868-a026-e66e64c9e983"

	graphMigrationCanonicalGroup      = "11111111-1111-4111-8111-111111111111"
	graphMigrationCanonicalNode       = "22222222-2222-4222-8222-222222222222"
	graphMigrationCanonicalTransition = "33333333-3333-4333-8333-333333333333"
	graphMigrationCanonicalEdge       = "44444444-4444-4444-8444-444444444444"

	graphMigrationLegacyGroup      = "group-prefixed"
	graphMigrationLegacyNode       = "node-prefixed"
	graphMigrationLegacyTransition = "transition-prefixed"
	graphMigrationLegacyEdge       = "edge-prefixed"
)

func TestGraphIdentityMigrationConvertsCompleteVersion79FixtureAtomically(t *testing.T) {
	t.Parallel()
	db := openGraphIdentityVersion79Fixture(t)
	seedGraphIdentityMigrationFixture(t, db)

	beforeOrder := graphMigrationOrder(t, db)
	beforeJoinProviders := graphMigrationJSONStrings(t, db, `
SELECT value
FROM workflow_nodes, json_each(workflow_nodes.join_input_providers_json)
WHERE node_key = 'join'
ORDER BY json_each.key`)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 84); err != nil {
		t.Fatalf("apply graph identity migration: %v", err)
	}

	assertGraphMigrationVersion(t, provider, 84)
	assertGraphMigrationStorage(t, db)
	var workflowVersion int64
	if err := db.QueryRow(`SELECT version FROM workflows WHERE id = ?`, graphMigrationWorkflowBlob(t)).Scan(&workflowVersion); err != nil {
		t.Fatalf("read migrated Workflow Version: %v", err)
	}
	if workflowVersion != 2 {
		t.Fatalf("migrated Workflow Version = %d, want 2 after legacy identity remap", workflowVersion)
	}
	if got := graphMigrationOrder(t, db); !equalGraphMigrationStrings(got, beforeOrder) {
		t.Fatalf("graph order after migration = %v, want %v", got, beforeOrder)
	}

	canonicalGroup := graphMigrationMappedText(t, db, "workflow_node_groups", "group_key", "canonical")
	if canonicalGroup != graphMigrationCanonicalGroup {
		t.Fatalf("canonical Node Group = %q, want unchanged %q", canonicalGroup, graphMigrationCanonicalGroup)
	}
	canonicalNode := graphMigrationMappedText(t, db, "workflow_nodes", "node_key", "canonical")
	if canonicalNode != graphMigrationCanonicalNode {
		t.Fatalf("canonical Node = %q, want unchanged %q", canonicalNode, graphMigrationCanonicalNode)
	}
	canonicalTransition := graphMigrationMappedText(t, db, "workflow_transition_groups", "transition_id", "canonical")
	if canonicalTransition != graphMigrationCanonicalTransition {
		t.Fatalf("canonical Transition = %q, want unchanged %q", canonicalTransition, graphMigrationCanonicalTransition)
	}
	canonicalEdge := graphMigrationMappedText(t, db, "workflow_edges", "edge_key", "canonical")
	if canonicalEdge != graphMigrationCanonicalEdge {
		t.Fatalf("canonical Transition Branch = %q, want unchanged %q", canonicalEdge, graphMigrationCanonicalEdge)
	}

	legacyGroup := graphMigrationMappedText(t, db, "workflow_node_groups", "group_key", "legacy")
	legacyNode := graphMigrationMappedText(t, db, "workflow_nodes", "node_key", "legacy")
	legacyTransition := graphMigrationMappedText(t, db, "workflow_transition_groups", "transition_id", "legacy")
	legacyEdge := graphMigrationMappedText(t, db, "workflow_edges", "edge_key", "legacy")
	for kind, id := range map[string]string{
		"Node Group":        legacyGroup,
		"Node":              legacyNode,
		"Transition":        legacyTransition,
		"Transition Branch": legacyEdge,
	} {
		if _, err := runtimeids.GraphEntityIDBlob(id); err != nil {
			t.Fatalf("migrated legacy %s %q is not canonical UUIDv4: %v", kind, id, err)
		}
	}

	var groupID []byte
	if err := db.QueryRow(`SELECT group_id FROM workflow_nodes WHERE node_key = 'legacy'`).Scan(&groupID); err != nil {
		t.Fatalf("read migrated Node Group membership: %v", err)
	}
	assertGraphMigrationBlobText(t, groupID, legacyGroup, "Node Group membership")
	var absentGroupID []byte
	if err := db.QueryRow(`SELECT group_id FROM workflow_nodes WHERE node_key = 'canonical'`).Scan(&absentGroupID); !errors.Is(err, sql.ErrNoRows) && err != nil {
		t.Fatalf("read absent Node Group membership: %v", err)
	}
	var nullCount int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_nodes WHERE node_key = 'canonical' AND group_id IS NULL`).Scan(&nullCount); err != nil {
		t.Fatalf("count SQL NULL membership: %v", err)
	}
	if nullCount != 1 {
		t.Fatalf("SQL NULL Node Group memberships = %d, want 1", nullCount)
	}

	var currentNode, enteredEdge, approvalNode, sessionNode []byte
	if err := db.QueryRow(`SELECT node_id, entered_by_edge_id FROM task_current_nodes WHERE task_id = 'task-graph-migration'`).Scan(&currentNode, &enteredEdge); err != nil {
		t.Fatalf("read migrated Current Node: %v", err)
	}
	if err := db.QueryRow(`SELECT source_node_id FROM task_pending_approvals WHERE id = 'approval-graph-migration'`).Scan(&approvalNode); err != nil {
		t.Fatalf("read migrated pending Approval: %v", err)
	}
	if err := db.QueryRow(`SELECT node_id FROM session_workflow_node_associations WHERE session_id = 'session-graph-migration'`).Scan(&sessionNode); err != nil {
		t.Fatalf("read migrated historical Session association: %v", err)
	}
	for location, raw := range map[string][]byte{
		"Current Node":                   currentNode,
		"Current Node entering Branch":   enteredEdge,
		"pending Approval source Node":   approvalNode,
		"historical Session association": sessionNode,
	} {
		want := legacyNode
		if location == "Current Node entering Branch" {
			want = legacyEdge
		}
		assertGraphMigrationBlobText(t, raw, want, location)
	}

	var joinProviders string
	if err := db.QueryRow(`SELECT join_input_providers_json FROM workflow_nodes WHERE node_key = 'join'`).Scan(&joinProviders); err != nil {
		t.Fatalf("read migrated Join providers: %v", err)
	}
	var providers []struct {
		InputName      string `json:"input_name"`
		ProviderEdgeID string `json:"provider_edge_id"`
	}
	if err := json.Unmarshal([]byte(joinProviders), &providers); err != nil {
		t.Fatalf("decode migrated Join providers: %v", err)
	}
	if len(providers) != 2 || providers[0].ProviderEdgeID != legacyEdge || providers[1].ProviderEdgeID != legacyEdge {
		t.Fatalf("migrated Join providers = %+v, want repeated mapped Branch %q", providers, legacyEdge)
	}
	afterJoinProviders := graphMigrationJSONStrings(t, db, `
SELECT value
FROM workflow_nodes, json_each(workflow_nodes.join_input_providers_json)
WHERE node_key = 'join'
ORDER BY json_each.key`)
	if len(afterJoinProviders) != len(beforeJoinProviders) {
		t.Fatalf("Join provider order length = %d, want %d", len(afterJoinProviders), len(beforeJoinProviders))
	}
	for index := range afterJoinProviders {
		var before, after map[string]any
		if err := json.Unmarshal([]byte(beforeJoinProviders[index]), &before); err != nil {
			t.Fatalf("decode before Join provider %d: %v", index, err)
		}
		if err := json.Unmarshal([]byte(afterJoinProviders[index]), &after); err != nil {
			t.Fatalf("decode after Join provider %d: %v", index, err)
		}
		if before["input_name"] != after["input_name"] {
			t.Fatalf("Join provider order changed at %d: before=%v after=%v", index, before, after)
		}
	}

	var transitionSnapshot, targetSnapshot, edgeSnapshot string
	if err := db.QueryRow(`
SELECT approval.transition_snapshot_json, branch.target_snapshot_json, branch.effective_edge_configuration_json
FROM task_pending_approvals approval
JOIN task_pending_approval_branches branch ON branch.approval_id = approval.id
WHERE approval.id = 'approval-graph-migration'`).Scan(&transitionSnapshot, &targetSnapshot, &edgeSnapshot); err != nil {
		t.Fatalf("read migrated Approval snapshots: %v", err)
	}
	assertGraphMigrationJSONIdentity(t, transitionSnapshot, "$.id", legacyTransition)
	assertGraphMigrationJSONIdentity(t, transitionSnapshot, "$.source_node_id", legacyNode)
	assertGraphMigrationJSONIdentity(t, targetSnapshot, "$.node_id", legacyNode)
	assertGraphMigrationJSONIdentity(t, targetSnapshot, "$.entered_by_edge_id", legacyEdge)
	assertGraphMigrationJSONIdentity(t, edgeSnapshot, "$.id", legacyEdge)
	assertGraphMigrationJSONIdentity(t, edgeSnapshot, "$.transition_group_id", legacyTransition)
	assertGraphMigrationJSONIdentity(t, edgeSnapshot, "$.target_node_id", legacyNode)

	var nodeIDsJSON string
	if err := db.QueryRow(`SELECT node_ids_json FROM workflow_task_status_records WHERE task_id = 'task-graph-migration'`).Scan(&nodeIDsJSON); err != nil {
		t.Fatalf("read migrated task status Node identities: %v", err)
	}
	var statusNodeIDs []string
	if err := json.Unmarshal([]byte(nodeIDsJSON), &statusNodeIDs); err != nil {
		t.Fatalf("decode migrated task status Node identities: %v", err)
	}
	if len(statusNodeIDs) != 1 || statusNodeIDs[0] != legacyNode {
		t.Fatalf("task status Node identities = %v, want [%s]", statusNodeIDs, legacyNode)
	}

	var foreignKeyViolations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatalf("foreign-key check: %v", err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("foreign-key violations = %d, want 0", foreignKeyViolations)
	}
	if _, err := db.Exec(`DELETE FROM workflow_nodes WHERE node_key = 'legacy'`); err == nil {
		t.Fatal("migrated pending-Approval Node deletion blocker did not fire")
	}
}

func TestGraphIdentityMigrationVersionsWorkflowForJoinOnlyBranchRemap(t *testing.T) {
	t.Parallel()
	db := openGraphIdentityVersion79Fixture(t)
	workflowID := graphMigrationWorkflowBlob(t)
	execGraphMigrationSeed(t, db, `INSERT INTO workflows (
		id, name, description, version, execution_target_policy,
		created_at_unix_ms, updated_at_unix_ms
	) VALUES (?, 'Join-only remap', '', 1, 'head', 1, 1)`, workflowID)
	execGraphMigrationSeed(t, db, `INSERT INTO workflow_nodes (
		id, workflow_id, node_key, kind, display_name, subagent_role, group_id,
		sort_order, join_input_providers_json, completion_mode, script_path
	) VALUES (?, ?, 'join', 'join', 'Join', '', NULL, 0,
		'[{"input_name":"result","provider_edge_id":"dangling-legacy-edge"}]', '', NULL)`,
		graphMigrationCanonicalNode,
		workflowID,
	)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 84); err != nil {
		t.Fatalf("apply graph identity migration: %v", err)
	}

	var workflowVersion int64
	if err := db.QueryRow(`SELECT version FROM workflows WHERE id = ?`, workflowID).Scan(&workflowVersion); err != nil {
		t.Fatalf("read migrated Workflow Version: %v", err)
	}
	if workflowVersion != 2 {
		t.Fatalf("migrated Workflow Version = %d, want 2 after Join-only Branch remap", workflowVersion)
	}
	var providerEdgeID string
	if err := db.QueryRow(`
SELECT json_extract(provider.value, '$.provider_edge_id')
FROM workflow_nodes nodes, json_each(nodes.join_input_providers_json) provider
WHERE nodes.node_key = 'join'`).Scan(&providerEdgeID); err != nil {
		t.Fatalf("read migrated Join provider identity: %v", err)
	}
	if _, err := runtimeids.GraphEntityIDBlob(providerEdgeID); err != nil {
		t.Fatalf("migrated Join provider identity %q is not canonical UUIDv4: %v", providerEdgeID, err)
	}
}

func TestGraphIdentityMigrationRejectsInvalidVersion79IdentitiesWithoutMutation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed func(*testing.T, *sql.DB)
	}{
		{
			name: "blank",
			seed: func(t *testing.T, db *sql.DB) {
				execGraphMigrationSeed(t, db, `INSERT INTO workflow_node_groups (
					id, workflow_id, group_key, display_name, sort_order
				) VALUES ('', ?, 'blank', 'Blank', 100)`, graphMigrationWorkflowBlob(t))
			},
		},
		{
			name: "zero",
			seed: func(t *testing.T, db *sql.DB) {
				execGraphMigrationSeed(t, db, `INSERT INTO workflow_node_groups (
					id, workflow_id, group_key, display_name, sort_order
				) VALUES ('00000000-0000-0000-0000-000000000000', ?, 'zero', 'Zero', 100)`, graphMigrationWorkflowBlob(t))
			},
		},
		{
			name: "broken required relationship",
			seed: func(t *testing.T, db *sql.DB) {
				db.SetMaxOpenConns(1)
				execGraphMigrationSeed(t, db, `PRAGMA foreign_keys = OFF`)
				execGraphMigrationSeed(t, db, `INSERT INTO workflow_transition_groups (
					id, source_node_id, transition_id, display_name, sort_order, description
				) VALUES ('orphan-transition', 'missing-node', 'orphan', 'Orphan', 100, '')`)
				execGraphMigrationSeed(t, db, `PRAGMA foreign_keys = ON`)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			db := openGraphIdentityVersion79Fixture(t)
			seedGraphIdentityMigrationFixture(t, db)
			test.seed(t, db)

			provider, err := newMetadataMigrationProvider(db)
			if err != nil {
				t.Fatalf("create migration provider: %v", err)
			}
			if _, err := provider.UpTo(t.Context(), 84); err == nil {
				t.Fatal("invalid graph identity migration succeeded")
			}

			assertGraphMigrationVersion(t, provider, 83)
			assertGraphMigrationVersion79State(t, db)
		})
	}
}

func TestGraphIdentityMigrationRollsBackWhenVersionRecordingFails(t *testing.T) {
	t.Parallel()
	db := openGraphIdentityVersion79Fixture(t)
	seedGraphIdentityMigrationFixture(t, db)

	store, err := goosedatabase.NewStore(goosedatabase.DialectSQLite3, goose.DefaultTablename)
	if err != nil {
		t.Fatalf("create SQLite migration store: %v", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectCustom,
		db,
		metadatamigrations.FS,
		goose.WithStore(graphMigrationFailVersion83Store{Store: store}),
		goose.WithLogger(goose.NopLogger()),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("create failing migration provider: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 84); err == nil {
		t.Fatal("graph identity migration succeeded despite version-record failure")
	}

	assertGraphMigrationVersion(t, provider, 82)
	assertGraphMigrationVersion79State(t, db)
}

type graphMigrationFailVersion83Store struct {
	goosedatabase.Store
}

func (store graphMigrationFailVersion83Store) Insert(
	ctx context.Context,
	db goosedatabase.DBTxConn,
	request goosedatabase.InsertRequest,
) error {
	if request.Version == 83 {
		return errors.New("forced graph identity migration version-record failure")
	}
	return store.Store.Insert(ctx, db, request)
}

func openGraphIdentityVersion79Fixture(t *testing.T) *sql.DB {
	t.Helper()
	root := t.TempDir()
	db, err := openDatabaseAtVersionForTest(t, root, filepath.Join(root, "db", "main.sqlite3"), 79)
	if err != nil {
		t.Fatalf("open version 79 database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedGraphIdentityMigrationFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	workflowID := graphMigrationWorkflowBlob(t)
	statements := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "project",
			sql: `INSERT INTO projects (
				id, display_name, project_key, next_task_seq, created_at_unix_ms, updated_at_unix_ms, metadata_json
			) VALUES ('project-graph-migration', 'Graph migration', 'GRF', 2, 1, 1, '{}')`,
		},
		{
			name: "workspace",
			sql: `INSERT INTO workspaces (
				id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
			) VALUES ('workspace-graph-migration', 'project-graph-migration', '/graph-migration', '{}', 1, 1)`,
		},
		{
			name: "workflow",
			sql: `INSERT INTO workflows (
				id, name, description, version, execution_target_policy,
				created_at_unix_ms, updated_at_unix_ms
			) VALUES (?, 'Graph migration', '', 1, 'head', 1, 1)`,
			args: []any{workflowID},
		},
		{
			name: "link",
			sql: `INSERT INTO project_workflow_links (
				id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms
			) VALUES ('link-graph-migration', 'project-graph-migration', ?, 1, 1)`,
			args: []any{workflowID},
		},
		{
			name: "groups",
			sql: `INSERT INTO workflow_node_groups (
				id, workflow_id, group_key, display_name, sort_order
			) VALUES
				(?, ?, 'legacy', 'Legacy', 10),
				(?, ?, 'canonical', 'Canonical', 10)`,
			args: []any{
				graphMigrationLegacyGroup, workflowID,
				graphMigrationCanonicalGroup, workflowID,
			},
		},
		{
			name: "nodes",
			sql: `INSERT INTO workflow_nodes (
				id, workflow_id, node_key, kind, display_name, subagent_role, group_id,
				sort_order, join_input_providers_json, completion_mode, script_path
			) VALUES
				(?, ?, 'legacy', 'start', 'Legacy', '', ?, 20, '[]', '', NULL),
				(?, ?, 'canonical', 'agent', 'Canonical', 'default', NULL, 20, '[]', 'tool', NULL),
				('node-join-legacy', ?, 'join', 'join', 'Join', '', NULL, 30,
				 '[{"input_name":"first","provider_edge_id":"edge-prefixed"},{"input_name":"second","provider_edge_id":"edge-prefixed"}]',
				 '', NULL),
				('node-terminal-legacy', ?, 'terminal', 'terminal', 'Terminal', '', NULL, 40, '[]', '', NULL)`,
			args: []any{
				graphMigrationLegacyNode, workflowID, graphMigrationLegacyGroup,
				graphMigrationCanonicalNode, workflowID,
				workflowID,
				workflowID,
			},
		},
		{
			name: "transitions",
			sql: `INSERT INTO workflow_transition_groups (
				id, source_node_id, transition_id, display_name, sort_order, description
			) VALUES
				(?, ?, 'legacy', 'Legacy', 30, ''),
				(?, ?, 'canonical', 'Canonical', 30, '')`,
			args: []any{
				graphMigrationLegacyTransition, graphMigrationLegacyNode,
				graphMigrationCanonicalTransition, graphMigrationCanonicalNode,
			},
		},
		{
			name: "edges",
			sql: `INSERT INTO workflow_edges (
				id, transition_group_id, edge_key, target_node_id, requires_approval,
				context_mode, input_bindings_json, output_requirements_json, sort_order,
				context_source_kind, context_source_node_key, prompt_template, parameters_json,
				assignee_selection, thinking_selection
			) VALUES
				(?, ?, 'legacy', 'node-join-legacy', 1, 'new_session', '[]', '[]', 40,
				 'immediate_source', '', '', '[]', 'configured', 'configured'),
				(?, ?, 'canonical', 'node-terminal-legacy', 0, 'new_session', '[]', '[]', 40,
				 'immediate_source', '', '', '[]', 'configured', 'configured')`,
			args: []any{
				graphMigrationLegacyEdge, graphMigrationLegacyTransition,
				graphMigrationCanonicalEdge, graphMigrationCanonicalTransition,
			},
		},
		{
			name: "task",
			sql: `INSERT INTO tasks (
				id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id,
				title, body, source_url, source_workspace_id, pending_initial_managed_branch_name,
				created_at_unix_ms, updated_at_unix_ms, metadata_json
			) VALUES (
				'task-graph-migration', 'link-graph-migration', 1, 1, 'GRF-1',
				'Graph migration', '', '', 'workspace-graph-migration', 'GRF-1', 1, 1, '{}'
			)`,
		},
		{
			name: "session",
			sql: `INSERT INTO sessions (
				id, project_id, workspace_id, artifact_relpath, name, category,
				created_at_unix_ms, updated_at_unix_ms, task_id
			) VALUES (
				'session-graph-migration', 'project-graph-migration', 'workspace-graph-migration',
				'sessions/graph-migration', 'Graph migration', 'main', 1, 1, 'task-graph-migration'
			)`,
		},
		{
			name: "current node",
			sql: `INSERT INTO task_current_nodes (
				task_id, node_id, transition_branch_key, current_input_values_json,
				prior_node_values_json, session_id, scheduling_state, entered_by_edge_id
			) VALUES (
				'task-graph-migration', ?, NULL, '{}', '{"transition_parameters":{}}',
				'session-graph-migration', 'ready', ?
			)`,
			args: []any{graphMigrationLegacyNode, graphMigrationLegacyEdge},
		},
		{
			name: "session association",
			sql: `INSERT INTO session_workflow_node_associations (
				session_id, node_id, transition_branch_key, associated_at_unix_ms
			) VALUES ('session-graph-migration', ?, NULL, 1)`,
			args: []any{graphMigrationLegacyNode},
		},
		{
			name: "approval",
			sql: `INSERT INTO task_pending_approvals (
				id, source_task_id, source_node_id, source_transition_branch_key,
				source_session_id, workflow_version, transition_snapshot_json,
				materialized_values_json, created_at_unix_ms
			) VALUES (
				'approval-graph-migration', 'task-graph-migration', ?, NULL,
				'session-graph-migration', 1, ?, '{}', 1
			)`,
			args: []any{
				graphMigrationLegacyNode,
				fmt.Sprintf(`{"workflow_id":%q,"id":%q,"source_node_id":%q,"transition_id":"legacy","display_name":"Legacy","description":"","source_display_name":"Legacy"}`,
					graphMigrationWorkflowID, graphMigrationLegacyTransition, graphMigrationLegacyNode),
			},
		},
		{
			name: "approval branch",
			sql: `INSERT INTO task_pending_approval_branches (
				approval_id, transition_branch_key, target_snapshot_json,
				effective_edge_configuration_json, context_source_resolution_json
			) VALUES (
				'approval-graph-migration', 'legacy-branch', ?, ?, '{}'
			)`,
			args: []any{
				fmt.Sprintf(`{"node_id":%q,"node_key":"legacy","node_kind":"start","node_display_name":"Legacy","entered_by_edge_id":%q,"current_input_values":{},"prior_values":{"transition_parameters":{}}}`,
					graphMigrationLegacyNode, graphMigrationLegacyEdge),
				fmt.Sprintf(`{"workflow_id":%q,"id":%q,"key":"legacy","transition_group_id":%q,"target_node_id":%q,"target_node_key":"join","target_node_display_name":"Join","target_node_kind":"join","context_mode":"new_session","context_source":{"kind":"immediate_source"},"requires_approval":true,"prompt_template":"","parameters":[],"input_bindings":[],"output_requirements":[]}`,
					graphMigrationWorkflowID, graphMigrationLegacyEdge, graphMigrationLegacyTransition, graphMigrationLegacyNode),
			},
		},
	}
	for _, statement := range statements {
		execGraphMigrationSeed(t, db, statement.sql, statement.args...)
	}
}

func graphMigrationWorkflowBlob(t *testing.T) []byte {
	t.Helper()
	workflowID, err := runtimeids.ParseWorkflowID(graphMigrationWorkflowID)
	if err != nil {
		t.Fatalf("parse fixture Workflow ID: %v", err)
	}
	value, err := workflowID.Value()
	if err != nil {
		t.Fatalf("encode fixture Workflow ID: %v", err)
	}
	raw, ok := value.([]byte)
	if !ok {
		t.Fatalf("fixture Workflow ID value type = %T, want []byte", value)
	}
	return raw
}

func execGraphMigrationSeed(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("seed graph migration fixture: %v\n%s", err, statement)
	}
}

func graphMigrationOrder(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`
SELECT kind || ':' || stable_key
FROM (
	SELECT 'group' AS kind, group_key AS stable_key, sort_order, rowid FROM workflow_node_groups
	UNION ALL
	SELECT 'node', node_key, sort_order, rowid FROM workflow_nodes
	UNION ALL
	SELECT 'transition', transition_id, sort_order, rowid FROM workflow_transition_groups
	UNION ALL
	SELECT 'edge', edge_key, sort_order, rowid FROM workflow_edges
)
ORDER BY kind, sort_order, rowid`)
	if err != nil {
		t.Fatalf("query graph migration order: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan graph migration order: %v", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate graph migration order: %v", err)
	}
	return values
}

func graphMigrationJSONStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("query graph migration JSON: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan graph migration JSON: %v", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate graph migration JSON: %v", err)
	}
	return values
}

func graphMigrationMappedText(t *testing.T, db *sql.DB, table, keyColumn, key string) string {
	t.Helper()
	var raw []byte
	query := fmt.Sprintf(`SELECT id FROM %s WHERE %s = ?`, table, keyColumn)
	if err := db.QueryRow(query, key).Scan(&raw); err != nil {
		t.Fatalf("read migrated %s identity: %v", table, err)
	}
	text, err := runtimeids.GraphEntityIDText(raw)
	if err != nil {
		t.Fatalf("decode migrated %s identity: %v", table, err)
	}
	return text
}

func assertGraphMigrationBlobText(t *testing.T, raw []byte, want, location string) {
	t.Helper()
	got, err := runtimeids.GraphEntityIDText(raw)
	if err != nil {
		t.Fatalf("decode %s: %v", location, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", location, got, want)
	}
}

func assertGraphMigrationJSONIdentity(t *testing.T, document, path, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal([]byte(document), &map[string]any{}); err != nil {
		t.Fatalf("decode migrated JSON document: %v", err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open JSON assertion database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRow(`SELECT json_extract(?, ?)`, document, path).Scan(&got); err != nil {
		t.Fatalf("read migrated JSON identity %s: %v", path, err)
	}
	if got != want {
		t.Fatalf("migrated JSON identity %s = %q, want %q", path, got, want)
	}
}

func assertGraphMigrationStorage(t *testing.T, db *sql.DB) {
	t.Helper()
	checks := []struct {
		table  string
		column string
		where  string
	}{
		{"workflow_node_groups", "id", "1"},
		{"workflow_nodes", "id", "1"},
		{"workflow_nodes", "group_id", "group_id IS NOT NULL"},
		{"workflow_transition_groups", "id", "1"},
		{"workflow_transition_groups", "source_node_id", "1"},
		{"workflow_edges", "id", "1"},
		{"workflow_edges", "transition_group_id", "1"},
		{"workflow_edges", "target_node_id", "1"},
		{"task_current_nodes", "node_id", "1"},
		{"task_current_nodes", "entered_by_edge_id", "entered_by_edge_id IS NOT NULL"},
		{"task_pending_approvals", "source_node_id", "1"},
		{"session_workflow_node_associations", "node_id", "1"},
	}
	for _, check := range checks {
		var invalid int
		query := fmt.Sprintf(`
SELECT count(*)
FROM %s
WHERE %s AND (typeof(%s) != 'blob' OR length(%s) != 16 OR %s = zeroblob(16))`,
			check.table, check.where, check.column, check.column, check.column)
		if err := db.QueryRow(query).Scan(&invalid); err != nil {
			t.Fatalf("inspect %s.%s storage: %v", check.table, check.column, err)
		}
		if invalid != 0 {
			t.Fatalf("%s.%s invalid BLOB rows = %d, want 0", check.table, check.column, invalid)
		}
	}
}

func assertGraphMigrationVersion79State(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, column := range []struct {
		table string
		name  string
	}{
		{"workflow_node_groups", "id"},
		{"workflow_nodes", "id"},
		{"workflow_nodes", "group_id"},
		{"workflow_transition_groups", "id"},
		{"workflow_transition_groups", "source_node_id"},
		{"workflow_edges", "id"},
		{"workflow_edges", "transition_group_id"},
		{"workflow_edges", "target_node_id"},
		{"task_current_nodes", "node_id"},
		{"task_current_nodes", "entered_by_edge_id"},
		{"task_pending_approvals", "source_node_id"},
		{"session_workflow_node_associations", "node_id"},
	} {
		var columnType string
		query := fmt.Sprintf(`SELECT type FROM pragma_table_info(%q) WHERE name = ?`, column.table)
		if err := db.QueryRow(query, column.name).Scan(&columnType); err != nil {
			t.Fatalf("read rolled-back %s.%s type: %v", column.table, column.name, err)
		}
		if columnType != "TEXT" {
			t.Fatalf("rolled-back %s.%s type = %q, want TEXT", column.table, column.name, columnType)
		}
	}
	for _, identity := range []struct {
		name  string
		query string
		want  string
	}{
		{"Node Group", `SELECT id FROM workflow_node_groups WHERE group_key = 'legacy'`, graphMigrationLegacyGroup},
		{"Node", `SELECT id FROM workflow_nodes WHERE node_key = 'legacy'`, graphMigrationLegacyNode},
		{"Node membership", `SELECT group_id FROM workflow_nodes WHERE node_key = 'legacy'`, graphMigrationLegacyGroup},
		{"Transition", `SELECT id FROM workflow_transition_groups WHERE transition_id = 'legacy'`, graphMigrationLegacyTransition},
		{"Transition source", `SELECT source_node_id FROM workflow_transition_groups WHERE transition_id = 'legacy'`, graphMigrationLegacyNode},
		{"Transition Branch", `SELECT id FROM workflow_edges WHERE edge_key = 'legacy'`, graphMigrationLegacyEdge},
		{"Transition Branch group", `SELECT transition_group_id FROM workflow_edges WHERE edge_key = 'legacy'`, graphMigrationLegacyTransition},
		{"Transition Branch target", `SELECT target_node_id FROM workflow_edges WHERE edge_key = 'legacy'`, "node-join-legacy"},
		{"Current Node", `SELECT node_id FROM task_current_nodes WHERE task_id = 'task-graph-migration'`, graphMigrationLegacyNode},
		{"Current Node entering Branch", `SELECT entered_by_edge_id FROM task_current_nodes WHERE task_id = 'task-graph-migration'`, graphMigrationLegacyEdge},
		{"Approval source Node", `SELECT source_node_id FROM task_pending_approvals WHERE id = 'approval-graph-migration'`, graphMigrationLegacyNode},
		{"Session association Node", `SELECT node_id FROM session_workflow_node_associations WHERE session_id = 'session-graph-migration'`, graphMigrationLegacyNode},
		{"Join provider Branch", `SELECT json_extract(provider.value, '$.provider_edge_id') FROM workflow_nodes, json_each(join_input_providers_json) provider WHERE node_key = 'join' ORDER BY CAST(provider.key AS INTEGER) LIMIT 1`, graphMigrationLegacyEdge},
		{"Approval Transition", `SELECT json_extract(transition_snapshot_json, '$.id') FROM task_pending_approvals WHERE id = 'approval-graph-migration'`, graphMigrationLegacyTransition},
		{"Approval Transition source", `SELECT json_extract(transition_snapshot_json, '$.source_node_id') FROM task_pending_approvals WHERE id = 'approval-graph-migration'`, graphMigrationLegacyNode},
		{"Approval target Node", `SELECT json_extract(target_snapshot_json, '$.node_id') FROM task_pending_approval_branches WHERE approval_id = 'approval-graph-migration'`, graphMigrationLegacyNode},
		{"Approval entering Branch", `SELECT json_extract(target_snapshot_json, '$.entered_by_edge_id') FROM task_pending_approval_branches WHERE approval_id = 'approval-graph-migration'`, graphMigrationLegacyEdge},
		{"Approval Branch", `SELECT json_extract(effective_edge_configuration_json, '$.id') FROM task_pending_approval_branches WHERE approval_id = 'approval-graph-migration'`, graphMigrationLegacyEdge},
		{"Approval Branch Transition", `SELECT json_extract(effective_edge_configuration_json, '$.transition_group_id') FROM task_pending_approval_branches WHERE approval_id = 'approval-graph-migration'`, graphMigrationLegacyTransition},
		{"Approval Branch target", `SELECT json_extract(effective_edge_configuration_json, '$.target_node_id') FROM task_pending_approval_branches WHERE approval_id = 'approval-graph-migration'`, graphMigrationLegacyNode},
	} {
		var got string
		if err := db.QueryRow(identity.query).Scan(&got); err != nil {
			t.Fatalf("read rolled-back %s: %v", identity.name, err)
		}
		if got != identity.want {
			t.Fatalf("rolled-back %s = %q, want %q", identity.name, got, identity.want)
		}
	}
	var nodeIDsJSON string
	if err := db.QueryRow(`SELECT node_ids_json FROM workflow_task_status_records WHERE task_id = 'task-graph-migration'`).Scan(&nodeIDsJSON); err != nil {
		t.Fatalf("read rolled-back task status Node identities: %v", err)
	}
	var nodeIDs []string
	if err := json.Unmarshal([]byte(nodeIDsJSON), &nodeIDs); err != nil {
		t.Fatalf("decode rolled-back task status Node identities: %v", err)
	}
	if len(nodeIDs) != 1 || nodeIDs[0] != graphMigrationLegacyNode {
		t.Fatalf("rolled-back task status Node identities = %v, want [%s]", nodeIDs, graphMigrationLegacyNode)
	}
	for _, table := range []string{
		"workflow_node_groups_v80",
		"workflow_nodes_v80",
		"workflow_transition_groups_v80",
		"workflow_edges_v80",
		"task_current_nodes_v80",
		"task_pending_approvals_v80",
		"task_pending_approval_branches_v80",
		"session_workflow_node_associations_v80",
	} {
		if tableExists(t, db, table) {
			t.Fatalf("failed migration retained shadow table %q", table)
		}
	}
}

func assertGraphMigrationVersion(t *testing.T, provider *goose.Provider, want int64) {
	t.Helper()
	version, err := provider.GetDBVersion(t.Context())
	if err != nil {
		t.Fatalf("read graph migration version: %v", err)
	}
	if version != want {
		t.Fatalf("graph migration version = %d, want %d", version, want)
	}
}

func equalGraphMigrationStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestGraphMigrationCanonicalFixtureBytesAreExact(t *testing.T) {
	t.Parallel()
	raw, err := runtimeids.GraphEntityIDBlob(graphMigrationCanonicalNode)
	if err != nil {
		t.Fatalf("encode canonical fixture Node: %v", err)
	}
	if !bytes.Equal(raw, []byte{
		0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x42, 0x22,
		0x82, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22,
	}) {
		t.Fatalf("canonical fixture Node bytes = %x", raw)
	}
}
