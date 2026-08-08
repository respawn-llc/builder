package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/server/workflowsvc"
	"core/server/worktree"
	"core/shared/serverapi"
)

type taskExecutionRootServiceStub struct {
	request  worktree.TaskExecutionRootPreparationRequest
	prepared worktree.TaskExecutionRootPreparation
	err      error
}

func (s *taskExecutionRootServiceStub) PrepareTaskExecutionRoot(
	_ context.Context,
	req worktree.TaskExecutionRootPreparationRequest,
) (worktree.TaskExecutionRootPreparation, error) {
	s.request = req
	return s.prepared, s.err
}

func (*taskExecutionRootServiceStub) RestoreLockedTaskWorktree(
	context.Context,
	worktree.LockedTaskWorktreeRestoreRequest,
) (worktree.TaskWorktreeMaterialization, error) {
	return worktree.TaskWorktreeMaterialization{}, nil
}

func TestTaskExecutionTargetInfrastructurePreparesUnmanagedRootAndCarriesPreviousWorktree(t *testing.T) {
	previous := &serverapi.RetainedPreviousWorktree{}
	root := workflowstore.ExecutionRoot{
		SourceWorkspaceID:   "workspace-id",
		SourceWorkspaceRoot: t.TempDir(),
	}
	stub := &taskExecutionRootServiceStub{
		prepared: worktree.TaskExecutionRootPreparation{
			Root:                     root,
			RetainedPreviousWorktree: previous,
		},
	}
	infrastructure := taskExecutionTargetInfrastructure{service: stub}

	prepared, err := infrastructure.PrepareTaskExecutionRoot(
		context.Background(),
		workflowsvc.TaskExecutionRootPreparationRequest{
			TaskID:              workflow.TaskID("task-id"),
			SourceWorkspaceID:   root.SourceWorkspaceID,
			SourceWorkspaceRoot: root.SourceWorkspaceRoot,
		},
	)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if stub.request.ManagedTarget != nil {
		t.Fatalf("worktree preparation managed target = %+v, want none", stub.request.ManagedTarget)
	}
	if prepared.Root != root || prepared.RetainedPreviousWorktree != previous {
		t.Fatalf("prepared execution root = %+v, want root and retained previous worktree", prepared)
	}
}

func TestTaskExecutionTargetInfrastructureMapsManagedSnapshotAndPreservesFailureResult(t *testing.T) {
	requestedRef := "HEAD"
	resolvedRef := "refs/heads/main"
	commitOID := strings.Repeat("a", 40)
	managed := &workflowstore.ManagedExecutionRoot{
		WorktreeID: "worktree-id",
		Root:       t.TempDir(),
	}
	root := workflowstore.ExecutionRoot{
		SourceWorkspaceID:   "workspace-id",
		SourceWorkspaceRoot: t.TempDir(),
		Managed:             managed,
	}
	setupErr := errors.New("setup failed")
	stub := &taskExecutionRootServiceStub{
		prepared: worktree.TaskExecutionRootPreparation{Root: root},
		err:      setupErr,
	}
	infrastructure := taskExecutionTargetInfrastructure{service: stub}

	prepared, err := infrastructure.PrepareTaskExecutionRoot(
		context.Background(),
		workflowsvc.TaskExecutionRootPreparationRequest{
			TaskID:              workflow.TaskID("task-id"),
			SourceWorkspaceID:   root.SourceWorkspaceID,
			SourceWorkspaceRoot: root.SourceWorkspaceRoot,
			ManagedSnapshot: &workflowstore.ExecutionTargetSnapshot{
				Mode:         workflow.ExecutionTargetModeHead,
				RequestedRef: &requestedRef,
				ResolvedRef:  &resolvedRef,
				CommitOID:    &commitOID,
				Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
			},
		},
	)
	if !errors.Is(err, setupErr) {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want %v", err, setupErr)
	}
	if stub.request.ManagedTarget == nil ||
		stub.request.ManagedTarget.RequestedRef != requestedRef ||
		stub.request.ManagedTarget.CommitOID != commitOID ||
		stub.request.ManagedTarget.CanonicalRef == nil ||
		*stub.request.ManagedTarget.CanonicalRef != resolvedRef {
		t.Fatalf("worktree preparation target = %+v, want mapped managed snapshot", stub.request.ManagedTarget)
	}
	if prepared.Root.Managed != managed {
		t.Fatalf("prepared managed root = %+v, want preserved failure result", prepared.Root.Managed)
	}
}
