package workflowstore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func TestExecutionTargetSnapshotStructuralValidation(t *testing.T) {
	if _, exists := reflect.TypeOf(ExecutionTargetSnapshot{}).FieldByName("ManagedWorktreeID"); exists {
		t.Fatal("execution target snapshot duplicates current managed worktree binding")
	}
	commit := "0123456789abcdef"
	requested := "HEAD"
	worktreeID := "worktree-1"
	for _, test := range []struct {
		name     string
		snapshot ExecutionTargetSnapshot
		wantErr  bool
	}{
		{
			name:     "none",
			snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		},
		{
			name: "managed with absent relation",
			snapshot: ExecutionTargetSnapshot{
				Mode:         workflow.ExecutionTargetModeHead,
				RequestedRef: &requested,
				CommitOID:    &commit,
				Provenance:   ExecutionTargetProvenanceResolved,
			},
		},
		{
			name: "legacy managed selection",
			snapshot: ExecutionTargetSnapshot{
				Mode:         workflow.ExecutionTargetModeHead,
				RequestedRef: &requested,
				CommitOID:    &commit,
				Provenance:   ExecutionTargetProvenanceLegacyObserved,
			},
		},
		{
			name:     "none cannot retain managed facts",
			snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, RequestedRef: &requested, Provenance: ExecutionTargetProvenanceResolved},
			wantErr:  true,
		},
		{
			name:     "managed requires commit",
			snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requested, Provenance: ExecutionTargetProvenanceResolved},
			wantErr:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.snapshot.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error=%t", err, test.wantErr)
			}
		})
	}

	sourceRoot := ExecutionRoot{
		SourceWorkspaceID:   "workspace-1",
		SourceWorkspaceRoot: "/source",
	}
	if err := sourceRoot.Validate(); err != nil {
		t.Fatalf("source execution root: %v", err)
	}
	if got := sourceRoot.EffectiveRoot(); got != "/source" {
		t.Fatalf("source effective root = %q, want /source", got)
	}
	managedRoot := ExecutionRoot{
		SourceWorkspaceID:   "workspace-1",
		SourceWorkspaceRoot: "/source",
		Managed: &ManagedExecutionRoot{
			WorktreeID: worktreeID,
			Root:       "/worktree",
		},
	}
	if err := managedRoot.Validate(); err != nil {
		t.Fatalf("managed execution root: %v", err)
	}
	if got := managedRoot.EffectiveRoot(); got != "/worktree" {
		t.Fatalf("managed effective root = %q, want /worktree", got)
	}
}

func TestTaskRecordFromTaskDecodesSelectionFactsIndependentlyFromManagedBinding(t *testing.T) {
	requested := "HEAD"
	commit := "0123456789abcdef"
	mode := string(workflow.ExecutionTargetModeHead)
	provenance := string(ExecutionTargetProvenanceLegacyObserved)
	record, err := taskRecordFromTask(sqlitegen.TaskRecord{
		ID:                          "task-1",
		ProjectWorkflowLinkID:       "link-1",
		WorkflowRevisionSeen:        1,
		ShortID:                     "WOR-1",
		Title:                       "Title",
		Body:                        "Body",
		ExecutionTargetMode:         sql.NullString{String: mode, Valid: true},
		ExecutionTargetRequestedRef: sql.NullString{String: requested, Valid: true},
		ExecutionTargetCommitOid:    sql.NullString{String: commit, Valid: true},
		ExecutionTargetProvenance:   sql.NullString{String: provenance, Valid: true},
	})
	if err != nil {
		t.Fatalf("taskRecordFromTask: %v", err)
	}
	if record.ExecutionTarget == nil || record.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead || record.ExecutionTarget.Provenance != ExecutionTargetProvenanceLegacyObserved {
		t.Fatalf("decoded execution target = %+v", record.ExecutionTarget)
	}

	_, err = taskRecordFromTask(sqlitegen.TaskRecord{
		ID:                          "task-invalid",
		ExecutionTargetRequestedRef: sql.NullString{String: "HEAD", Valid: true},
	})
	if err == nil {
		t.Fatal("taskRecordFromTask accepted unlocked task with target facts")
	}
}

