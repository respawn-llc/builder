package metadata

import (
	"context"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func appendMetadataMessage(t *testing.T, store *session.Store, stepID string, role session.MessageRole, content string) session.EventRecord {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	step := stepID
	text := content
	record, receipt, err := eventLog.AppendRecord(&step, session.MessageRecord{
		Role:    role,
		Content: &text,
	})
	if err != nil || !receipt.Committed {
		t.Fatalf("append typed message: receipt=%+v error=%v", receipt, err)
	}
	return record
}

func metadataStringPointer(value string) *string {
	return &value
}

func TestEnsureWorkspaceBindingDoesNotRegisterUnknownWorkspace(t *testing.T) {
	t.Parallel()
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

func TestWorkspacePathKeyClaimAndRelease(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()

	var before sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT managed_worktree_path_key FROM workspaces WHERE id = ?`, binding.WorkspaceID).Scan(&before); err != nil {
		t.Fatalf("read initial workspace path key: %v", err)
	}
	if before.Valid {
		t.Fatalf("new workspace path key = %q, want NULL", before.String)
	}
	for _, candidate := range []sql.NullString{{}, {String: "", Valid: true}} {
		rows, err := store.queries.ClaimWorkspacePathKey(ctx, sqlitegen.ClaimWorkspacePathKeyParams{
			ManagedWorktreePathKey: candidate,
			ID:                     binding.WorkspaceID,
		})
		if err != nil {
			t.Fatalf("empty workspace path-key claim %v: %v", candidate, err)
		}
		if rows != 0 {
			t.Fatalf("empty workspace path-key claim %v updated %d rows, want 0", candidate, rows)
		}
	}

	claimed, err := store.ClaimWorkspacePathKey(ctx, binding.WorkspaceID, "source")
	if err != nil {
		t.Fatalf("claim workspace path key: %v", err)
	}
	if claimed != "source" {
		t.Fatalf("claimed workspace path key = %q, want source", claimed)
	}

	converged, err := store.ClaimWorkspacePathKey(ctx, binding.WorkspaceID, "other")
	if err != nil {
		t.Fatalf("converged workspace path key: %v", err)
	}
	if converged != "source" {
		t.Fatalf("converged workspace path key = %q, want source", converged)
	}

	if err := store.ReleaseWorkspacePathKey(ctx, binding.WorkspaceID, "other"); err != nil {
		t.Fatalf("release wrong workspace path key: %v", err)
	}
	var retained string
	if err := store.db.QueryRowContext(ctx, `SELECT managed_worktree_path_key FROM workspaces WHERE id = ?`, binding.WorkspaceID).Scan(&retained); err != nil {
		t.Fatalf("read retained workspace path key: %v", err)
	}
	if retained != "source" {
		t.Fatalf("retained workspace path key = %q, want source", retained)
	}
	if err := store.ReleaseWorkspacePathKey(ctx, binding.WorkspaceID, "source"); err != nil {
		t.Fatalf("release workspace path key: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT managed_worktree_path_key FROM workspaces WHERE id = ?`, binding.WorkspaceID).Scan(&before); err != nil {
		t.Fatalf("read released workspace path key: %v", err)
	}
	if before.Valid {
		t.Fatalf("released workspace path key = %q, want NULL", before.String)
	}
}

func TestWorkspacePathKeyClaimReportsCandidateCollision(t *testing.T) {
	t.Parallel()
	store, cfg := newMetadataTestStoreWithoutBinding(t)
	ctx := t.Context()
	first, err := store.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("register first workspace: %v", err)
	}
	secondRoot := t.TempDir()
	second, err := store.RegisterWorkspaceBinding(ctx, secondRoot)
	if err != nil {
		t.Fatalf("register second workspace: %v", err)
	}
	if _, err := store.ClaimWorkspacePathKey(ctx, first.WorkspaceID, "same"); err != nil {
		t.Fatalf("claim first workspace path key: %v", err)
	}
	if _, err := store.ClaimWorkspacePathKey(ctx, second.WorkspaceID, "same"); !errors.Is(err, ErrWorkspacePathKeyCandidateCollision) {
		t.Fatalf("second claim error = %v, want candidate collision", err)
	}
}

func TestWorkspacePathKeySurvivesRegistrationUpsertAndRebind(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	if _, err := store.ClaimWorkspacePathKey(ctx, binding.WorkspaceID, "stable"); err != nil {
		t.Fatalf("claim workspace path key: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	if err := store.queries.UpsertWorkspace(ctx, sqlitegen.UpsertWorkspaceParams{
		ID:                binding.WorkspaceID,
		ProjectID:         binding.ProjectID,
		CanonicalRootPath: binding.CanonicalRoot,
		GitMetadataJson:   "{}",
		CreatedAtUnixMs:   now,
		UpdatedAtUnixMs:   now,
	}); err != nil {
		t.Fatalf("upsert workspace: %v", err)
	}
	if got, err := store.ClaimWorkspacePathKey(ctx, binding.WorkspaceID, "different"); err != nil || got != "stable" {
		t.Fatalf("path key after upsert = %q, %v; want stable", got, err)
	}
	newRoot := t.TempDir()
	if _, err := store.RebindWorkspace(ctx, binding.CanonicalRoot, newRoot); err != nil {
		t.Fatalf("rebind workspace: %v", err)
	}
	if got, err := store.ClaimWorkspacePathKey(ctx, binding.WorkspaceID, "different"); err != nil || got != "stable" {
		t.Fatalf("path key after rebind = %q, %v; want stable", got, err)
	}
}

