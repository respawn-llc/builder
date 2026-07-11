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

func TestStoreUpdateTaskExecutionTargetLifecycleUsesClaimFence(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Claimed managed target",
		Body:       "Body",
	})
	provisioningGeneration := "provisioning-1"
	claimGeneration := "claim-1"
	target := workflow.ExecutionTarget{
		TaskID: task.ID,
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &workflow.ExecutionTargetClaim{Generation: claimGeneration, Phase: workflow.ExecutionTargetClaimMaterializing},
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	locked := target
	locked.State = workflow.ExecutionTargetStateLocked
	locked.SetupState = workflow.ExecutionTargetSetupSucceeded
	locked.ActiveClaim = nil
	if err := store.UpdateTaskExecutionTargetLifecycle(ctx, locked, *target.ActiveClaim); err != nil {
		t.Fatalf("UpdateTaskExecutionTargetLifecycle: %v", err)
	}
	actual, err := store.GetTaskExecutionTarget(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if actual == nil || actual.State != workflow.ExecutionTargetStateLocked || actual.SetupState != workflow.ExecutionTargetSetupSucceeded || actual.ActiveClaim != nil {
		t.Fatalf("updated target = %+v, want locked target with cleared claim", actual)
	}
	if err := store.UpdateTaskExecutionTargetLifecycle(ctx, locked, *target.ActiveClaim); !errors.Is(err, ErrTaskExecutionTargetClaimChanged) {
		t.Fatalf("stale UpdateTaskExecutionTargetLifecycle error = %v, want %v", err, ErrTaskExecutionTargetClaimChanged)
	}
}

func TestStoreFenceExecutionTargetRecoveryRequeuesOrphanedClaims(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	createClaimedTarget := func(t *testing.T, title string, claimPhase workflow.ExecutionTargetClaimPhase, setupState workflow.ExecutionTargetSetupState) workflow.ExecutionTarget {
		t.Helper()
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      title,
			Body:       "Body",
		})
		provisioningGeneration := title + "-provisioning"
		claimGeneration := title + "-claim"
		target := workflow.ExecutionTarget{
			TaskID: task.ID,
			Policy: workflow.ExecutionPolicyHead,
			ResolvedSource: &workflow.ExecutionTargetResolvedSource{
				Kind:   workflow.ExecutionTargetSourceDetachedCommit,
				Commit: "deadbeef",
			},
			State:                       workflow.ExecutionTargetStateInitialProvisioning,
			ProvisioningGeneration:      &provisioningGeneration,
			SetupProvisioningGeneration: &provisioningGeneration,
			SetupState:                  setupState,
			ActiveClaim:                 &workflow.ExecutionTargetClaim{Generation: claimGeneration, Phase: claimPhase},
			RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
		}
		if err := store.SaveTaskExecutionTarget(ctx, target); err != nil {
			t.Fatalf("SaveTaskExecutionTarget %s: %v", title, err)
		}
		return target
	}
	materializing := createClaimedTarget(t, "materializing", workflow.ExecutionTargetClaimMaterializing, workflow.ExecutionTargetSetupPending)
	recovering := createClaimedTarget(t, "recovering", workflow.ExecutionTargetClaimRecovering, workflow.ExecutionTargetSetupRunning)
	queuedRunning := createClaimedTarget(t, "queued-running", workflow.ExecutionTargetClaimRecoveryQueued, workflow.ExecutionTargetSetupRunning)

	fenced, err := store.FenceExecutionTargetRecovery(ctx, 2)
	if err != nil {
		t.Fatalf("FenceExecutionTargetRecovery: %v", err)
	}
	if len(fenced) != 2 {
		t.Fatalf("first fenced task ids = %+v, want two targets", fenced)
	}
	next, err := store.FenceExecutionTargetRecovery(ctx, 2)
	if err != nil {
		t.Fatalf("FenceExecutionTargetRecovery next page: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("next fenced task ids = %+v, want one target", next)
	}
	assertFencedTarget := func(t *testing.T, expected workflow.ExecutionTarget, wantSetup workflow.ExecutionTargetSetupState, wantNewGeneration bool) {
		t.Helper()
		actual, err := store.GetTaskExecutionTarget(ctx, expected.TaskID)
		if err != nil {
			t.Fatalf("GetTaskExecutionTarget %s: %v", expected.TaskID, err)
		}
		if actual == nil || actual.ActiveClaim == nil ||
			actual.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecoveryQueued ||
			actual.SetupState != wantSetup {
			t.Fatalf("fenced target = %+v, want queued claim and setup %q", actual, wantSetup)
		}
		if wantNewGeneration && actual.ActiveClaim.Generation == expected.ActiveClaim.Generation {
			t.Fatalf("fenced target claim generation = %q, want a replacement for %q", actual.ActiveClaim.Generation, expected.ActiveClaim.Generation)
		}
		if !wantNewGeneration && actual.ActiveClaim.Generation != expected.ActiveClaim.Generation {
			t.Fatalf("fenced target claim generation = %q, want retained %q", actual.ActiveClaim.Generation, expected.ActiveClaim.Generation)
		}
	}
	assertFencedTarget(t, materializing, workflow.ExecutionTargetSetupPending, true)
	assertFencedTarget(t, recovering, workflow.ExecutionTargetSetupFailed, true)
	assertFencedTarget(t, queuedRunning, workflow.ExecutionTargetSetupFailed, false)

	if next, err := store.FenceExecutionTargetRecovery(ctx, 2); err != nil {
		t.Fatalf("FenceExecutionTargetRecovery second pass: %v", err)
	} else if len(next) != 0 {
		t.Fatalf("second recovery fence = %+v, want no already-fenced targets", next)
	}
}

