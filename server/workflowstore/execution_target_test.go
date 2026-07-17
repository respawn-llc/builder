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
	for _, test := range []struct {
		name     string
		snapshot ExecutionTargetSnapshot
		wantErr  bool
	}{
		{name: "none", snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved}},
		{name: "managed with absent relation", snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requested, CommitOID: &commit, Provenance: ExecutionTargetProvenanceResolved}},
		{name: "legacy managed selection", snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requested, CommitOID: &commit, Provenance: ExecutionTargetProvenanceLegacyObserved}},
		{name: "none cannot retain managed facts", snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, RequestedRef: &requested, Provenance: ExecutionTargetProvenanceResolved}, wantErr: true},
		{name: "managed requires commit", snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requested, Provenance: ExecutionTargetProvenanceResolved}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.snapshot.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error=%t", err, test.wantErr)
			}
		})
	}

	for _, test := range []struct {
		name string
		root ExecutionRoot
		want string
	}{
		{name: "source", root: ExecutionRoot{SourceWorkspaceID: "workspace-1", SourceWorkspaceRoot: "/source"}, want: "/source"},
		{name: "managed", root: ExecutionRoot{SourceWorkspaceID: "workspace-1", SourceWorkspaceRoot: "/source", Managed: &ManagedExecutionRoot{WorktreeID: "worktree-1", Root: "/worktree"}}, want: "/worktree"},
	} {
		t.Run(test.name+" execution root", func(t *testing.T) {
			if err := test.root.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := test.root.EffectiveRoot(); got != test.want {
				t.Fatalf("EffectiveRoot = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTaskRecordFromTaskDecodesSelectionFactsIndependentlyFromManagedBinding(t *testing.T) {
	record, err := taskRecordFromTask(sqlitegen.TaskRecord{
		ID:                          "task-1",
		ProjectWorkflowLinkID:       "link-1",
		WorkflowRevisionSeen:        1,
		ShortID:                     "WOR-1",
		Title:                       "Title",
		Body:                        "Body",
		ExecutionTargetMode:         sql.NullString{String: string(workflow.ExecutionTargetModeHead), Valid: true},
		ExecutionTargetRequestedRef: sql.NullString{String: "HEAD", Valid: true},
		ExecutionTargetCommitOid:    sql.NullString{String: "0123456789abcdef", Valid: true},
		ExecutionTargetProvenance:   sql.NullString{String: string(ExecutionTargetProvenanceLegacyObserved), Valid: true},
	})
	if err != nil {
		t.Fatalf("taskRecordFromTask: %v", err)
	}
	if record.ExecutionTarget == nil || record.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead || record.ExecutionTarget.Provenance != ExecutionTargetProvenanceLegacyObserved {
		t.Fatalf("decoded execution target = %+v", record.ExecutionTarget)
	}

	if _, err := taskRecordFromTask(sqlitegen.TaskRecord{ID: "task-invalid", ExecutionTargetRequestedRef: sql.NullString{String: "HEAD", Valid: true}}); err == nil {
		t.Fatal("taskRecordFromTask accepted unlocked task with target facts")
	}
}

func TestExecutionRootMaterializesSourceAndManagedFactsWithoutGitInspection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Execution root", SourceWorkspaceID: binding.WorkspaceID})
	sourceWorkspace, err := store.queries.GetWorkspaceByID(ctx, task.SourceWorkspaceID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID: %v", err)
	}

	setTaskExecutionTargetFixture(t, ctx, store, task.ID, workflow.ExecutionTargetModeNone, nil)
	row, _ := executionTargetFactsForTask(t, ctx, store, task.ID)
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
	setTaskExecutionTargetFixture(t, ctx, store, task.ID, workflow.ExecutionTargetModeHead, &worktreeID)
	row, _ = executionTargetFactsForTask(t, ctx, store, task.ID)
	root, err = executionRootForTask(ctx, store.queries, row)
	if err != nil {
		t.Fatalf("executionRootForTask managed: %v", err)
	}
	if root.EffectiveRoot() != worktreeRoot || root.Managed == nil || root.Managed.WorktreeID != worktreeID {
		t.Fatalf("managed root = %+v", root)
	}

	setTaskExecutionTargetFixture(t, ctx, store, task.ID, workflow.ExecutionTargetModeHead, nil)
	row, _ = executionTargetFactsForTask(t, ctx, store, task.ID)
	_, err = executionRootForTask(ctx, store.queries, row)
	requireExecutionRootErrorKind(t, err, ExecutionRootErrorManagedRelationMissing)
	row.ManagedWorktreeID = sql.NullString{String: "worktree-missing", Valid: true}
	_, err = executionRootForTask(ctx, store.queries, row)
	requireExecutionRootErrorKind(t, err, ExecutionRootErrorManagedRecordMissing)
}

func TestExecutionRootRejectsSourceWorkspaceFromAnotherProject(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	otherBinding, err := store.metadata.RegisterWorkspaceBinding(ctx, t.TempDir())
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
	requireExecutionRootErrorKind(t, err, ExecutionRootErrorSourceWorkspaceOwnership)
}

func requireExecutionRootErrorKind(t *testing.T, err error, kind ExecutionRootErrorKind) {
	t.Helper()
	var rootErr *ExecutionRootError
	if !errors.As(err, &rootErr) || rootErr.Kind != kind {
		t.Fatalf("execution root error = %v, want kind %q", err, kind)
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
	root := &ExecutionRoot{
		SourceWorkspaceID:   "workspace-source",
		SourceWorkspaceRoot: rootPath,
	}
	if err := store.validateScriptNodeForExecution(ctx, store.queries, workflow.NodeIDOf(nodeByKey(t, definition, "script")), root); err != nil {
		t.Fatalf("validateScriptNodeForExecution: %v", err)
	}
}