func TestWorktreeRecordPersistsImmutableCreationBaseCommitOID(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	oid := "creation-commit"
	root := t.TempDir()
	record := WorktreeRecord{
		ID:                    "worktree-creation-base",
		WorkspaceID:           binding.WorkspaceID,
		CanonicalRoot:         root,
		GitMetadataJSON:       "{}",
		CreationBaseCommitOID: &oid,
	}
	if err := store.UpsertWorktreeRecord(ctx, record); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	stored, err := store.GetWorktreeRecordByID(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if stored.CreationBaseCommitOID == nil || *stored.CreationBaseCommitOID != oid {
		t.Fatalf("creation base commit oid = %+v, want %q", stored.CreationBaseCommitOID, oid)
	}
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:                    "worktree-creation-base-conflict",
		WorkspaceID:           binding.WorkspaceID,
		CanonicalRoot:         root,
		GitMetadataJSON:       "{}",
		CreationBaseCommitOID: stringPointerForStoreTest("different-commit"),
	}); err == nil {
		t.Fatal("UpsertWorktreeRecord accepted a conflicting immutable creation base commit")
	}
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:              "worktree-creation-base",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   root,
		GitMetadataJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord without creation base should preserve immutable provenance: %v", err)
	}
}

func stringPointerForStoreTest(value string) *string {
	return &value
}

func TestResolveWorkspacePathLeavesNestedDirectoryUnbound(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	store, _ := newMetadataTestStoreWithoutBinding(t)

	if _, err := store.LookupWorkspaceBindingByID(context.Background(), "workspace-missing"); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("LookupWorkspaceBindingByID error = %v, want ErrWorkspaceNotRegistered", err)
	}
}

func TestAttachWorkspaceToProjectAllowsNestedPathAsSeparateWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "nested")
	other := t.TempDir()
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}

	store, cfg := newMetadataTestStoreForWorkspace(t, workspace)
	otherCfg := loadMetadataTestConfig(t, other, cfg.PersistenceRoot)

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
	t.Parallel()
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	soleBlockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, binding.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace sole: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, soleBlockers, "default_workspace")
	assertNoWorkspaceUnlinkBlocker(t, soleBlockers, "only_workspace")
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	defaultBlockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, binding.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace default: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, defaultBlockers, "default_workspace")
	assertNoWorkspaceUnlinkBlocker(t, defaultBlockers, "only_workspace")

	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	execSeed(t, store.db, "active source task", `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-active-workspace', 'link-1', 1, 1, 'BLD-1', 'Active', '', ?, ?, ?, json_object('source_workspace_snapshot', json_object('workspace_id', ?, 'display_name', ?, 'root_path', ?)))`, attached.WorkspaceID, now, now, attached.WorkspaceID, attached.WorkspaceName, attached.CanonicalRoot)
	insertTaskCurrentNode(t, store.db, "task-active-workspace", "node-agent", nil)

	activeTaskBlockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace active task: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, activeTaskBlockers, "non_terminal_tasks")

	insertTaskPendingApproval(t, store.db, "approval-pending-workspace", "task-active-workspace", "node-agent", nil, now)
	pendingApprovalBlockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspace pending approval transition: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, pendingApprovalBlockers, "non_terminal_tasks")
}

