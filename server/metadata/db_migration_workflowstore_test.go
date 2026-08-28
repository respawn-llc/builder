package metadata_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"core/server/metadata"
	metadatamigrations "core/server/metadata/migrations"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/toolspec"

	_ "modernc.org/sqlite"
)

const version71CutoverPrompt = `Execute {{.TaskTitle}}.`

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
	provider, err := metadatamigrations.NewProvider(db, nil)
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
    ('node-agent', ?, 'agent', 'agent', 'Agent', 'default', 'Discard legacy Node prompt.', '[{"name":"legacy_input","description":"Discard legacy input."}]', '[{"name":"legacy_output","description":"Discard legacy output."}]', '[]', 'tool', NULL, 1),
    ('node-script', ?, 'script', 'script', 'Script', '', 'Discard', '[]', '[]', '[]', '', 'scripts/run', 2),
    ('node-branch', ?, 'branch', 'agent', 'Branch', 'default', 'Discard', '[]', '[]', '[]', 'tool', NULL, 3),
    ('node-join', ?, 'join', 'join', 'Join', '', 'Discard', '[]', '[]', '[{"input_name":"summary","provider_edge_id":"edge-agent-join"}]', '', NULL, 4),
    ('node-done', ?, 'done', 'terminal', 'Done', '', '', '[]', '[]', '[]', '', NULL, 5);
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES
    ('group-start', 'node-start', 'start', 'Start'),
    ('group-agent', 'node-agent', 'fanout', 'Fanout'),
    ('group-branch', 'node-branch', 'join_branch', 'Join'),
    ('group-script', 'node-script', 'join_script', 'Join'),
    ('group-join', 'node-join', 'done', 'Done');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode,
    prompt_template, parameters_json, input_bindings_json, output_requirements_json
) VALUES
    ('edge-start', 'group-start', 'start', 'node-agent', 'new_session',
     '', '[]', '[]', '[]'),
    ('edge-agent-script', 'group-agent', 'to_script', 'node-script', 'new_session',
     '', '[{"key":"summary","description":"Summary."}]', '[]', '[]'),
    ('edge-agent-branch', 'group-agent', 'to_branch', 'node-branch', 'new_session',
     'Continue {{.TaskTitle}}.', '[]', '[]', '[]'),
    ('edge-agent-join', 'group-branch', 'to_join', 'node-join', 'new_session',
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
		workflowID,
	); err != nil {
		t.Fatalf("seed version 71 workflow: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE workflow_edges
SET prompt_template = ?
WHERE id = 'edge-start'`, version71CutoverPrompt); err != nil {
		t.Fatalf("seed version 71 Transition Prompt: %v", err)
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
	edge := findWorkflowEdgeByKey(t, loaded, "to_script")
	if len(edge.Parameters) != 1 || edge.Parameters[0].Key != "summary" {
		t.Fatalf("loaded Transition Parameters = %+v, want preserved summary Parameter", edge.Parameters)
	}
	var legacyNodeColumns int
	if err := metadataStore.DB().QueryRowContext(ctx, `
SELECT count(*)
FROM pragma_table_info('workflow_nodes')
WHERE name IN ('prompt_template', 'input_fields_json', 'output_fields_json')`).Scan(&legacyNodeColumns); err != nil {
		t.Fatalf("query removed Node contract columns: %v", err)
	}
	if legacyNodeColumns != 0 {
		t.Fatalf("legacy Node contract columns remaining = %d, want none", legacyNodeColumns)
	}
	agentNode, ok := findWorkflowNodeByKey(t, loaded, "agent").(workflow.AgentNode)
	if !ok || agentNode.SubagentRole != "default" || agentNode.CompletionMode != "tool" {
		t.Fatalf("preserved Agent configuration = %#v", agentNode)
	}
	scriptNode, ok := findWorkflowNodeByKey(t, loaded, "script").(workflow.ScriptNode)
	if !ok || scriptNode.ScriptPath.String() != "scripts/run" {
		t.Fatalf("preserved Script configuration = %#v", scriptNode)
	}
	joinNode, ok := findWorkflowNodeByKey(t, loaded, "join").(workflow.JoinNode)
	providerEdge := findWorkflowEdgeByKey(t, loaded, "to_join")
	if !ok ||
		len(joinNode.JoinInputProviders) != 1 ||
		joinNode.JoinInputProviders[0].InputName != "summary" ||
		joinNode.JoinInputProviders[0].ProviderEdgeID != providerEdge.ID {
		t.Fatalf("preserved Join configuration = %#v, provider edge = %#v", joinNode, providerEdge)
	}
	var foreignKeyViolations int
	if err := metadataStore.DB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatalf("foreign-key check: %v", err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("foreign-key violations after migration 72 = %d", foreignKeyViolations)
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
	startEdge := findWorkflowEdgeByKey(t, loaded, "start")
	if len(started.Mutation.Created) != 1 ||
		started.Mutation.Created[0].EnteredByEdgeID == nil ||
		*started.Mutation.Created[0].EnteredByEdgeID != startEdge.ID {
		t.Fatalf("started migrated Task mutation = %+v, want actual Transition-owned start path", started.Mutation)
	}
	startContext, err := store.ResolveCurrentNodeStartContext(ctx, started.Mutation.Created[0].Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
	}
	if startContext.TransitionPrompt != version71CutoverPrompt {
		t.Fatalf("start Transition Prompt = %q, want migrated prompt", startContext.TransitionPrompt)
	}
	completed, err := store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "fanout",
		OutputValues: map[string]string{"summary": "migrated summary"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode through migrated Parameters: %v", err)
	}
	var scriptCurrentNode *workflow.CurrentNode
	for index := range completed.Mutation.Created {
		currentNode := &completed.Mutation.Created[index]
		if currentNode.Reference.NodeID == scriptNode.ID {
			scriptCurrentNode = currentNode
			break
		}
	}
	if scriptCurrentNode == nil {
		t.Fatalf("fanout mutation = %+v, want migrated script target", completed.Mutation)
	}
	scriptContext, err := store.ResolveCurrentNodeStartContext(ctx, scriptCurrentNode.Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext script target: %v", err)
	}
	if scriptContext.ParameterValues["summary"] != "migrated summary" ||
		scriptCurrentNode.CurrentInputValues["summary"] != "migrated summary" {
		t.Fatalf("materialized migrated summary = parameter_values=%v current_input_values=%v",
			scriptContext.ParameterValues, scriptCurrentNode.CurrentInputValues)
	}
}

type cutoverRoleResolver struct{}

func (cutoverRoleResolver) RoleExists(string) bool { return true }

func (cutoverRoleResolver) RoleToolEnabled(string, toolspec.ID) bool { return true }

func (cutoverRoleResolver) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	return workflow.TargetAgentRole{Identity: role, QuestionsEnabled: true, ExplicitAgentCallable: true}, true
}

func (cutoverRoleResolver) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return []workflow.TargetAgentRole{
		{Identity: workflow.DefaultAgentRole, QuestionsEnabled: true, ExplicitAgentCallable: true},
		{Identity: "coder", QuestionsEnabled: true, ExplicitAgentCallable: true},
	}
}

func findWorkflowEdgeByKey(t *testing.T, definition workflow.Definition, key workflow.ModelKey) workflow.Edge {
	t.Helper()
	for _, edge := range definition.Edges {
		if edge.Key == key {
			return edge
		}
	}
	t.Fatalf("workflow edge key %q not found", key)
	return workflow.Edge{}
}

func findWorkflowNodeByKey(t *testing.T, definition workflow.Definition, key workflow.ModelKey) workflow.Node {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Identity().Key == key {
			return node
		}
	}
	t.Fatalf("workflow node key %q not found", key)
	return nil
}
