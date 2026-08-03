package workflowexecution

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestLaunchPreparationVariantsValidateTheirRequiredFacts(t *testing.T) {
	setupID := serverapi.NewWorktreeSetupOperationID()
	valid := []LaunchPreparation{
		{Kind: LaunchPreparationEstablishedRoot},
		{
			Kind:                LaunchPreparationRestoreLockedTarget,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			SetupOperationID:    setupID,
		},
		{
			Kind:                LaunchPreparationEstablishUnlockedNone,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeNone},
			SetupOperationID:    setupID,
		},
		{
			Kind:                LaunchPreparationEstablishUnlockedManaged,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeHead},
			SetupOperationID:    setupID,
		},
	}
	for _, preparation := range valid {
		if err := preparation.Validate(); err != nil {
			t.Fatalf("valid launch preparation %#v rejected: %v", preparation, err)
		}
	}

	invalid := []LaunchPreparation{
		{Kind: LaunchPreparationKind("future")},
		{Kind: LaunchPreparationRestoreLockedTarget},
		{
			Kind:                LaunchPreparationEstablishUnlockedNone,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeHead},
			SetupOperationID:    setupID,
		},
		{
			Kind:                LaunchPreparationEstablishUnlockedManaged,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeNone},
			SetupOperationID:    setupID,
		},
	}
	for _, preparation := range invalid {
		if err := preparation.Validate(); err == nil {
			t.Fatalf("invalid launch preparation %#v validated", preparation)
		}
	}
}

type countingLaunchTargetPreparer struct {
	calls atomic.Int32
	root  workflowstore.ExecutionRoot
}

func (p *countingLaunchTargetPreparer) PrepareExecutionTarget(
	context.Context,
	workflow.CurrentNodeReference,
	LaunchPreparation,
) (workflowstore.ExecutionRoot, error) {
	p.calls.Add(1)
	return p.root, nil
}

func TestLaunchPreparationCoordinatorPreparesOneTaskTargetForParallelStarts(t *testing.T) {
	reference, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	preparation := LaunchPreparation{
		Kind:                LaunchPreparationEstablishUnlockedManaged,
		SourceWorkspaceID:   "workspace",
		SourceWorkspaceRoot: "/workspace",
		Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeHead},
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
	}
	coordinator := NewLaunchPreparationCoordinator()
	preparer := &countingLaunchTargetPreparer{
		root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Managed:             &workflowstore.ManagedExecutionRoot{WorktreeID: "worktree", Root: "/worktree"},
		},
	}
	type result struct {
		root workflowstore.ExecutionRoot
		err  error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			root, err := coordinator.Prepare(context.Background(), reference, preparation, preparer)
			results <- result{root: root, err: err}
		}()
	}
	group.Wait()
	close(results)

	var roots []workflowstore.ExecutionRoot
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("coordinated preparation: %v", outcome.err)
		}
		roots = append(roots, outcome.root)
	}
	if preparer.calls.Load() != 1 {
		t.Fatalf("preparer calls = %d, want one", preparer.calls.Load())
	}
	if len(roots) != 2 || !reflect.DeepEqual(roots[0], roots[1]) {
		t.Fatalf("coordinated roots = %+v, want identical roots", roots)
	}
}