func TestUnlinkProjectWorkspaceBlocksReferencedWorkspaceDependencies(t *testing.T) {
	t.Run("executable current node", func(t *testing.T) {
		ctx := context.Background()
		store, _, binding := newMetadataTestStore(t)
		attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
		if err != nil {
			t.Fatalf("AttachWorkspaceToProject: %v", err)
		}
		now := time.Now().UTC().UnixMilli()
		seedWorkflowGraph(t, store.db, binding.ProjectID, now)
		execSeed(t, store.db, "task", workflowSeedTaskSQL, "task-executable-workspace", "link-1", 1, "BLD-1", now, now)
		execSeed(t, store.db, "session", `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-executable-workspace', ?, ?, 'projects/project/sessions/session-executable-workspace', ?, ?)`,
			binding.ProjectID, attached.WorkspaceID, now, now)
		execSeed(t, store.db, "current node", `INSERT INTO task_current_nodes (task_id, node_id, current_input_values_json, prior_node_values_json, session_id)
VALUES ('task-executable-workspace', 'node-agent', '{}', '{"transition_parameters":{}}', 'session-executable-workspace')`)

		blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
		if err != nil {
			t.Fatalf("UnlinkProjectWorkspace: %v", err)
		}
		assertWorkspaceUnlinkBlocker(t, blockers, "executable_current_nodes")
		assertWorkspaceRetainedAfterBlockedUnlink(t, store, ctx, binding.ProjectID, attached.WorkspaceID, retainedWorkspaceRecords{
			TaskID:    metadataStringPointer("task-executable-workspace"),
			SessionID: metadataStringPointer("session-executable-workspace"),
		})
	})

	for _, test := range []struct {
		name          string
		managed       bool
		createdBranch bool
	}{
		{name: "managed created branch", managed: true, createdBranch: true},
		{name: "managed existing ref", managed: true, createdBranch: false},
		{name: "unmanaged worktree", managed: false},
	} {
		t.Run("worktree dependency/"+test.name, func(t *testing.T) {
			ctx := context.Background()
			store, _, binding := newMetadataTestStore(t)
			attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
			if err != nil {
				t.Fatalf("AttachWorkspaceToProject: %v", err)
			}
			if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
				ID:              "managed-owned-worktree",
				WorkspaceID:     attached.WorkspaceID,
				CanonicalRoot:   t.TempDir(),
				Managed:         test.managed,
				CreatedBranch:   test.createdBranch,
				GitMetadataJSON: "{}",
			}); err != nil {
				t.Fatalf("UpsertWorktreeRecord: %v", err)
			}

			blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
			if err != nil {
				t.Fatalf("UnlinkProjectWorkspace: %v", err)
			}
			assertWorkspaceUnlinkBlocker(t, blockers, "managed_owned_worktrees")
			assertWorkspaceRetainedAfterBlockedUnlink(t, store, ctx, binding.ProjectID, attached.WorkspaceID, retainedWorkspaceRecords{})
			var worktreeCount int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktrees WHERE id = 'managed-owned-worktree'`).Scan(&worktreeCount); err != nil {
				t.Fatalf("count managed worktree: %v", err)
			}
			if worktreeCount != 1 {
				t.Fatalf("managed worktree count = %d, want 1", worktreeCount)
			}
		})
	}

	t.Run("missing history snapshot", func(t *testing.T) {
		ctx := context.Background()
		store, _, binding := newMetadataTestStore(t)
		attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
		if err != nil {
			t.Fatalf("AttachWorkspaceToProject: %v", err)
		}
		now := time.Now().UTC().UnixMilli()
		seedWorkflowGraph(t, store.db, binding.ProjectID, now)
		execSeed(t, store.db, "task", `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-missing-workspace-snapshot', 'link-1', 1, 1, 'BLD-1', 'Task', 'Body', ?, ?, ?, '{}')`, attached.WorkspaceID, now, now)
		insertTaskCurrentNode(t, store.db, "task-missing-workspace-snapshot", "node-done", nil)

		blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
		if err != nil {
			t.Fatalf("UnlinkProjectWorkspace: %v", err)
		}
		assertWorkspaceUnlinkBlocker(t, blockers, "missing_history_snapshot")
		assertWorkspaceRetainedAfterBlockedUnlink(t, store, ctx, binding.ProjectID, attached.WorkspaceID, retainedWorkspaceRecords{
			TaskID: metadataStringPointer("task-missing-workspace-snapshot"),
		})
	})

	t.Run("root source workspace snapshot may omit display name", func(t *testing.T) {
		ctx := context.Background()
		store, _, binding := newMetadataTestStore(t)
		root := string(filepath.Separator)
		if volume := filepath.VolumeName(t.TempDir()); volume != "" {
			root = volume + string(filepath.Separator)
		}
		attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, root)
		if err != nil {
			t.Fatalf("AttachWorkspaceToProject: %v", err)
		}
		now := time.Now().UTC().UnixMilli()
		seedWorkflowGraph(t, store.db, binding.ProjectID, now)
		execSeed(t, store.db, "terminal root task", `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-root-workspace', 'link-1', 1, 1, 'BLD-1', 'Root', 'Body', ?, ?, ?, json_object('source_workspace_snapshot', json_object('workspace_id', ?, 'display_name', '', 'root_path', ?)))`, attached.WorkspaceID, now, now, attached.WorkspaceID, root)
		insertTaskCurrentNode(t, store.db, "task-root-workspace", "node-done", nil)

		blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
		if err != nil {
			t.Fatalf("UnlinkProjectWorkspace: %v", err)
		}
		assertNoWorkspaceUnlinkBlocker(t, blockers, "missing_history_snapshot")
	})

	t.Run("missing retained session snapshot", func(t *testing.T) {
		ctx := context.Background()
		store, _, binding := newMetadataTestStore(t)
		attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
		if err != nil {
			t.Fatalf("AttachWorkspaceToProject: %v", err)
		}
		now := time.Now().UTC().UnixMilli()
		execSeed(t, store.db, "session missing workspace snapshot", `INSERT INTO sessions (
	id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms, metadata_json
)
VALUES ('session-missing-workspace-snapshot', ?, ?, 'session-missing-workspace-snapshot', ?, ?, '{}')`,
			binding.ProjectID, attached.WorkspaceID, now, now)

		blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, attached.WorkspaceID)
		if err != nil {
			t.Fatalf("UnlinkProjectWorkspace: %v", err)
		}
		assertWorkspaceUnlinkBlocker(t, blockers, "missing_history_snapshot")
		assertWorkspaceRetainedAfterBlockedUnlink(t, store, ctx, binding.ProjectID, attached.WorkspaceID, retainedWorkspaceRecords{
			SessionID: metadataStringPointer("session-missing-workspace-snapshot"),
		})
	})
}

type retainedWorkspaceRecords struct {
	TaskID    *string
	SessionID *string
}