func TestStoreClaimsQueuedExecutionTargetRecoveryWithGenerationFence(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Queued recovery",
		Body:       "Body",
	})
	provisioningGeneration := "provisioning-1"
	queuedClaim := workflow.ExecutionTargetClaim{
		Generation: "queued-claim-1",
		Phase:      workflow.ExecutionTargetClaimRecoveryQueued,
	}
	target := workflow.ExecutionTarget{
		TaskID: task.ID,
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}

	queued, err := store.ListQueuedExecutionTargetRecoveries(ctx, 1)
	if err != nil {
		t.Fatalf("ListQueuedExecutionTargetRecoveries: %v", err)
	}
	if len(queued) != 1 || queued[0].TaskID != task.ID || queued[0].ActiveClaim == nil || *queued[0].ActiveClaim != queuedClaim {
		t.Fatalf("queued recoveries = %+v, want task %q with queued claim %+v", queued, task.ID, queuedClaim)
	}

	claimed, err := store.ClaimQueuedExecutionTargetRecovery(ctx, task.ID, queuedClaim)
	if err != nil {
		t.Fatalf("ClaimQueuedExecutionTargetRecovery: %v", err)
	}
	if claimed.ActiveClaim == nil ||
		claimed.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecovering ||
		claimed.ActiveClaim.Generation == queuedClaim.Generation {
		t.Fatalf("claimed recovery target = %+v, want a fresh recovering claim", claimed)
	}
	if _, err := store.ClaimQueuedExecutionTargetRecovery(ctx, task.ID, queuedClaim); !errors.Is(err, ErrTaskExecutionTargetClaimChanged) {
		t.Fatalf("stale ClaimQueuedExecutionTargetRecovery error = %v, want %v", err, ErrTaskExecutionTargetClaimChanged)
	}
	queued, err = store.ListQueuedExecutionTargetRecoveries(ctx, 1)
	if err != nil {
		t.Fatalf("ListQueuedExecutionTargetRecoveries after claim: %v", err)
	}
	if len(queued) != 0 {
		t.Fatalf("queued recoveries after claim = %+v, want none", queued)
	}
}

func TestStoreRequeuesOwnedExecutionTargetRecoveryWithGenerationFence(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Recovering target",
		Body:       "Body",
	})
	provisioningGeneration := "provisioning-1"
	recoveringClaim := workflow.ExecutionTargetClaim{
		Generation: "recovering-claim-1",
		Phase:      workflow.ExecutionTargetClaimRecovering,
	}
	target := workflow.ExecutionTarget{
		TaskID: task.ID,
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &recoveringClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}

	requeued, err := store.RequeueExecutionTargetRecovery(ctx, task.ID, recoveringClaim)
	if err != nil {
		t.Fatalf("RequeueExecutionTargetRecovery: %v", err)
	}
	if requeued.ActiveClaim == nil ||
		requeued.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecoveryQueued ||
		requeued.ActiveClaim.Generation == recoveringClaim.Generation {
		t.Fatalf("requeued recovery target = %+v, want a fresh queued claim", requeued)
	}
	if _, err := store.RequeueExecutionTargetRecovery(ctx, task.ID, recoveringClaim); !errors.Is(err, ErrTaskExecutionTargetClaimChanged) {
		t.Fatalf("stale RequeueExecutionTargetRecovery error = %v, want %v", err, ErrTaskExecutionTargetClaimChanged)
	}
}

