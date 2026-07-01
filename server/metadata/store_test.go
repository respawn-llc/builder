package metadata

import (
	"context"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureWorkspaceBindingDoesNotRegisterUnknownWorkspace(t *testing.T) {
	store, cfg := newMetadataTestStoreWithoutBinding(t)

	if _, err := store.EnsureWorkspaceBinding(context.Background(), cfg.WorkspaceRoot); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("EnsureWorkspaceBinding error = %v, want ErrWorkspaceNotRegistered", err)
	}
	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected no registered projects, got %+v", projects)
	}

	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if binding.ProjectID == "" || binding.WorkspaceID == "" {
		t.Fatalf("expected registered binding ids, got %+v", binding)
	}

	resolved, err := store.EnsureWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding after registration: %v", err)
	}
	if resolved.ProjectID != binding.ProjectID || resolved.WorkspaceID != binding.WorkspaceID || resolved.ProjectKey != binding.ProjectKey {
		t.Fatalf("resolved binding mismatch: got %+v want %+v", resolved, binding)
	}
}

func TestResolveWorkspacePathLeavesNestedDirectoryUnbound(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "subdir", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}
	store, cfg := newMetadataTestStoreForWorkspace(t, workspace)

	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}

	canonicalNested, resolved, err := store.ResolveWorkspacePath(context.Background(), nested)
	if err != nil {
		t.Fatalf("ResolveWorkspacePath nested: %v", err)
	}
	if canonicalNested == binding.CanonicalRoot {
		t.Fatalf("expected resolved canonical path for nested directory, got workspace root %q", canonicalNested)
	}
	if resolved != nil {
		t.Fatalf("expected nested directory to remain unbound, got %+v", *resolved)
	}

	if _, err := store.EnsureWorkspaceBinding(context.Background(), nested); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("EnsureWorkspaceBinding nested error = %v, want ErrWorkspaceNotRegistered", err)
	}

	registered, err := store.RegisterWorkspaceBinding(context.Background(), nested)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding nested: %v", err)
	}
	if registered.CanonicalRoot == binding.CanonicalRoot {
		t.Fatalf("expected nested registration to create its own workspace, got %+v", registered)
	}
	if registered.CanonicalRoot != canonicalNested {
		t.Fatalf("registered nested root = %q, want %q", registered.CanonicalRoot, canonicalNested)
	}

	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("project count = %d, want 2", len(projects))
	}
}

func TestLookupWorkspaceBindingByIDReturnsWorkspaceNotRegisteredForUnknownID(t *testing.T) {
	store, _ := newMetadataTestStoreWithoutBinding(t)

	if _, err := store.LookupWorkspaceBindingByID(context.Background(), "workspace-missing"); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("LookupWorkspaceBindingByID error = %v, want ErrWorkspaceNotRegistered", err)
	}
}

func TestAttachWorkspaceToProjectAllowsNestedPathAsSeparateWorkspace(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "nested")
	other := t.TempDir()
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}

	store, cfg := newMetadataTestStoreForWorkspace(t, workspace)
	otherCfg, err := config.Load(other, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load other: %v", err)
	}

	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding workspace: %v", err)
	}
	otherBinding, err := store.RegisterWorkspaceBinding(context.Background(), otherCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding other: %v", err)
	}

	resolved, err := store.AttachWorkspaceToProject(context.Background(), binding.ProjectID, nested)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject nested: %v", err)
	}
	if resolved.ProjectID != binding.ProjectID {
		t.Fatalf("nested attach project id = %q, want %q", resolved.ProjectID, binding.ProjectID)
	}
	if resolved.CanonicalRoot == binding.CanonicalRoot {
		t.Fatalf("expected nested attach to create separate workspace, got %+v", resolved)
	}
	canonicalNested, err := config.CanonicalWorkspaceRoot(nested)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot nested: %v", err)
	}
	if resolved.CanonicalRoot != canonicalNested {
		t.Fatalf("nested attach root = %q, want %q", resolved.CanonicalRoot, canonicalNested)
	}

	sharedPath, err := store.AttachWorkspaceToProject(context.Background(), otherBinding.ProjectID, nested)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject shared path into other project: %v", err)
	}
	if sharedPath.ProjectID != otherBinding.ProjectID {
		t.Fatalf("shared path project id = %q, want %q", sharedPath.ProjectID, otherBinding.ProjectID)
	}
	if sharedPath.WorkspaceID == resolved.WorkspaceID {
		t.Fatalf("shared path workspace id reused across projects: %+v", sharedPath)
	}
	if sharedPath.CanonicalRoot != canonicalNested {
		t.Fatalf("shared path root = %q, want %q", sharedPath.CanonicalRoot, canonicalNested)
	}

	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("project count = %d, want 2", len(projects))
	}
}

