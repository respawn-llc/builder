package workflowstore

import (
	"errors"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func TestTaskExecutionTargetProjectionRoundTripsNoneExecutionRoot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "No worktree target",
		Body:       "Body",
	})
	insertTaskExecutionTarget(t, ctx, store, task.ID, map[string]any{
		"policy":               "none",
		"state":                "locked",
		"setup_state":          "not_applicable",
		"recovery_disposition": "available",
	})

	target, err := store.GetTaskExecutionTarget(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil {
		t.Fatal("GetTaskExecutionTarget = nil, want none target")
	}
	if target.Policy != workflow.ExecutionPolicyNone || target.ResolvedSource != nil || target.ProvisioningGeneration != nil {
		t.Fatalf("none execution target = %+v, want locked target without Git facts", target)
	}

	root, err := store.ResolveTaskExecutionRoot(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResolveTaskExecutionRoot: %v", err)
	}
	if root.SourceWorkspace.ID != binding.WorkspaceID || root.ManagedWorktree != nil || root.EffectiveRoot != binding.CanonicalRoot {
		t.Fatalf("none execution root = %+v, want source workspace root", root)
	}

	unmaterialized := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Unmaterialized target",
		Body:       "Body",
	})
	target, err = store.GetTaskExecutionTarget(ctx, unmaterialized.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget unmaterialized: %v", err)
	}
	if target != nil {
		t.Fatalf("GetTaskExecutionTarget unmaterialized = %+v, want nil", target)
	}
	if _, err := store.ResolveTaskExecutionRoot(ctx, unmaterialized.ID); !errors.Is(err, ErrTaskExecutionTargetNotMaterialized) {
		t.Fatalf("ResolveTaskExecutionRoot unmaterialized error = %v, want %v", err, ErrTaskExecutionTargetNotMaterialized)
	}
}

func TestStoreSaveTaskExecutionTargetLocksImmutableNoneTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Persisted none target",
		Body:       "Body",
	})
	target := workflow.ExecutionTarget{
		TaskID:              task.ID,
		Policy:              workflow.ExecutionPolicyNone,
		State:               workflow.ExecutionTargetStateLocked,
		SetupState:          workflow.ExecutionTargetSetupNotApplicable,
		RecoveryDisposition: workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	actual, err := store.GetTaskExecutionTarget(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if actual == nil || actual.Policy != workflow.ExecutionPolicyNone || actual.State != workflow.ExecutionTargetStateLocked {
		t.Fatalf("saved target = %+v", actual)
	}
	if err := store.SaveTaskExecutionTarget(ctx, target); err == nil {
		t.Fatal("SaveTaskExecutionTarget duplicate succeeded")
	}

	managedTask := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Persisted managed target",
		Body:       "Body",
	})
	provisioningGeneration := "provisioning-1"
	managed := workflow.ExecutionTarget{
		TaskID: managedTask.ID,
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateLocked,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupSucceeded,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := store.SaveTaskExecutionTarget(ctx, managed); err != nil {
		t.Fatalf("SaveTaskExecutionTarget managed: %v", err)
	}
	actual, err = store.GetTaskExecutionTarget(ctx, managedTask.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget managed: %v", err)
	}
	if actual == nil || actual.ResolvedSource == nil || actual.ResolvedSource.Kind != workflow.ExecutionTargetSourceDetachedCommit || actual.ResolvedSource.Commit != "deadbeef" || actual.SetupState != workflow.ExecutionTargetSetupSucceeded {
		t.Fatalf("saved managed target = %+v", actual)
	}
}