func TestStoreAttachManagedExecutionTargetWorktreeLocksClaimedTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Attach managed target",
		Body:       "Body",
	})
	provisioningGeneration := "provisioning-1"
	claimGeneration := "claim-1"
	target := workflow.ExecutionTarget{
		TaskID: task.ID,
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &workflow.ExecutionTargetClaim{Generation: claimGeneration, Phase: workflow.ExecutionTargetClaimMaterializing},
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	locked := target
	locked.State = workflow.ExecutionTargetStateLocked
	worktree, err := store.AttachManagedExecutionTargetWorktree(ctx, AttachManagedExecutionTargetWorktreeRequest{
		Target:        locked,
		ExpectedClaim: *target.ActiveClaim,
		WorkspaceID:   binding.WorkspaceID,
		WorktreeRoot:  t.TempDir(),
		CreatedBranch: true,
	})
	if err != nil {
		t.Fatalf("AttachManagedExecutionTargetWorktree: %v", err)
	}
	if worktree.ID == "" || worktree.Root == "" {
		t.Fatalf("attached worktree = %+v, want persisted worktree", worktree)
	}
	root, err := store.ResolveTaskExecutionRoot(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResolveTaskExecutionRoot: %v", err)
	}
	if root.ManagedWorktree == nil || root.ManagedWorktree.ID != worktree.ID || root.EffectiveRoot != worktree.Root {
		t.Fatalf("execution root = %+v, want attached managed root", root)
	}
	actual, err := store.GetTaskExecutionTarget(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if actual == nil || actual.State != workflow.ExecutionTargetStateLocked || actual.ActiveClaim == nil || actual.ActiveClaim.Generation != claimGeneration {
		t.Fatalf("target = %+v, want locked target retaining materialization claim", actual)
	}
}

func TestStoreBeginManagedExecutionTargetMaterializationClearsNegotiation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Materializing target",
		Body:       "Body",
	})
	startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetActiveStartPlacementForTask: %v", err)
	}
	saveTaskExecutionTargetNegotiation(t, ctx, store, workflow.ExecutionTargetNegotiation{
		TaskID:            task.ID,
		Generation:        "negotiation-1",
		WorkflowID:        workflowID,
		SourceWorkspaceID: binding.WorkspaceID,
		Source:            workflow.ExecutionTargetNegotiationSource{Kind: workflow.ExecutionTargetNegotiationSourceNonGit},
		Action: workflow.ExecutionTargetNegotiationAction{
			Kind:             workflow.ExecutionTargetNegotiationActionStart,
			StartPlacementID: placementPointer(workflow.PlacementID(startPlacement.ID)),
		},
	})
	provisioningGeneration := "provisioning-1"
	claimGeneration := "claim-1"
	target := workflow.ExecutionTarget{
		TaskID: task.ID,
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:   workflow.ExecutionTargetSourceDetachedCommit,
			Commit: "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &workflow.ExecutionTargetClaim{Generation: claimGeneration, Phase: workflow.ExecutionTargetClaimMaterializing},
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	expectedNegotiation, err := store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if expectedNegotiation == nil {
		t.Fatal("GetTaskExecutionTargetNegotiation = nil, want persisted negotiation")
	}
	staleNegotiation := *expectedNegotiation
	staleNegotiation.Generation = "stale-negotiation"
	if err := store.BeginManagedExecutionTargetMaterialization(ctx, BeginManagedExecutionTargetMaterializationRequest{
		Target:              target,
		ExpectedNegotiation: &staleNegotiation,
	}); !errors.Is(err, ErrTaskExecutionTargetNegotiationChanged) {
		t.Fatalf("BeginManagedExecutionTargetMaterialization stale negotiation error = %v, want %v", err, ErrTaskExecutionTargetNegotiationChanged)
	}
	if materialized, materializedErr := store.GetTaskExecutionTarget(ctx, task.ID); materializedErr != nil || materialized != nil {
		t.Fatalf("GetTaskExecutionTarget after stale begin = target:%+v err:%v, want no target", materialized, materializedErr)
	}
	if err := store.BeginManagedExecutionTargetMaterialization(ctx, BeginManagedExecutionTargetMaterializationRequest{Target: target}); !errors.Is(err, ErrTaskExecutionTargetNegotiationInProgress) {
		t.Fatalf("BeginManagedExecutionTargetMaterialization unexpected negotiation error = %v, want %v", err, ErrTaskExecutionTargetNegotiationInProgress)
	}
	if err := store.BeginManagedExecutionTargetMaterialization(ctx, BeginManagedExecutionTargetMaterializationRequest{
		Target:              target,
		ExpectedNegotiation: expectedNegotiation,
	}); err != nil {
		t.Fatalf("BeginManagedExecutionTargetMaterialization: %v", err)
	}
	actual, err := store.GetTaskExecutionTarget(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if actual == nil || actual.State != workflow.ExecutionTargetStateInitialProvisioning || actual.ActiveClaim == nil || actual.ActiveClaim.Generation != claimGeneration {
		t.Fatalf("target = %+v, want initial materializing target", actual)
	}
	negotiation, err := store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation = %+v, want cleared after materialization claim", negotiation)
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
