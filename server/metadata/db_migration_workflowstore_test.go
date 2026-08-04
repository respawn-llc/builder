package metadata_test

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/toolspec"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.up.sql
var workflowMigrationFiles embed.FS

func TestVersion71CutoverLoadsAndStartsThroughWorkflowStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bootstrap, err := metadata.Open(filepath.Join(root, "bootstrap"))
	if err != nil {
		t.Fatalf("bootstrap metadata.Open: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap metadata store: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "db"), 0o755); err != nil {
		t.Fatalf("create database directory: %v", err)
	}
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open version 71 database: %v", err)
	}
	migrations, err := fs.Sub(workflowMigrationFiles, "migrations")
	if err != nil {
		t.Fatalf("open embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
		goose.WithDisableGlobalRegistry(true),
		goose.WithLogger(goose.NopLogger()),
	)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 71); err != nil {
		t.Fatalf("migrate database to version 71: %v", err)
	}
	workflowID := runtimeids.NewWorkflowID()
	if _, err := db.ExecContext(ctx, `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-workflow-store-cutover', 'Project', 1, 1, '{}');
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('workspace-cutover', 'project-workflow-store-cutover', '/workspace-cutover', '{}', 1, 1);
INSERT INTO workflows (id, name, description, version, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, 'Cutover', '', 1, 1, 1);
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role,
    prompt_template, input_fields_json, output_fields_json,
    join_input_providers_json, completion_mode, script_path, sort_order
) VALUES
    ('node-start', ?, 'backlog', 'start', 'Backlog', '', '', '[]', '[]', '[]', '', NULL, 0),
    ('node-agent', ?, 'agent', 'agent', 'Agent', 'default', 'Discard', '[]', '[]', '[]', 'tool', NULL, 1),
    ('node-script', ?, 'script', 'script', 'Script', '', 'Discard', '[]', '[]', '[]', '', 'scripts/run', 2),
    ('node-join', ?, 'join', 'join', 'Join', '', 'Discard', '[]', '[]', '[{"input_name":"summary","provider_edge_id":"edge-agent-join"}]', '', NULL, 3),
    ('node-done', ?, 'done', 'terminal', 'Done', '', '', '[]', '[]', '[]', '', NULL, 4);
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES
    ('group-start', 'node-start', 'start', 'Start'),
    ('group-agent', 'node-agent', 'fanout', 'Fanout'),
    ('group-script', 'node-script', 'join_script', 'Join'),
    ('group-join', 'node-join', 'done', 'Done');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode,
    prompt_template, parameters_json, input_bindings_json, output_requirements_json
) VALUES
    ('edge-start', 'group-start', 'start', 'node-agent', 'new_session',
     'Execute {{.TaskTitle}}.', '[]', '[]', '[]'),
    ('edge-agent-script', 'group-agent', 'to_script', 'node-script', 'new_session',
     '', '[{"key":"summary","description":"Summary."}]', '[]', '[]'),
    ('edge-agent-join', 'group-agent', 'to_join', 'node-join', 'new_session',
     '', '[]', '[]', '[]'),
    ('edge-script-join', 'group-script', 'script_join', 'node-join', 'new_session',
     '', '[]', '[]', '[]'),
    ('edge-join-done', 'group-join', 'done', 'node-done', 'new_session',
     '', '[]', '[]', '[]');
INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('link-cutover', 'project-workflow-store-cutover', ?, 1, 1);
UPDATE projects
SET default_project_workflow_link_id = 'link-cutover'
WHERE id = 'project-workflow-store-cutover'`,
		workflowID,
		workflowID,
		workflowID,
		workflowID,
		workflowID,
		workflowID,
	); err != nil {
		t.Fatalf("seed version 71 workflow: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 71 database: %v", err)
	}

	metadataStore, err := metadata.Open(root)
	if err != nil {
		t.Fatalf("open migrated metadata store: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(cutoverRoleResolver{}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	loaded, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	validation := workflow.ValidateDefinition(loaded, workflow.ValidationOptions{
		Context:      workflow.ValidationContextExecution,
		RoleResolver: cutoverRoleResolver{},
	})
	if len(validation.Errors) != 0 {
		t.Fatalf("loaded migrated definition validation = %+v", validation.Errors)
	}
	edge := findWorkflowEdge(t, loaded, "edge-agent-script")
	if len(edge.Parameters) != 1 || edge.Parameters[0].Key != "summary" {
		t.Fatalf("loaded Transition Parameters = %+v, want preserved summary Parameter", edge.Parameters)
	}
	task, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         "project-workflow-store-cutover",
		WorkflowID:        &workflowID,
		SourceWorkspaceID: "workspace-cutover",
		Title:             "Cutover",
		Body:              "Cutover",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 ||
		started.Mutation.Created[0].EnteredByEdgeID == nil ||
		*started.Mutation.Created[0].EnteredByEdgeID != "edge-start" {
		t.Fatalf("started migrated Task mutation = %+v, want actual Transition-owned start path", started.Mutation)
	}
}

type cutoverRoleResolver struct{}

func (cutoverRoleResolver) RoleExists(string) bool { return true }

func (cutoverRoleResolver) RoleToolEnabled(string, toolspec.ID) bool { return true }

func findWorkflowEdge(t *testing.T, definition workflow.Definition, id workflow.EdgeID) workflow.Edge {
	t.Helper()
	for _, edge := range definition.Edges {
		if edge.ID == id {
			return edge
		}
	}
	t.Fatalf("workflow edge %q not found", id)
	return workflow.Edge{}
}