func TestTaskExecutionTargetProjectionRoundTripsManagedTargetAndRoot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Managed target",
		Body:       "Body",
	})
	worktreeRoot := t.TempDir()
	const worktreeID = "worktree-execution-target"
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		DisplayName:   "Managed target",
		Availability:  "available",
		Managed:       true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	managedWorktree, err := store.metadata.GetWorktreeRecordByID(ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if _, err := store.queries.UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                string(task.ID),
		ManagedWorktreeID: nullableString(worktreeID),
		UpdatedAtUnixMs:   store.now().UnixMilli(),
	}); err != nil {
		t.Fatalf("UpdateTaskManagedWorktree: %v", err)
	}
	insertTaskExecutionTarget(t, ctx, store, task.ID, map[string]any{
		"policy":                        "custom_ref",
		"requested_custom_ref":          "release/2026.07",
		"resolved_source_kind":          "named_ref",
		"resolved_source_ref":           "refs/heads/release/2026.07",
		"resolved_commit":               "deadbeef",
		"state":                         "locked_reprovisioning",
		"provisioning_generation":       "target-provision-2",
		"setup_provisioning_generation": "target-provision-2",
		"setup_state":                   "failed",
		"active_claim_generation":       "target-claim-2",
		"active_claim_phase":            "recovering",
		"recovery_disposition":          "manual_recovery",
		"recovery_cause":                "test_recovery_cause",
		"exact_branch_observation":      "deadbeef",
		"linked_worktree_common_dir":    "/repo/.git",
		"linked_worktree_admin_entry":   "worktrees/WOR-1",
		"linked_worktree_gitdir":        managedWorktree.CanonicalRoot + "/.git",
		"linked_worktree_head_ref":      "refs/heads/WOR-1",
		"expected_detachment_commit":    "deadbeef",
	})

	target, err := store.GetTaskExecutionTarget(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil {
		t.Fatal("GetTaskExecutionTarget = nil, want managed target")
	}
	if target.Policy != workflow.ExecutionPolicyCustomRef ||
		target.RequestedCustomRef == nil ||
		*target.RequestedCustomRef != "release/2026.07" ||
		target.ResolvedSource == nil ||
		target.ResolvedSource.Kind != workflow.ExecutionTargetSourceNamedRef ||
		target.ResolvedSource.NamedRef == nil ||
		*target.ResolvedSource.NamedRef != "refs/heads/release/2026.07" ||
		target.ResolvedSource.Commit != "deadbeef" ||
		target.State != workflow.ExecutionTargetStateLockedReprovisioning ||
		target.ProvisioningGeneration == nil ||
		*target.ProvisioningGeneration != "target-provision-2" ||
		target.SetupProvisioningGeneration == nil ||
		*target.SetupProvisioningGeneration != "target-provision-2" ||
		target.SetupState != workflow.ExecutionTargetSetupFailed ||
		target.ActiveClaim == nil ||
		target.ActiveClaim.Generation != "target-claim-2" ||
		target.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecovering ||
		target.RecoveryDisposition != workflow.ExecutionTargetRecoveryManualRecovery ||
		target.RecoveryCause == nil ||
		*target.RecoveryCause != workflow.ExecutionTargetRecoveryCause("test_recovery_cause") ||
		target.ExactBranchObservation == nil ||
		*target.ExactBranchObservation != "deadbeef" ||
		target.LinkedWorktreeOwnership == nil ||
		target.LinkedWorktreeOwnership.CommonDir != "/repo/.git" ||
		target.LinkedWorktreeOwnership.AdminEntry != "worktrees/WOR-1" ||
		target.LinkedWorktreeOwnership.GitDir != managedWorktree.CanonicalRoot+"/.git" ||
		target.LinkedWorktreeOwnership.HeadRef != "refs/heads/WOR-1" ||
		target.ExpectedDetachmentCommit == nil ||
		*target.ExpectedDetachmentCommit != "deadbeef" {
		t.Fatalf("managed execution target = %+v, want all durable target facts", target)
	}

	root, err := store.ResolveTaskExecutionRoot(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResolveTaskExecutionRoot: %v", err)
	}
	if root.SourceWorkspace.ID != binding.WorkspaceID ||
		root.ManagedWorktree == nil ||
		root.ManagedWorktree.ID != worktreeID ||
		root.ManagedWorktree.Root != managedWorktree.CanonicalRoot ||
		root.EffectiveRoot != managedWorktree.CanonicalRoot {
		t.Fatalf("managed execution root = %+v, want managed effective root", root)
	}
}

func TestTaskExecutionTargetProjectionDistinguishesDetachedSource(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Detached target",
		Body:       "Body",
	})
	insertTaskExecutionTarget(t, ctx, store, task.ID, map[string]any{
		"policy":                        "head",
		"resolved_source_kind":          "detached_commit",
		"resolved_commit":               "abcdef",
		"state":                         "locked",
		"provisioning_generation":       "target-provision-3",
		"setup_provisioning_generation": "target-provision-3",
		"setup_state":                   "running",
		"recovery_disposition":          "available",
	})

	target, err := store.GetTaskExecutionTarget(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.ResolvedSource == nil ||
		target.ResolvedSource.Kind != workflow.ExecutionTargetSourceDetachedCommit ||
		target.ResolvedSource.NamedRef != nil ||
		target.ResolvedSource.Commit != "abcdef" {
		t.Fatalf("detached execution target = %+v, want detached commit source", target)
	}
}