func TestExecutionRootMaterializesSourceAndManagedFactsWithoutGitInspection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task, err := store.CreateTask(ctx, CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		Title:             "Execution root",
		SourceWorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	sourceWorkspace, err := store.queries.GetWorkspaceByID(ctx, task.SourceWorkspaceID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID: %v", err)
	}
	setSnapshot := func(mode string, requestedRef any, commitOID any, provenance string, worktreeID any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `
UPDATE tasks
SET
    execution_target_mode = ?,
    execution_target_requested_ref = ?,
    execution_target_resolved_ref = NULL,
    execution_target_commit_oid = ?,
    execution_target_provenance = ?,
    managed_worktree_id = ?
WHERE id = ?`,
			mode, requestedRef, commitOID, provenance, worktreeID, string(task.ID)); err != nil {
			t.Fatalf("set task execution target: %v", err)
		}
	}

	setSnapshot(string(workflow.ExecutionTargetModeNone), nil, nil, string(ExecutionTargetProvenanceResolved), nil)
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask none: %v", err)
	}
	root, err := executionRootForTask(ctx, store.queries, row)
	if err != nil {
		t.Fatalf("executionRootForTask none: %v", err)
	}
	if root.EffectiveRoot() != sourceWorkspace.CanonicalRootPath || root.Managed != nil {
		t.Fatalf("none root = %+v", root)
	}

	worktreeID := "worktree-execution-root"
	worktreeRoot := filepath.Join(t.TempDir(), "managed")
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   task.SourceWorkspaceID,
		CanonicalRoot: worktreeRoot,
		Availability:  "available",
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	setSnapshot(string(workflow.ExecutionTargetModeHead), "HEAD", "0123456789abcdef", string(ExecutionTargetProvenanceResolved), worktreeID)
	row, err = store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask managed: %v", err)
	}
	root, err = executionRootForTask(ctx, store.queries, row)
	if err != nil {
		t.Fatalf("executionRootForTask managed: %v", err)
	}
	if root.EffectiveRoot() != worktreeRoot || root.Managed == nil || root.Managed.WorktreeID != worktreeID {
		t.Fatalf("managed root = %+v", root)
	}

	setSnapshot(string(workflow.ExecutionTargetModeHead), "HEAD", "0123456789abcdef", string(ExecutionTargetProvenanceResolved), nil)
	row, err = store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask missing relation: %v", err)
	}
	_, err = executionRootForTask(ctx, store.queries, row)
	var rootErr *ExecutionRootError
	if !errors.As(err, &rootErr) || rootErr.Kind != ExecutionRootErrorManagedRelationMissing {
		t.Fatalf("missing relation root error = %v", err)
	}

	_, err = executionRootForTask(ctx, store.queries, sqlitegen.TaskRecord{
		ID:                          string(task.ID),
		ProjectWorkflowLinkID:       row.ProjectWorkflowLinkID,
		ProjectID:                   binding.ProjectID,
		ShortID:                     task.ShortID,
		SourceWorkspaceID:           sql.NullString{String: task.SourceWorkspaceID, Valid: true},
		ManagedWorktreeID:           sql.NullString{String: "worktree-missing", Valid: true},
		ExecutionTargetMode:         sql.NullString{String: string(workflow.ExecutionTargetModeHead), Valid: true},
		ExecutionTargetRequestedRef: sql.NullString{String: "HEAD", Valid: true},
		ExecutionTargetCommitOid:    sql.NullString{String: "0123456789abcdef", Valid: true},
		ExecutionTargetProvenance:   sql.NullString{String: string(ExecutionTargetProvenanceResolved), Valid: true},
	})
	if !errors.As(err, &rootErr) || rootErr.Kind != ExecutionRootErrorManagedRecordMissing {
		t.Fatalf("missing record root error = %v", err)
	}

}

func TestExecutionRootRejectsSourceWorkspaceFromAnotherProject(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	otherRoot := filepath.Join(t.TempDir(), "other-workspace")
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll other workspace: %v", err)
	}
	otherBinding, err := store.metadata.RegisterWorkspaceBinding(ctx, otherRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding other workspace: %v", err)
	}
	_, err = executionRootForTask(ctx, store.queries, sqlitegen.TaskRecord{
		ID:                        "task-source-ownership",
		ProjectID:                 binding.ProjectID,
		ShortID:                   "WOR-1",
		SourceWorkspaceID:         sql.NullString{String: otherBinding.WorkspaceID, Valid: true},
		ExecutionTargetMode:       sql.NullString{String: string(workflow.ExecutionTargetModeNone), Valid: true},
		ExecutionTargetProvenance: sql.NullString{String: string(ExecutionTargetProvenanceResolved), Valid: true},
		ProjectWorkflowLinkID:     "link-source-ownership",
	})
	var rootErr *ExecutionRootError
	if !errors.As(err, &rootErr) || rootErr.Kind != ExecutionRootErrorSourceWorkspaceOwnership {
		t.Fatalf("foreign source workspace root error = %v", err)
	}
}

func TestScriptValidationUsesEffectiveExecutionRoot(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	rootPath := t.TempDir()
	scriptPath := filepath.Join(rootPath, "workflow-script")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile script: %v", err)
	}
	workflowID := createScriptStartWorkflow(t, ctx, store, "workflow-script")
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	script := nodeByKey(t, definition, "script")
	root := &ExecutionRoot{
		SourceWorkspaceID:   "workspace-source",
		SourceWorkspaceRoot: rootPath,
	}
	if err := store.validateScriptNodeForExecution(ctx, store.queries, workflow.NodeIDOf(script), root); err != nil {
		t.Fatalf("validateScriptNodeForExecution: %v", err)
	}
}