func assertWorkspaceRetainedAfterBlockedUnlink(t *testing.T, store *Store, ctx context.Context, projectID string, workspaceID string, retained retainedWorkspaceRecords) {
	t.Helper()
	if _, err := store.GetWorkspaceByID(ctx, workspaceID); err != nil {
		t.Fatalf("workspace after blocked unlink: %v", err)
	}
	var bindingCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE project_id = ? AND id = ?`, projectID, workspaceID).Scan(&bindingCount); err != nil {
		t.Fatalf("count retained workspace binding: %v", err)
	}
	if bindingCount != 1 {
		t.Fatalf("retained workspace binding count = %d, want 1", bindingCount)
	}
	if retained.TaskID != nil {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ?`, *retained.TaskID).Scan(&count); err != nil {
			t.Fatalf("count retained task: %v", err)
		}
		if count != 1 {
			t.Fatalf("retained task count = %d, want 1", count)
		}
	}
	if retained.SessionID != nil {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, *retained.SessionID).Scan(&count); err != nil {
			t.Fatalf("count retained session: %v", err)
		}
		if count != 1 {
			t.Fatalf("retained session count = %d, want 1", count)
		}
	}
}

func TestWorkspaceUnlinkCommitPrefersAuthoritativeBlockersOverChangedSessionSet(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	otherStore, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("open concurrent metadata store: %v", err)
	}
	t.Cleanup(func() { _ = otherStore.Close() })
	seedWorkflowGraph(t, store.db, binding.ProjectID, time.Now().UTC().UnixMilli())

	blockers, err := store.UnlinkProjectWorkspaceWithRuntimeBlockers(ctx, binding.ProjectID, attached.WorkspaceID, nil,
		func(ctx context.Context, preparedSessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
			if len(preparedSessionIDs) != 0 {
				return nil, func() {}, nil
			}
			mutated := make(chan error, 1)
			go func() {
				mutated <- addMetadataRaceSessionAndActiveTask(ctx, otherStore, cfg, binding, "unlink-race", attached.CanonicalRoot, attached.WorkspaceID)
			}()
			if err := <-mutated; err != nil {
				return nil, nil, err
			}
			return nil, func() {}, nil
		},
	)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspaceWithRuntimeBlockers: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, blockers, "non_terminal_tasks")
	if _, err := store.GetWorkspaceByID(ctx, attached.WorkspaceID); err != nil {
		t.Fatalf("workspace should remain after authoritative blocker: %v", err)
	}
}

func TestWorkspaceUnlinkReturnsStaticAndRuntimeBlockers(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	blockers, err := store.UnlinkProjectWorkspaceWithRuntimeBlockers(
		ctx,
		binding.ProjectID,
		binding.WorkspaceID,
		nil,
		func(context.Context, []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
			return []serverapi.ProjectWorkspaceUnlinkBlocker{{
				Code:    "active_sessions",
				Message: "Active runtime sessions still depend on this workspace.",
				Count:   1,
			}}, func() {}, nil
		},
	)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspaceWithRuntimeBlockers: %v", err)
	}
	assertWorkspaceUnlinkBlocker(t, blockers, "default_workspace")
	assertWorkspaceUnlinkBlocker(t, blockers, "active_sessions")
}

func TestWorkspaceUnlinkCommitCombinesStaticAndRuntimeBlockers(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	seedWorkflowGraph(t, store.db, binding.ProjectID, time.Now().UTC().UnixMilli())
	otherStore, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("open concurrent metadata store: %v", err)
	}
	t.Cleanup(func() { _ = otherStore.Close() })
	callbackCalls := 0
	blockers, err := store.UnlinkProjectWorkspaceWithRuntimeBlockers(
		ctx,
		binding.ProjectID,
		attached.WorkspaceID,
		nil,
		func(ctx context.Context, preparedSessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
			callbackCalls++
			if callbackCalls == 1 {
				if len(preparedSessionIDs) != 0 {
					return nil, nil, fmt.Errorf("prepared session IDs = %v, want none", preparedSessionIDs)
				}
				if err := addMetadataRaceSessionAndActiveTask(ctx, otherStore, cfg, binding, "commit-runtime-blocker", attached.CanonicalRoot, attached.WorkspaceID); err != nil {
					return nil, nil, err
				}
				return nil, func() {}, nil
			}
			if len(preparedSessionIDs) == 0 {
				return nil, nil, errors.New("commit runtime blocker check received no session IDs")
			}
			return []serverapi.ProjectWorkspaceUnlinkBlocker{{
				Code:    "active_sessions",
				Message: "Active runtime sessions still depend on this workspace.",
				Count:   1,
			}}, func() {}, nil
		},
	)
	if err != nil {
		t.Fatalf("UnlinkProjectWorkspaceWithRuntimeBlockers: %v", err)
	}
	if callbackCalls != 2 {
		t.Fatalf("runtime blocker callback calls = %d, want 2", callbackCalls)
	}
	assertWorkspaceUnlinkBlocker(t, blockers, "non_terminal_tasks")
	assertWorkspaceUnlinkBlocker(t, blockers, "active_sessions")
}

func TestWorkspaceUnlinkCommitInvalidatesChangedSessionSetWithoutBlocker(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	_, err = store.UnlinkProjectWorkspaceWithRuntimeBlockers(ctx, binding.ProjectID, attached.WorkspaceID, nil,
		func(ctx context.Context, preparedSessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
			if len(preparedSessionIDs) != 0 {
				return nil, nil, fmt.Errorf("prepared session ids = %v, want none", preparedSessionIDs)
			}
			sessionID, err := addMetadataRaceSession(ctx, store, cfg, binding, "unlink-session-set")
			if err != nil {
				return nil, nil, err
			}
			if err := store.UpdateSessionExecutionTarget(ctx, SessionExecutionTargetUpdate{
				SessionID: sessionID,
				Workspace: &SessionExecutionTargetUpdateWorkspace{ID: attached.WorkspaceID},
			}); err != nil {
				return nil, nil, err
			}
			return nil, func() {}, nil
		},
	)
	if !errors.Is(err, serverapi.ErrWorkspaceDetachConflict) {
		t.Fatalf("UnlinkProjectWorkspaceWithRuntimeBlockers error = %v, want detach conflict", err)
	}
	workspace, err := store.GetWorkspaceByID(ctx, attached.WorkspaceID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID after invalidation: %v", err)
	}
	if workspace.ProjectID != binding.ProjectID {
		t.Fatalf("workspace after invalidated unlink = %+v, want binding retained", workspace)
	}
}