func TestUnlinkProjectWorkspaceBlocksUnsafeStates(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	defaultBlockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, binding.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace default: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, defaultBlockers, "default_workspace")

	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	execSeed(t, store.db, "active source task", `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-active-workspace', 'link-1', 1, 1, 'BLD-1', 'Active', '', ?, ?, ?, json_object('source_workspace_snapshot', json_object('workspace_id', ?, 'display_name', ?, 'root_path', ?)))`, attached.WorkspaceID, now, now, attached.WorkspaceID, attached.WorkspaceName, attached.CanonicalRoot)
	execSeed(t, store.db, "active source placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES ('placement-active-workspace', 'task-active-workspace', 'node-agent', 'active', ?, ?)`, now, now)

	activeTaskBlockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace active task: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, activeTaskBlockers, "non_terminal_tasks")

	execSeed(t, store.db, "complete active source placement", `UPDATE task_node_placements SET state = 'completed' WHERE id = 'placement-active-workspace'`)
	execSeed(t, store.db, "pending approval transition", `INSERT INTO task_transitions (id, task_id, source_placement_id, transition_id, workflow_revision_seen, actor, state, output_values_json, created_at_unix_ms)
VALUES ('transition-pending-workspace', 'task-active-workspace', 'placement-active-workspace', 'done', 1, 'agent', 'pending_approval', '{}', ?)`, now)
	pendingApprovalBlockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace pending approval transition: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, pendingApprovalBlockers, "non_terminal_tasks")
}

func TestDeleteProjectBlocksWorkflowWork(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx, now := context.Background(), time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTaskWithID(t, store, "task-delete-active", "link-1", 1, "BLD-1", "placement-delete-active", "node-agent")
	seedWorkflowTaskWithID(t, store, "task-delete-running", "link-1", 2, "BLD-2", "placement-delete-running", "node-agent")
	seedWorkflowTaskWithID(t, store, "task-delete-runnable", "link-1", 3, "BLD-3", "placement-delete-runnable", "node-agent")
	execSeed(t, store.db, "delete runs", `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, automation_requested_at_unix_ms, started_at_unix_ms, created_at_unix_ms, updated_at_unix_ms)
VALUES ('run-delete-running', 'placement-delete-running', 1, 0, ?, ?, ?),
       ('run-delete-runnable', 'placement-delete-runnable', 1, ?, 0, ?, ?)`, now, now, now, now, now, now)
	blockers, err := store.DeleteProject(ctx, binding.ProjectID, func(ProjectSessionArtifact, bool) error { return nil })
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	counts := map[string]int{}
	for _, blocker := range blockers {
		counts[blocker.Code] = blocker.Count
	}
	if len(blockers) != 3 || counts["non_terminal_tasks"] != 3 || counts["active_runs"] != 1 || counts["runnable_runs"] != 1 {
		t.Fatalf("delete blockers = %+v, want exact workflow blockers", blockers)
	}
}

func TestDeleteProjectAllowsBacklogTasks(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx, now := context.Background(), time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTaskWithID(t, store, "task-delete-backlog", "link-1", 1, "BLD-1", "placement-delete-backlog", "node-start")

	blockers, err := store.DeleteProject(ctx, binding.ProjectID, func(ProjectSessionArtifact, bool) error { return nil })
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("delete blockers = %+v, want none for backlog-only task", blockers)
	}
}

