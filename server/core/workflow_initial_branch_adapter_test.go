package core

import (
	"context"
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/server/workflowsvc"
	"core/server/worktree"
	"core/shared/serverapi"
)

func TestTaskExecutionTargetInfrastructureCarriesPostCreationBranchAssertion(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	testsetup.InitializeGitRepository(t, workspace)
	t.Setenv("HOME", t.TempDir())
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(ctx, resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	if err := appCore.bundles.Persistence.metadataStore.SetProjectKey(ctx, binding.ProjectID, "BRA"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := workflowstore.New(
		appCore.bundles.Persistence.metadataStore,
		workflowstore.WithRoleResolver(configRoleResolver{settings: resolved.Config.Settings}),
	)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	taskID, _ := createCoreStartupRecoveryTask(t, store, binding.ProjectID)
	git := worktree.NewGitInspector(nil)
	worktreeService := appCore.bundles.Worktrees.worktrees.(*worktree.Service)
	infrastructure := taskExecutionTargetInfrastructure{service: worktreeService, git: git}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	branchA := *targetContext.Task.PendingInitialManagedBranchName
	revision, err := git.ResolveHEAD(ctx, workspace)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	materialized, err := worktreeService.MaterializeInitialTaskWorktree(
		ctx,
		worktree.InitialTaskWorktreeMaterializationRequest{
			TaskID:         taskID,
			ResolvedTarget: revision,
		},
	)
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	requestedRef := revision.RequestedRef
	commitOID := revision.CommitOID
	snapshot := workflowstore.ExecutionTargetSnapshot{
		Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
		ResolvedRef: revision.CanonicalRef, CommitOID: &commitOID,
		Provenance: workflowstore.ExecutionTargetProvenanceResolved,
	}
	if err := store.LockTaskExecutionTarget(ctx, taskID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: snapshot,
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID: binding.WorkspaceID, SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: materialized.Worktree.GetRegistered().GetKent().GetWorktreeId(),
				Root:       materialized.Worktree.GetRegistered().GetGit().GetCanonicalRoot(),
			},
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	if err := infrastructure.AssertInitialTaskBranch(ctx, workflowsvc.InitialTaskBranchAssertionRequest{
		TaskID: taskID, BranchName: branchA,
	}); err != nil {
		t.Fatalf("AssertInitialTaskBranch exact assertion: %v", err)
	}
	branchB := "feature/other-branch"
	err = infrastructure.AssertInitialTaskBranch(ctx, workflowsvc.InitialTaskBranchAssertionRequest{
		TaskID: taskID, BranchName: branchB,
	})
	var mismatch *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &mismatch) ||
		mismatch.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch ||
		mismatch.BranchName != branchB ||
		mismatch.ExistingBranchName == nil ||
		*mismatch.ExistingBranchName != branchA {
		t.Fatalf("AssertInitialTaskBranch error = %T %+v, want %q versus %q mismatch", err, err, branchB, branchA)
	}
	if err := infrastructure.RestoreExecutionTarget(ctx, workflowsvc.ExecutionTargetRestoreRequest{
		TaskID: taskID, InitialBranchAssertion: &branchA,
	}); err != nil {
		t.Fatalf("RestoreExecutionTarget exact assertion reuse: %v", err)
	}
	err = infrastructure.RestoreExecutionTarget(ctx, workflowsvc.ExecutionTargetRestoreRequest{
		TaskID: taskID, InitialBranchAssertion: &branchB,
	})
	mismatch = nil
	if !errors.As(err, &mismatch) ||
		mismatch.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch ||
		mismatch.BranchName != branchB ||
		mismatch.ExistingBranchName == nil ||
		*mismatch.ExistingBranchName != branchA {
		t.Fatalf("MaterializeExecutionTarget error = %T %+v, want %q versus %q mismatch", err, err, branchB, branchA)
	}
}