func TestWorkspaceUnlinkRuntimePreparationDoesNotBlockUnrelatedMetadata(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	other, err := store.RegisterWorkspaceBinding(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding other project: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	unlinkDone := make(chan error, 1)
	go func() {
		_, err := store.UnlinkProjectWorkspaceWithRuntimeBlockers(ctx, binding.ProjectID, attached.WorkspaceID, nil,
			func(context.Context, []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
				close(started)
				<-release
				return nil, func() {}, nil
			},
		)
		unlinkDone <- err
	}()
	<-started

	if _, err := store.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects during workspace unlink runtime preparation: %v", err)
	}
	if err := store.UpdateProjectMetadata(ctx, other.ProjectID, "Other project", other.ProjectKey); err != nil {
		t.Fatalf("UpdateProjectMetadata during workspace unlink runtime preparation: %v", err)
	}
	select {
	case err := <-unlinkDone:
		t.Fatalf("workspace unlink completed while runtime preparation was blocked: %v", err)
	default:
	}
	close(release)
	if err := <-unlinkDone; err != nil {
		t.Fatalf("UnlinkProjectWorkspaceWithRuntimeBlockers: %v", err)
	}
}

func TestUnlinkProjectWorkspacePreservesTerminalHistoryWithoutWorktreeDependency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	execSeed(t, store.db, "terminal source task", `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-terminal-workspace', 'link-1', 1, 1, 'BLD-1', 'Terminal', '', ?, ?, ?, json_object('source_workspace_snapshot', json_object('workspace_id', ?, 'display_name', ?, 'root_path', ?)))`, attached.WorkspaceID, now, now, attached.WorkspaceID, attached.WorkspaceName, attached.CanonicalRoot)
	insertTaskCurrentNode(t, store.db, "task-terminal-workspace", "node-done", nil)
	execSeed(t, store.db, "historical workspace session", `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, previous_session_id, parent_agent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json)
VALUES ('session-terminal-workspace', ?, ?, ?, 'Historical', '', '', NULL, NULL, ?, ?, 0, 1, 1, '.', '{}', '{}', '{}', json_object('workspace_root', ?, 'workspace_container', ?))`, binding.ProjectID, attached.WorkspaceID, filepath.ToSlash(filepath.Join("projects", binding.ProjectID, "sessions", "session-terminal-workspace")), now, now, attached.CanonicalRoot, "sessions")

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
	var metadataJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), source_workspace_id, metadata_json FROM tasks WHERE id = 'task-terminal-workspace'`).Scan(&taskCount, &sourceWorkspaceID, &metadataJSON); err != nil {
		t.Fatalf("scan preserved task: %v", err)
	}
	if taskCount != 1 || sourceWorkspaceID.Valid || !strings.Contains(metadataJSON, attached.CanonicalRoot) {
		t.Fatalf("preserved task count/source/metadata = %d/%v/%s", taskCount, sourceWorkspaceID, metadataJSON)
	}
	var sessionWorkspaceID sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT workspace_id FROM sessions WHERE id = 'session-terminal-workspace'`).Scan(&sessionWorkspaceID); err != nil {
		t.Fatalf("scan preserved session: %v", err)
	}
	if sessionWorkspaceID.Valid {
		t.Fatalf("preserved session workspace = %v, want null", sessionWorkspaceID)
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
	t.Parallel()
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

func assertNoWorkspaceUnlinkBlocker(t *testing.T, blockers []serverapi.ProjectWorkspaceUnlinkBlocker, code string) {
	t.Helper()
	for _, blocker := range blockers {
		if blocker.Code == code {
			t.Fatalf("blockers = %+v, must not contain code %q", blockers, code)
		}
	}
}

func addMetadataRaceSessionAndActiveTask(
	ctx context.Context,
	store *Store,
	cfg config.App,
	binding Binding,
	suffix string,
	workspaceRoot string,
	sourceWorkspaceID string,
) error {
	projectSessionsDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	if _, err := session.Create(
		projectSessionsDir,
		filepath.Base(projectSessionsDir),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	); err != nil {
		return fmt.Errorf("create concurrent session: %w", err)
	}
	now := time.Now().UTC().UnixMilli()
	taskID := "task-" + suffix
	if _, err := store.db.ExecContext(ctx, workflowSeedTaskSQL, taskID, "link-1", 1, "BLD-1", now, now); err != nil {
		return fmt.Errorf("create concurrent task: %w", err)
	}
	if _, err := store.db.ExecContext(ctx, insertTaskCurrentNodeSQL, taskCurrentNodeArgs(taskID, "node-agent", nil)...); err != nil {
		return fmt.Errorf("create concurrent task current node: %w", err)
	}
	if strings.TrimSpace(sourceWorkspaceID) == "" {
		return nil
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE tasks
SET source_workspace_id = ?,
    metadata_json = json_object(
        'source_workspace_snapshot',
        json_object('root_path', ?, 'display_name', 'Concurrent workspace')
    )
WHERE id = ?`,
		sourceWorkspaceID,
		workspaceRoot,
		taskID,
	); err != nil {
		return fmt.Errorf("set concurrent task source workspace: %w", err)
	}
	return nil
}

func addMetadataRaceSession(ctx context.Context, store *Store, cfg config.App, binding Binding, suffix string) (string, error) {
	projectSessionsDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	created, err := session.Create(
		projectSessionsDir,
		filepath.Base(projectSessionsDir),
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		return "", fmt.Errorf("create concurrent session: %w", err)
	}
	if err := created.SetName(suffix); err != nil {
		return "", fmt.Errorf("persist concurrent session: %w", err)
	}
	return created.Meta().SessionID, nil
}

func TestRebindWorkspacePreservesWorkspaceIdentity(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	oldWorkspace := t.TempDir()
	otherWorkspace := t.TempDir()
	projectWorkspace := t.TempDir()
	missingWorkspace := filepath.Join(t.TempDir(), "missing")

	store, cfg := newMetadataTestStoreForWorkspace(t, oldWorkspace)
	otherCfg := loadMetadataTestConfig(t, otherWorkspace, cfg.PersistenceRoot)
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
	t.Parallel()
	oldWorkspace := t.TempDir()
	sharedTarget := t.TempDir()

	store, cfg := newMetadataTestStoreForWorkspace(t, oldWorkspace)
	targetCfg := loadMetadataTestConfig(t, sharedTarget, cfg.PersistenceRoot)
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
	t.Parallel()
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

func planAndCommitSessionWorkspaceRetarget(t *testing.T, ctx context.Context, store *Store, sessionID string, workspaceRoot string) Binding {
	t.Helper()
	plan, err := store.PlanSessionWorkspaceRetarget(ctx, SessionWorkspaceRetargetRequest{
		SessionID:     sessionID,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	result, err := store.CommitSessionWorkspaceRetarget(ctx, plan, time.Now().UTC())
	if err != nil {
		t.Fatalf("CommitSessionWorkspaceRetarget: %v", err)
	}
	return result.Binding
}

func TestCommitSessionWorkspaceRetargetAttachesTargetAndUpdatesSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	store, cfg, bindingA := newMetadataTestStoreForBoundWorkspace(t, workspaceA)
	sess, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), bindingA.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...,
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
	if err := store.UpdateSessionExecutionTarget(ctx, SessionExecutionTargetUpdate{SessionID: sess.Meta().SessionID, Workspace: &SessionExecutionTargetUpdateWorkspace{ID: bindingA.WorkspaceID}, Worktree: &SessionExecutionTargetUpdateWorktree{ID: "worktree-a"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget before retarget: %v", err)
	}
	if err := sess.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/a"),
			WorktreePath:  canonicalWorktreeRootA,
			WorkspaceRoot: cfg.WorkspaceRoot,
			EffectiveCwd:  filepath.Join(canonicalWorktreeRootA, "pkg"),
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState before retarget: %v", err)
	}

	retargeted := planAndCommitSessionWorkspaceRetarget(t, ctx, store, sess.Meta().SessionID, workspaceB)
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
	if target.Worktree != nil {
		t.Fatalf("target worktree = %+v, want nil after workspace retarget", target.Worktree)
	}
	if target.CwdRelpath != "." {
		t.Fatalf("target cwd relpath = %q, want . after workspace retarget", target.CwdRelpath)
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

func TestCommitSessionWorkspaceRetargetClearsSameWorkspaceStaleWorktreeTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	if err := sess.SetName("stale worktree recovery"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	worktreeRoot := filepath.Join(cfg.WorkspaceRoot, "deleted-task-worktree")
	worktreeSubdir := filepath.Join(worktreeRoot, "pkg")
	if err := os.MkdirAll(worktreeSubdir, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree subdir: %v", err)
	}
	canonicalWorktreeRoot := createMetadataTestWorktree(t, ctx, store, binding.WorkspaceID, "worktree-stale", worktreeRoot)
	if err := store.UpdateSessionExecutionTarget(ctx, SessionExecutionTargetUpdate{
		SessionID:  sess.Meta().SessionID,
		Workspace:  &SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID},
		Worktree:   &SessionExecutionTargetUpdateWorktree{ID: "worktree-stale"},
		CwdRelpath: "pkg",
	}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget before retarget: %v", err)
	}
	if err := sess.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("task/stale"),
			WorktreePath:  canonicalWorktreeRoot,
			WorkspaceRoot: cfg.WorkspaceRoot,
			EffectiveCwd:  filepath.Join(canonicalWorktreeRoot, "pkg"),
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState before retarget: %v", err)
	}
	if err := os.RemoveAll(canonicalWorktreeRoot); err != nil {
		t.Fatalf("RemoveAll stale worktree root: %v", err)
	}

	retargeted := planAndCommitSessionWorkspaceRetarget(t, ctx, store, sess.Meta().SessionID, cfg.WorkspaceRoot)
	if retargeted.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("retargeted workspace id = %q, want %q", retargeted.WorkspaceID, binding.WorkspaceID)
	}
	var storedWorktreeID sql.NullString
	if err := store.db.QueryRowContext(ctx, "SELECT worktree_id FROM sessions WHERE id = ?", sess.Meta().SessionID).Scan(&storedWorktreeID); err != nil {
		t.Fatalf("scan session worktree_id: %v", err)
	}
	if storedWorktreeID.Valid {
		t.Fatalf("stored worktree_id = %+v, want SQL NULL", storedWorktreeID)
	}
	target, err := store.ResolveSessionExecutionTarget(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.Worktree != nil {
		t.Fatalf("target worktree = %+v, want nil", target.Worktree)
	}
	if target.CwdRelpath != "." {
		t.Fatalf("target cwd_relpath = %q, want .", target.CwdRelpath)
	}
	if target.EffectiveWorkdir != retargeted.CanonicalRoot {
		t.Fatalf("target effective workdir = %q, want %q", target.EffectiveWorkdir, retargeted.CanonicalRoot)
	}
	reopened, err := session.OpenByID(cfg.PersistenceRoot, sess.Meta().SessionID, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if reopened.Meta().WorkspaceRoot != retargeted.CanonicalRoot {
		t.Fatalf("reopened workspace root = %q, want %q", reopened.Meta().WorkspaceRoot, retargeted.CanonicalRoot)
	}
	if reopened.Meta().WorktreeReminder != nil {
		t.Fatalf("reopened worktree reminder = %+v, want nil", reopened.Meta().WorktreeReminder)
	}
}

func TestResolvePersistedSessionRoundTripsRequiredStructuredMetadata(t *testing.T) {
	t.Parallel()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	reminder := &session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/reminder"),
			WorktreePath:  "/tmp/wt-reminder",
			WorkspaceRoot: cfg.WorkspaceRoot,
			EffectiveCwd:  "/tmp/wt-reminder/pkg",
		},
	}
	if err := sess.SetWorktreeReminderState(reminder); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	reminder = session.CloneWorktreeReminderState(sess.Meta().WorktreeReminder)
	goal, _, err := sess.SetGoal("ship durable goal metadata", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	buffers := []session.InputDraftRecoveryBuffer{{
		Kind: "queued_input",
		Text: "recover this draft",
	}}
	if err := sess.SetInputDraftRecovery("draft", buffers); err != nil {
		t.Fatalf("SetInputDraftRecovery: %v", err)
	}
	recovery := session.PendingModelRecovery{
		RecoveryID:             "recovery-1",
		StepID:                 "step-1",
		Reason:                 "interrupted",
		CreatedAt:              time.Now().UTC().Round(0),
		OutstandingToolCallIDs: []string{"tool-call-1", "tool-call-2"},
	}
	if err := sess.SetPendingModelRecovery(recovery); err != nil {
		t.Fatalf("SetPendingModelRecovery: %v", err)
	}
	appendMetadataMessage(t, sess, "step-2", session.MessageRoleUser, "establish conversation")

	record, err := store.ResolvePersistedSession(t.Context(), sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	meta := record.Meta
	if meta == nil {
		t.Fatal("resolved metadata is nil")
	}
	if !meta.ConversationEstablished {
		t.Fatal("conversation establishment did not round-trip through SQLite")
	}
	if meta.WorktreeReminder == nil || !session.WorktreeReminderStateEqual(*meta.WorktreeReminder, *reminder) {
		t.Fatalf("worktree reminder = %+v, want %+v", meta.WorktreeReminder, reminder)
	}
	if meta.Goal == nil || *meta.Goal != goal {
		t.Fatalf("goal = %+v, want %+v", meta.Goal, goal)
	}
	if len(meta.InputDraftRecoveryBuffers) != 1 || meta.InputDraftRecoveryBuffers[0] != buffers[0] {
		t.Fatalf("recovery buffers = %+v, want %+v", meta.InputDraftRecoveryBuffers, buffers)
	}
	persistedRecovery := meta.PendingModelRecovery
	if persistedRecovery == nil {
		t.Fatal("pending model recovery did not round-trip through SQLite")
	}
	if persistedRecovery.RecoveryID != recovery.RecoveryID ||
		persistedRecovery.StepID != recovery.StepID ||
		persistedRecovery.Reason != recovery.Reason ||
		!persistedRecovery.CreatedAt.Equal(recovery.CreatedAt) ||
		len(persistedRecovery.OutstandingToolCallIDs) != len(recovery.OutstandingToolCallIDs) {
		t.Fatalf("pending model recovery = %+v, want %+v", persistedRecovery, recovery)
	}
	for i, toolCallID := range recovery.OutstandingToolCallIDs {
		if persistedRecovery.OutstandingToolCallIDs[i] != toolCallID {
			t.Fatalf("outstanding tool call ids = %+v, want %+v", persistedRecovery.OutstandingToolCallIDs, recovery.OutstandingToolCallIDs)
		}
	}
}

func TestMissingEventLogRepairOccursAtEventUse(t *testing.T) {
	t.Parallel()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	appendMetadataMessage(t, sess, "step-1", session.MessageRoleUser, "establish conversation")
	eventsPath := filepath.Join(sess.Dir(), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events artifact: %v", err)
	}

	repaired, err := session.OpenByID(
		cfg.PersistenceRoot,
		sess.Meta().SessionID,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID repair: %v", err)
	}
	if _, err := os.Stat(eventsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata-only open repaired missing event log: %v", err)
	}
	repairedEventLog, err := repaired.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize missing event log: %v", err)
	}
	if mustEventLogRevision(repairedEventLog) != 0 {
		t.Fatalf("repaired event-log revision = %d, want fresh empty conversation", mustEventLogRevision(repairedEventLog))
	}
	if mustEventLogFreshness(repairedEventLog) != session.ConversationFreshnessFresh {
		t.Fatalf("repaired freshness = %q, want fresh", mustEventLogFreshness(repairedEventLog))
	}

	reopened, err := session.OpenByID(
		cfg.PersistenceRoot,
		sess.Meta().SessionID,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID reopen: %v", err)
	}
	reopenedEventLog, err := reopened.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize reopened event log: %v", err)
	}
	if mustEventLogRevision(reopenedEventLog) != 0 {
		t.Fatalf("reopened event-log revision = %d, want fresh empty conversation", mustEventLogRevision(reopenedEventLog))
	}
	if mustEventLogFreshness(reopenedEventLog) != session.ConversationFreshnessFresh {
		t.Fatalf("reopened freshness = %q, want fresh", mustEventLogFreshness(reopenedEventLog))
	}
}

func TestRebindWorkspaceRetargetsDescendantWorktrees(t *testing.T) {
	t.Parallel()
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
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
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
	if target.Worktree == nil || target.Worktree.ID != worktreeID {
		t.Fatalf("target worktree = %+v, want %q", target.Worktree, worktreeID)
	}
	if target.Worktree == nil || target.Worktree.Root != canonicalNewWorktree {
		t.Fatalf("target worktree root = %+v, want %q", target.Worktree, canonicalNewWorktree)
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
	oldWorkspace := t.TempDir()
	otherWorkspace := t.TempDir()
	newWorkspace := filepath.Join(t.TempDir(), "workspace-target")
	if err := os.MkdirAll(newWorkspace, 0o755); err != nil {
		t.Fatalf("MkdirAll newWorkspace: %v", err)
	}
	persistenceRoot := filepath.Join(t.TempDir(), "persistence")
	cfg := loadMetadataTestConfig(t, oldWorkspace, persistenceRoot)
	otherCfg := loadMetadataTestConfig(t, otherWorkspace, persistenceRoot)
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

func TestRebindWorkspaceExpectedBindingRejectsOldRootReuse(t *testing.T) {
	ctx := context.Background()
	store, cfg, prepared := newMetadataTestStore(t)
	otherStore, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("open second metadata store: %v", err)
	}
	t.Cleanup(func() { _ = otherStore.Close() })

	movedRoot := t.TempDir()
	requestedRoot := t.TempDir()
	moved, err := otherStore.RebindWorkspace(ctx, cfg.WorkspaceRoot, movedRoot)
	if err != nil {
		t.Fatalf("move prepared workspace: %v", err)
	}
	if moved.WorkspaceID != prepared.WorkspaceID {
		t.Fatalf("moved workspace id = %q, want %q", moved.WorkspaceID, prepared.WorkspaceID)
	}
	replacement, err := otherStore.AttachWorkspaceToProject(ctx, prepared.ProjectID, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("reuse old workspace root: %v", err)
	}
	if replacement.WorkspaceID == prepared.WorkspaceID {
		t.Fatalf("replacement workspace id = %q, want a new identity", replacement.WorkspaceID)
	}

	_, err = store.RebindWorkspaceWithExpectedBinding(
		ctx,
		cfg.WorkspaceRoot,
		requestedRoot,
		prepared.ProjectID,
		prepared.WorkspaceID,
	)
	if err == nil {
		t.Fatal("stale expected workspace binding rebind succeeded")
	}

	currentMoved, err := store.EnsureWorkspaceBinding(ctx, movedRoot)
	if err != nil {
		t.Fatalf("resolve moved workspace: %v", err)
	}
	if currentMoved.WorkspaceID != prepared.WorkspaceID {
		t.Fatalf("moved workspace id after stale request = %q, want %q", currentMoved.WorkspaceID, prepared.WorkspaceID)
	}
	currentReplacement, err := store.EnsureWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("resolve replacement workspace: %v", err)
	}
	if currentReplacement.WorkspaceID != replacement.WorkspaceID {
		t.Fatalf("replacement workspace id after stale request = %q, want %q", currentReplacement.WorkspaceID, replacement.WorkspaceID)
	}
	if _, err := store.EnsureWorkspaceBinding(ctx, requestedRoot); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("requested root after stale request error = %v, want ErrWorkspaceNotRegistered", err)
	}
}

func TestRegisterWorkspaceBindingConvergesUnderConcurrentFirstRegistration(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	cfg := loadMetadataTestConfig(t, workspace, filepath.Join(t.TempDir(), "persistence"))
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
	t.Parallel()
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

func TestSessionCategorySnapshotImportRoundTripsThroughResolver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := "session-category-round-trip"
	category := sessioncontract.SessionCategorySubagent
	err := store.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: config.ProjectSessionDir(cfg, binding.ProjectID, sessionID),
		Meta: session.Meta{
			SessionID:          sessionID,
			Category:           &category,
			WorkspaceRoot:      binding.CanonicalRoot,
			WorkspaceContainer: filepath.Base(binding.CanonicalRoot),
			CreatedAt:          time.Now().UTC(),
			UpdatedAt:          time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatalf("ImportSessionSnapshot: %v", err)
	}
	record, err := store.ResolvePersistedSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Category == nil || *record.Meta.Category != sessioncontract.SessionCategorySubagent {
		t.Fatalf("resolved category = %v, want subagent", record.Meta.Category)
	}
}