func TestUnlinkProjectWorkspacePreservesTerminalHistory(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	worktreeID := "worktree-terminal-workspace"
	execSeed(t, store.db, "terminal workspace worktree", `INSERT INTO worktrees (id, workspace_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, ?, '{}', ?, ?)`, worktreeID, attached.WorkspaceID, filepath.Join(attached.CanonicalRoot, "terminal-worktree"), now, now)
	execSeed(t, store.db, "terminal source task", `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-terminal-workspace', 'link-1', 1, 1, 'BLD-1', 'Terminal', '', ?, ?, ?, ?, json_object('source_workspace_snapshot', json_object('workspace_id', ?, 'display_name', ?, 'root_path', ?)))`, attached.WorkspaceID, worktreeID, now, now, attached.WorkspaceID, attached.WorkspaceName, attached.CanonicalRoot)
	execSeed(t, store.db, "terminal source placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES ('placement-terminal-workspace', 'task-terminal-workspace', 'node-done', 'active', ?, ?)`, now, now)
	execSeed(t, store.db, "historical workspace session", `INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, name, first_prompt_preview, input_draft, parent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, in_flight_step, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json)
VALUES ('session-terminal-workspace', ?, ?, ?, ?, 'Historical', '', '', '', ?, ?, 0, 1, 0, 1, '.', '{}', '{}', '{}', json_object('workspace_root', ?, 'workspace_container', ?))`, binding.ProjectID, attached.WorkspaceID, worktreeID, filepath.ToSlash(filepath.Join("projects", binding.ProjectID, "sessions", "session-terminal-workspace")), now, now, attached.CanonicalRoot, "sessions")

	blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("unlink blockers = %+v, want none", blockers)
	}
	if _, err := store.GetWorkspaceByID(ctx, attached.WorkspaceID); err == nil {
		t.Fatalf("workspace %q still exists after unlink", attached.WorkspaceID)
	}
	var taskCount int
	var sourceWorkspaceID sql.NullString
	var managedWorktreeID sql.NullString
	var metadataJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), source_workspace_id, managed_worktree_id, metadata_json FROM tasks WHERE id = 'task-terminal-workspace'`).Scan(&taskCount, &sourceWorkspaceID, &managedWorktreeID, &metadataJSON); err != nil {
		t.Fatalf("scan preserved task: %v", err)
	}
	if taskCount != 1 || sourceWorkspaceID.Valid || managedWorktreeID.Valid || !strings.Contains(metadataJSON, attached.CanonicalRoot) {
		t.Fatalf("preserved task count/source/managed/metadata = %d/%v/%v/%s", taskCount, sourceWorkspaceID, managedWorktreeID, metadataJSON)
	}
	var sessionWorkspaceID sql.NullString
	var sessionWorktreeID sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT workspace_id, worktree_id FROM sessions WHERE id = 'session-terminal-workspace'`).Scan(&sessionWorkspaceID, &sessionWorktreeID); err != nil {
		t.Fatalf("scan preserved session: %v", err)
	}
	if sessionWorkspaceID.Valid || sessionWorktreeID.Valid {
		t.Fatalf("preserved session workspace/worktree = %v/%v, want null/null", sessionWorkspaceID, sessionWorktreeID)
	}
	record, err := store.ResolvePersistedSession(ctx, "session-terminal-workspace")
	if err != nil {
		t.Fatalf("ResolvePersistedSession after unlink: %v", err)
	}
	if record.Meta.WorkspaceRoot != attached.CanonicalRoot || record.Meta.WorkspaceContainer != "sessions" {
		t.Fatalf("session workspace snapshot = %q/%q, want %q/%q", record.Meta.WorkspaceRoot, record.Meta.WorkspaceContainer, attached.CanonicalRoot, "sessions")
	}
}

func TestProjectWorkspaceMutationsDoNotRequireWorkflowEvents(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	if err := store.UpdateProjectMetadata(ctx, binding.ProjectID, "Events", ""); err != nil {
		t.Fatalf("UpdateProjectMetadata: %v", err)
	}
	if err := store.SetProjectDefaultWorkspace(ctx, binding.ProjectID, attached.WorkspaceID); err != nil {
		t.Fatalf("SetProjectDefaultWorkspace attached: %v", err)
	}
	if err := store.SetProjectDefaultWorkspace(ctx, binding.ProjectID, binding.WorkspaceID); err != nil {
		t.Fatalf("SetProjectDefaultWorkspace original: %v", err)
	}
	if blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID); err != nil {
		t.Fatalf("UnlinkProjectWorkspace: %v", err)
	} else if len(blockers) != 0 {
		t.Fatalf("unlink blockers = %+v, want none", blockers)
	}
	if tableExists(t, store.db, "workflow_events") {
		t.Fatal("workflow_events should not exist; project mutations must not depend on persisted invalidation rows")
	}
}

func assertWorkspaceUnlinkBlocker(t *testing.T, blockers []serverapi.ProjectWorkspaceUnlinkBlocker, code string) {
	t.Helper()
	for _, blocker := range blockers {
		if blocker.Code == code {
			return
		}
	}
	t.Fatalf("blockers = %+v, want code %q", blockers, code)
}

