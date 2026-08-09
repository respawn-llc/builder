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
	prospectiveBranch := "feature/adapter-inspection"
	if err := infrastructure.InspectProspectiveInitialTaskBranch(ctx, workflowsvc.InitialTaskBranchInspectionRequest{
		SourceWorkspaceRoot: binding.CanonicalRoot,
		BranchName:          prospectiveBranch,
	}); err != nil {
		t.Fatalf("InspectProspectiveInitialTaskBranch(%q): %v", prospectiveBranch, err)
	}
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
				WorktreeID: materialized.Worktree.Registered.Kent.WorktreeID,
				Root:       materialized.Worktree.Registered.Git.CanonicalRoot,
			},
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	exact, err := infrastructure.MaterializeExecutionTarget(ctx, workflowsvc.ExecutionTargetMaterializeRequest{
		TaskID: taskID, Snapshot: snapshot, InitialBranchAssertion: &branchA,
	})
	if err != nil {
		t.Fatalf("MaterializeExecutionTarget exact assertion reuse: %v", err)
	}
	if exact.RetainedRoot == nil ||
		exact.RetainedRoot.WorktreeID != materialized.Worktree.Registered.Kent.WorktreeID ||
		exact.RetainedRoot.Root != materialized.Worktree.Registered.Git.CanonicalRoot {
		t.Fatalf("exact assertion reuse = %+v, want original managed Worktree", exact)
	}
	if err := git.Remove(ctx, workspace, materialized.Worktree.Registered.Git.CanonicalRoot, true); err != nil {
		t.Fatalf("remove managed Worktree before exact recreation: %v", err)
	}
	recreated, err := infrastructure.MaterializeExecutionTarget(ctx, workflowsvc.ExecutionTargetMaterializeRequest{
		TaskID: taskID, Snapshot: snapshot, InitialBranchAssertion: &branchA,
	})
	if err != nil {
		t.Fatalf("MaterializeExecutionTarget exact assertion recreation: %v", err)
	}
	if recreated.RetainedRoot == nil ||
		recreated.RetainedRoot.WorktreeID != materialized.Worktree.Registered.Kent.WorktreeID ||
		recreated.RetainedRoot.Root != materialized.Worktree.Registered.Git.CanonicalRoot {
		t.Fatalf("exact assertion recreation = %+v, want original managed Worktree identity", recreated)
	}
	before, err := git.List(ctx, workspace)
	if err != nil {
		t.Fatalf("git.List before assertion: %v", err)
	}
	branchB := "feature/other-branch"
	_, err = infrastructure.MaterializeExecutionTarget(ctx, workflowsvc.ExecutionTargetMaterializeRequest{
		TaskID: taskID, Snapshot: snapshot, InitialBranchAssertion: &branchB,
	})
	var mismatch *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &mismatch) ||
		mismatch.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch ||
		mismatch.BranchName != branchB ||
		mismatch.ExistingBranchName == nil ||
		*mismatch.ExistingBranchName != branchA {
		t.Fatalf("MaterializeExecutionTarget error = %T %+v, want %q versus %q mismatch", err, err, branchB, branchA)
	}
	after, err := git.List(ctx, workspace)
	if err != nil {
		t.Fatalf("git.List after assertion: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("Git Worktrees after mismatch = %d, want unchanged %d", len(after), len(before))
	}
	exists, err := git.BranchExists(ctx, workspace, branchB)
	if err != nil {
		t.Fatalf("BranchExists(%q): %v", branchB, err)
	}
	if exists {
		t.Fatalf("mismatched branch %q was created", branchB)
	}
}