func TestRebindWorkspacePreservesWorkspaceIdentity(t *testing.T) {
	oldWorkspace := t.TempDir()
	newParent := t.TempDir()
	newWorkspace := filepath.Join(newParent, "renamed-workspace")
	store, cfg, binding := newMetadataTestStoreForBoundWorkspace(t, oldWorkspace)
	sessionID := "session-rebind"
	sessionDir := config.ProjectSessionDir(cfg, binding.ProjectID, sessionID)
	if err := store.ImportSessionSnapshot(context.Background(), session.PersistedStoreSnapshot{
		SessionDir: sessionDir,
		Meta: session.Meta{
			SessionID:          sessionID,
			WorkspaceRoot:      binding.CanonicalRoot,
			WorkspaceContainer: filepath.Base(sessionDir),
			FirstPromptPreview: "hello",
			CreatedAt:          time.Now().UTC(),
			UpdatedAt:          time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("ImportSessionSnapshot: %v", err)
	}
	if err := os.Rename(oldWorkspace, newWorkspace); err != nil {
		t.Fatalf("Rename workspace: %v", err)
	}

	rebound, err := store.RebindWorkspace(context.Background(), oldWorkspace, newWorkspace)
	if err != nil {
		t.Fatalf("RebindWorkspace: %v", err)
	}
	canonicalNewWorkspace, err := config.CanonicalWorkspaceRoot(newWorkspace)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot newWorkspace: %v", err)
	}
	if rebound.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("rebound workspace id = %q, want %q", rebound.WorkspaceID, binding.WorkspaceID)
	}
	if rebound.ProjectID != binding.ProjectID {
		t.Fatalf("rebound project id = %q, want %q", rebound.ProjectID, binding.ProjectID)
	}
	if rebound.CanonicalRoot != canonicalNewWorkspace {
		t.Fatalf("rebound canonical root = %q, want %q", rebound.CanonicalRoot, canonicalNewWorkspace)
	}
	if _, err := store.EnsureWorkspaceBinding(context.Background(), oldWorkspace); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("EnsureWorkspaceBinding old workspace error = %v, want ErrWorkspaceNotRegistered", err)
	}
	resolved, err := store.EnsureWorkspaceBinding(context.Background(), newWorkspace)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding new workspace: %v", err)
	}
	if resolved.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("resolved rebound workspace id = %q, want %q", resolved.WorkspaceID, binding.WorkspaceID)
	}
	var sessionWorkspaceID string
	if err := store.db.QueryRowContext(context.Background(), "SELECT workspace_id FROM sessions WHERE id = ?", sessionID).Scan(&sessionWorkspaceID); err != nil {
		t.Fatalf("scan rebound session workspace id: %v", err)
	}
	if sessionWorkspaceID != binding.WorkspaceID {
		t.Fatalf("session workspace id = %q, want %q", sessionWorkspaceID, binding.WorkspaceID)
	}
	var workspaceCount int
	if err := store.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM workspaces WHERE project_id = ?", binding.ProjectID).Scan(&workspaceCount); err != nil {
		t.Fatalf("count project workspaces: %v", err)
	}
	if workspaceCount != 1 {
		t.Fatalf("workspace count after rebind = %d, want 1", workspaceCount)
	}
}

func TestRebindWorkspaceRejectsInvalidTargets(t *testing.T) {
	oldWorkspace := t.TempDir()
	otherWorkspace := t.TempDir()
	projectWorkspace := t.TempDir()
	missingWorkspace := filepath.Join(t.TempDir(), "missing")

	store, cfg := newMetadataTestStoreForWorkspace(t, oldWorkspace)
	otherCfg, err := config.Load(otherWorkspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load otherWorkspace: %v", err)
	}
	oldBinding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding oldWorkspace: %v", err)
	}
	_, err = store.RegisterWorkspaceBinding(context.Background(), otherCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding otherWorkspace: %v", err)
	}
	_, err = store.AttachWorkspaceToProject(context.Background(), oldBinding.ProjectID, projectWorkspace)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject projectWorkspace: %v", err)
	}

	if _, err := store.RebindWorkspace(context.Background(), filepath.Join(t.TempDir(), "unknown-old"), otherWorkspace); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("RebindWorkspace unknown old error = %v, want ErrWorkspaceNotRegistered", err)
	}
	if _, err := store.RebindWorkspace(context.Background(), oldWorkspace, missingWorkspace); !errors.Is(err, ErrWorkspacePathMissing) {
		t.Fatalf("RebindWorkspace missing new error = %v, want ErrWorkspacePathMissing", err)
	}
	if _, err := store.RebindWorkspace(context.Background(), oldWorkspace, projectWorkspace); !errors.Is(err, ErrWorkspaceAlreadyBound) {
		t.Fatalf("RebindWorkspace bound new error = %v, want ErrWorkspaceAlreadyBound", err)
	}
	resolved, err := store.EnsureWorkspaceBinding(context.Background(), oldWorkspace)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding old workspace after failed rebinds: %v", err)
	}
	if resolved.WorkspaceID != oldBinding.WorkspaceID {
		t.Fatalf("resolved workspace id after failed rebinds = %q, want %q", resolved.WorkspaceID, oldBinding.WorkspaceID)
	}
}

func TestRebindWorkspaceAllowsTargetPathUsedByAnotherProject(t *testing.T) {
	oldWorkspace := t.TempDir()
	sharedTarget := t.TempDir()

	store, cfg := newMetadataTestStoreForWorkspace(t, oldWorkspace)
	targetCfg, err := config.Load(sharedTarget, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load sharedTarget: %v", err)
	}
	oldBinding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding oldWorkspace: %v", err)
	}
	targetBinding, err := store.RegisterWorkspaceBinding(context.Background(), targetCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding sharedTarget: %v", err)
	}
	rebound, err := store.RebindWorkspace(context.Background(), oldWorkspace, sharedTarget)
	if err != nil {
		t.Fatalf("RebindWorkspace shared target: %v", err)
	}
	if rebound.WorkspaceID != oldBinding.WorkspaceID {
		t.Fatalf("rebound workspace id = %q, want %q", rebound.WorkspaceID, oldBinding.WorkspaceID)
	}
	if rebound.ProjectID != oldBinding.ProjectID {
		t.Fatalf("rebound project id = %q, want %q", rebound.ProjectID, oldBinding.ProjectID)
	}
	if rebound.ProjectID == targetBinding.ProjectID {
		t.Fatalf("rebound project reused target project: %+v target %+v", rebound, targetBinding)
	}
}

func TestRebindWorkspaceRejectsAmbiguousOldPath(t *testing.T) {
	oldWorkspace := t.TempDir()
	newWorkspace := t.TempDir()
	store, cfg := newMetadataTestStoreForWorkspace(t, oldWorkspace)

	if _, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterWorkspaceBinding oldWorkspace: %v", err)
	}
	if _, err := store.CreateProjectForWorkspace(context.Background(), cfg.WorkspaceRoot, "second"); err != nil {
		t.Fatalf("CreateProjectForWorkspace duplicate: %v", err)
	}
	if _, err := store.RebindWorkspace(context.Background(), oldWorkspace, newWorkspace); !errors.Is(err, serverapi.ErrWorkspaceBindingAmbiguous) {
		t.Fatalf("RebindWorkspace duplicate old error = %v, want ErrWorkspaceBindingAmbiguous", err)
	}
}

func TestRetargetSessionWorkspaceAttachesTargetAndUpdatesSession(t *testing.T) {
	ctx := context.Background()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	store, cfg, bindingA := newMetadataTestStoreForBoundWorkspace(t, workspaceA)
	sess, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), bindingA.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.SetName("incident triage"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	worktreeRootA := filepath.Join(cfg.WorkspaceRoot, "wt-a")
	if err := os.MkdirAll(worktreeRootA, 0o755); err != nil {
		t.Fatalf("MkdirAll worktreeRootA: %v", err)
	}
	canonicalWorktreeRootA, err := config.CanonicalWorkspaceRoot(worktreeRootA)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot worktreeRootA: %v", err)
	}
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:              "worktree-a",
		WorkspaceID:     bindingA.WorkspaceID,
		CanonicalRoot:   canonicalWorktreeRootA,
		DisplayName:     filepath.Base(canonicalWorktreeRootA),
		Availability:    "available",
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := store.UpdateSessionExecutionTargetByID(ctx, sess.Meta().SessionID, bindingA.WorkspaceID, "worktree-a", "pkg"); err != nil {
		t.Fatalf("UpdateSessionExecutionTargetByID before retarget: %v", err)
	}
	if err := sess.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		Branch:        "feature/a",
		WorktreePath:  canonicalWorktreeRootA,
		WorkspaceRoot: cfg.WorkspaceRoot,
		EffectiveCwd:  filepath.Join(canonicalWorktreeRootA, "pkg"),
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState before retarget: %v", err)
	}

	retargeted, err := store.RetargetSessionWorkspace(ctx, sess.Meta().SessionID, workspaceB)
	if err != nil {
		t.Fatalf("RetargetSessionWorkspace: %v", err)
	}
	canonicalWorkspaceB, err := config.CanonicalWorkspaceRoot(workspaceB)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot workspaceB: %v", err)
	}
	if retargeted.ProjectID != bindingA.ProjectID {
		t.Fatalf("retargeted project id = %q, want %q", retargeted.ProjectID, bindingA.ProjectID)
	}
	if retargeted.CanonicalRoot != canonicalWorkspaceB {
		t.Fatalf("retargeted canonical root = %q, want %q", retargeted.CanonicalRoot, canonicalWorkspaceB)
	}

	resolvedBinding, err := store.EnsureWorkspaceBinding(ctx, workspaceB)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding workspaceB: %v", err)
	}
	if resolvedBinding.ProjectID != bindingA.ProjectID {
		t.Fatalf("workspaceB project id = %q, want %q", resolvedBinding.ProjectID, bindingA.ProjectID)
	}

	target, err := store.ResolveSessionExecutionTarget(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceID != retargeted.WorkspaceID {
		t.Fatalf("target workspace id = %q, want %q", target.WorkspaceID, retargeted.WorkspaceID)
	}
	if target.WorkspaceRoot != canonicalWorkspaceB {
		t.Fatalf("target workspace root = %q, want %q", target.WorkspaceRoot, canonicalWorkspaceB)
	}
	if target.WorktreeID != "" {
		t.Fatalf("target worktree id = %q, want empty after workspace retarget", target.WorktreeID)
	}
	if target.CwdRelpath != "." {
		t.Fatalf("target cwd relpath = %q, want . after workspace retarget", target.CwdRelpath)
	}
	if target.WorktreeRoot != "" {
		t.Fatalf("target worktree root = %q, want empty after workspace retarget", target.WorktreeRoot)
	}
	if target.EffectiveWorkdir != canonicalWorkspaceB {
		t.Fatalf("target effective workdir = %q, want %q", target.EffectiveWorkdir, canonicalWorkspaceB)
	}
	if target.EffectiveWorkdir == filepath.Join(canonicalWorktreeRootA, "pkg") {
		t.Fatalf("target effective workdir leaked previous worktree path %q", target.EffectiveWorkdir)
	}

	reopened, err := session.OpenByID(cfg.PersistenceRoot, sess.Meta().SessionID, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if reopened.Meta().WorkspaceRoot != canonicalWorkspaceB {
		t.Fatalf("reopened workspace root = %q, want %q", reopened.Meta().WorkspaceRoot, canonicalWorkspaceB)
	}
	if reopened.Meta().WorktreeReminder != nil {
		t.Fatalf("expected stale worktree reminder cleared after workspace retarget, got %+v", reopened.Meta().WorktreeReminder)
	}
}

func TestResolvePersistedSessionPreservesWorktreeReminderStateFromMetadata(t *testing.T) {
	store, cfg, binding := newMetadataTestStore(t)
	sess, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	reminder := &session.WorktreeReminderState{
		Mode:                  session.WorktreeReminderModeEnter,
		Branch:                "feature/reminder",
		WorktreePath:          "/tmp/wt-reminder",
		WorkspaceRoot:         cfg.WorkspaceRoot,
		EffectiveCwd:          "/tmp/wt-reminder/pkg",
		HasIssuedInGeneration: true,
		IssuedCompactionCount: 7,
	}
	if err := sess.SetWorktreeReminderState(reminder); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}

	reopened, err := session.OpenByID(cfg.PersistenceRoot, sess.Meta().SessionID, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	state := reopened.Meta().WorktreeReminder
	if state == nil {
		t.Fatal("expected persisted worktree reminder state")
	}
	if *state != *reminder {
		t.Fatalf("worktree reminder = %+v, want %+v", *state, *reminder)
	}
}

func TestResolvePersistedSessionPreservesGoalStateFromMetadata(t *testing.T) {
	store, cfg, binding := newMetadataTestStore(t)
	sess, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	goal, err := sess.SetGoal("ship durable goal metadata", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	reopened, err := session.OpenByID(cfg.PersistenceRoot, sess.Meta().SessionID, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	persisted := reopened.Meta().Goal
	if persisted == nil {
		t.Fatal("expected persisted goal state")
	}
	if *persisted != goal {
		t.Fatalf("goal = %+v, want %+v", *persisted, goal)
	}
}

func TestRebindWorkspaceRetargetsDescendantWorktrees(t *testing.T) {
	ctx := context.Background()
	oldWorkspace := t.TempDir()
	oldWorktree := filepath.Join(oldWorkspace, "wt-a")
	newParent := t.TempDir()
	newWorkspace := filepath.Join(newParent, "workspace-moved")
	newWorktree := filepath.Join(newWorkspace, "wt-a")
	if err := os.MkdirAll(oldWorktree, 0o755); err != nil {
		t.Fatalf("MkdirAll oldWorktree: %v", err)
	}
	store, cfg, binding := newMetadataTestStoreForBoundWorkspace(t, oldWorkspace)
	worktreeID := "worktree-rebind"
	canonicalOldWorktree, err := config.CanonicalWorkspaceRoot(oldWorktree)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot oldWorktree: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO worktrees (
			id,
			workspace_id,
			canonical_root_path,
			git_metadata_json,
			created_at_unix_ms,
			updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?)
	`, worktreeID, binding.WorkspaceID, canonicalOldWorktree, "{}", now, now); err != nil {
		t.Fatalf("insert worktree: %v", err)
	}
	projectSessionsDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.SetName("hello"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if err := sess.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	sessionID := sess.Meta().SessionID
	if _, err := store.db.ExecContext(ctx, "UPDATE sessions SET worktree_id = ? WHERE id = ?", worktreeID, sessionID); err != nil {
		t.Fatalf("attach worktree to session: %v", err)
	}
	if err := os.Rename(oldWorkspace, newWorkspace); err != nil {
		t.Fatalf("Rename workspace: %v", err)
	}

	rebound, err := store.RebindWorkspace(ctx, oldWorkspace, newWorkspace)
	if err != nil {
		t.Fatalf("RebindWorkspace: %v", err)
	}
	canonicalNewWorktree, err := config.CanonicalWorkspaceRoot(newWorktree)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot newWorktree: %v", err)
	}
	var storedWorktreeRoot string
	if err := store.db.QueryRowContext(ctx, "SELECT canonical_root_path FROM worktrees WHERE id = ?", worktreeID).Scan(&storedWorktreeRoot); err != nil {
		t.Fatalf("scan rebound worktree root: %v", err)
	}
	if storedWorktreeRoot != canonicalNewWorktree {
		t.Fatalf("stored worktree root = %q, want %q", storedWorktreeRoot, canonicalNewWorktree)
	}
	target, err := store.ResolveSessionExecutionTarget(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorktreeID != worktreeID {
		t.Fatalf("target worktree id = %q, want %q", target.WorktreeID, worktreeID)
	}
	if target.WorktreeRoot != canonicalNewWorktree {
		t.Fatalf("target worktree root = %q, want %q", target.WorktreeRoot, canonicalNewWorktree)
	}
	if target.EffectiveWorkdir != canonicalNewWorktree {
		t.Fatalf("effective workdir = %q, want %q", target.EffectiveWorkdir, canonicalNewWorktree)
	}
	reopened, err := session.OpenByID(cfg.PersistenceRoot, sessionID, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if got := reopened.Meta().WorkspaceRoot; got != rebound.CanonicalRoot {
		t.Fatalf("reopened workspace root = %q, want %q", got, rebound.CanonicalRoot)
	}
}

func TestRebindWorkspaceNormalizesUniqueConflictRace(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	oldWorkspace := t.TempDir()
	otherWorkspace := t.TempDir()
	newWorkspace := filepath.Join(t.TempDir(), "workspace-target")
	if err := os.MkdirAll(newWorkspace, 0o755); err != nil {
		t.Fatalf("MkdirAll newWorkspace: %v", err)
	}
	t.Setenv("HOME", home)

	cfg, err := config.Load(oldWorkspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load oldWorkspace: %v", err)
	}
	otherCfg, err := config.Load(otherWorkspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load otherWorkspace: %v", err)
	}
	storeA, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("Open storeA: %v", err)
	}
	defer func() { _ = storeA.Close() }()
	storeB, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("Open storeB: %v", err)
	}
	defer func() { _ = storeB.Close() }()

	oldBinding, err := storeA.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding oldWorkspace: %v", err)
	}
	if _, err := storeA.RegisterWorkspaceBinding(ctx, otherCfg.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterWorkspaceBinding otherWorkspace: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	rebindWorkspaceBeforeUpdateHook = func() {
		close(started)
		<-release
	}
	t.Cleanup(func() { rebindWorkspaceBeforeUpdateHook = nil })

	errCh := make(chan error, 1)
	go func() {
		_, err := storeA.RebindWorkspace(ctx, oldWorkspace, newWorkspace)
		errCh <- err
	}()
	<-started
	if _, err := storeB.AttachWorkspaceToProject(ctx, oldBinding.ProjectID, newWorkspace); err != nil {
		close(release)
		t.Fatalf("AttachWorkspaceToProject competing bind: %v", err)
	}
	close(release)
	err = <-errCh
	if !errors.Is(err, ErrWorkspaceAlreadyBound) {
		t.Fatalf("RebindWorkspace race error = %v, want ErrWorkspaceAlreadyBound", err)
	}
	resolved, err := storeA.EnsureWorkspaceBinding(ctx, oldWorkspace)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding oldWorkspace after race: %v", err)
	}
	if resolved.WorkspaceID != oldBinding.WorkspaceID {
		t.Fatalf("resolved old workspace id after race = %q, want %q", resolved.WorkspaceID, oldBinding.WorkspaceID)
	}
	newResolved, err := storeA.EnsureWorkspaceBinding(ctx, newWorkspace)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding newWorkspace after race: %v", err)
	}
	if newResolved.ProjectID != oldBinding.ProjectID {
		t.Fatalf("new workspace project id after race = %q, want %q", newResolved.ProjectID, oldBinding.ProjectID)
	}
}

func TestInsertWorkspaceBindingAllowsSameCanonicalRootAcrossProjects(t *testing.T) {
	ctx := context.Background()
	store, cfg := newMetadataTestStoreWithoutBinding(t)
	canonicalRoot, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	now := time.Now().UTC()
	winner, err := store.insertWorkspaceBinding(ctx, canonicalRoot, filepath.Base(canonicalRoot), "", filepath.Base(canonicalRoot), "project-winner", "workspace-winner", now, true)
	if err != nil {
		t.Fatalf("insertWorkspaceBinding winner: %v", err)
	}
	duplicateInProject, err := store.insertWorkspaceBinding(ctx, canonicalRoot, filepath.Base(canonicalRoot), "", filepath.Base(canonicalRoot), winner.ProjectID, "workspace-duplicate", now, true)
	if err != nil {
		t.Fatalf("insertWorkspaceBinding duplicate in project: %v", err)
	}
	if duplicateInProject.WorkspaceID != winner.WorkspaceID {
		t.Fatalf("duplicate in project workspace id = %q, want %q", duplicateInProject.WorkspaceID, winner.WorkspaceID)
	}
	second, err := store.insertWorkspaceBinding(ctx, canonicalRoot, filepath.Base(canonicalRoot), "", filepath.Base(canonicalRoot), "project-second", "workspace-second", now, true)
	if err != nil {
		t.Fatalf("insertWorkspaceBinding second: %v", err)
	}
	if second.ProjectID == winner.ProjectID || second.WorkspaceID == winner.WorkspaceID {
		t.Fatalf("second binding reused winner identity: got %+v winner %+v", second, winner)
	}
	var projectCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&projectCount); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if projectCount != 2 {
		t.Fatalf("project count = %d, want 2", projectCount)
	}
	var workspaceCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM workspaces").Scan(&workspaceCount); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if workspaceCount != 2 {
		t.Fatalf("workspace count = %d, want 2", workspaceCount)
	}
	if _, err := store.EnsureWorkspaceBinding(ctx, cfg.WorkspaceRoot); !errors.Is(err, serverapi.ErrWorkspaceBindingAmbiguous) {
		t.Fatalf("EnsureWorkspaceBinding after duplicate-path inserts error = %v, want ErrWorkspaceBindingAmbiguous", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE id = ?", winner.ProjectID).Scan(&projectCount); err != nil {
		t.Fatalf("count winner project: %v", err)
	}
	if projectCount != 1 {
		t.Fatalf("winner project unexpectedly deleted")
	}
	var duplicatePathCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM workspaces WHERE canonical_root_path = ?", canonicalRoot).Scan(&duplicatePathCount); err != nil {
		t.Fatalf("count duplicate canonical roots: %v", err)
	}
	if duplicatePathCount != 2 {
		t.Fatalf("duplicate path count = %d, want 2", duplicatePathCount)
	}
}

func TestRegisterWorkspaceBindingConvergesUnderConcurrentFirstRegistration(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	storeA, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("Open storeA: %v", err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("Open storeB: %v", err)
	}
	t.Cleanup(func() { _ = storeB.Close() })

	barrier := make(chan struct{})
	var once sync.Once
	var reached atomic.Int32
	registerWorkspaceBindingAfterLookupMissHook = func() {
		if reached.Add(1) == 2 {
			once.Do(func() { close(barrier) })
		}
		<-barrier
	}
	t.Cleanup(func() {
		registerWorkspaceBindingAfterLookupMissHook = nil
		once.Do(func() { close(barrier) })
	})

	results := make(chan Binding, 2)
	errs := make(chan error, 2)
	run := func(store *Store) {
		binding, err := store.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
		if err != nil {
			errs <- err
			return
		}
		results <- binding
	}
	go run(storeA)
	go run(storeB)

	bindings := make([]Binding, 0, 2)
	for len(bindings) < 2 {
		select {
		case err := <-errs:
			t.Fatalf("RegisterWorkspaceBinding concurrent call: %v", err)
		case binding := <-results:
			bindings = append(bindings, binding)
		}
	}
	if bindings[0].ProjectID != bindings[1].ProjectID || bindings[0].WorkspaceID != bindings[1].WorkspaceID {
		t.Fatalf("concurrent bindings diverged: %+v vs %+v", bindings[0], bindings[1])
	}
	resolved, err := storeA.EnsureWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding after concurrent registration: %v", err)
	}
	if resolved.ProjectID != bindings[0].ProjectID || resolved.WorkspaceID != bindings[0].WorkspaceID {
		t.Fatalf("resolved binding mismatch: got %+v want %+v", resolved, bindings[0])
	}
	var projectCount int
	if err := storeA.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&projectCount); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if projectCount != 1 {
		t.Fatalf("project count = %d, want 1", projectCount)
	}
	var workspaceCount int
	if err := storeA.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM workspaces").Scan(&workspaceCount); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if workspaceCount != 1 {
		t.Fatalf("workspace count = %d, want 1", workspaceCount)
	}
}

func TestInsertWorkspaceBindingRollsBackProjectOnWorkspaceFailure(t *testing.T) {
	ctx := context.Background()
	store, cfg := newMetadataTestStoreWithoutBinding(t)
	canonicalRoot, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	insertWorkspaceBindingAfterProjectUpsertHook = cancel
	t.Cleanup(func() { insertWorkspaceBindingAfterProjectUpsertHook = nil })
	_, err = store.insertWorkspaceBinding(ctx, canonicalRoot, filepath.Base(canonicalRoot), "", filepath.Base(canonicalRoot), "project-cancelled", "workspace-cancelled", time.Now().UTC(), true)
	if err == nil {
		t.Fatal("expected insertWorkspaceBinding to fail after context cancellation")
	}
	var projectCount int
	if err := store.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM projects WHERE id = ?", "project-cancelled").Scan(&projectCount); err != nil {
		t.Fatalf("count cancelled project: %v", err)
	}
	if projectCount != 0 {
		t.Fatalf("expected cancelled project insert to roll back, got %d rows", projectCount)
	}
}

func TestImportSessionSnapshotRejectsSessionDirOutsidePersistenceRoot(t *testing.T) {
	ctx := context.Background()
	store, cfg, _ := newMetadataTestStore(t)
	outsideDir := t.TempDir()
	err := store.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: outsideDir,
		Meta: session.Meta{
			SessionID:          "session-outside",
			WorkspaceRoot:      cfg.WorkspaceRoot,
			WorkspaceContainer: filepath.Base(cfg.WorkspaceRoot),
			CreatedAt:          time.Now().UTC(),
			UpdatedAt:          time.Now().UTC(),
		},
	})
	if !errors.Is(err, ErrPathEscapesPersistenceRoot) {
		t.Fatalf("expected outside-persistence-root error, got %v", err)
	}
}
