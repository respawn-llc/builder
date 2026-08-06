package workflowrunner

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestLifecycleCaptureDoesNotWaitForProviderPreparation(t *testing.T) {
	fixture := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, fixture.store)
	task := fixture.createTask(t, workflowID)
	fixture.providerEntered = make(chan struct{})
	fixture.providerRelease = make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(fixture.providerRelease)
		})
	}
	t.Cleanup(release)

	started, err := fixture.controller.StartTask(
		context.Background(),
		task.ID,
		func(ctx context.Context) error {
			return fixture.store.LockTaskExecutionTarget(
				ctx,
				task.ID,
				&workflowstore.ExecutionTargetCandidate{
					Snapshot: workflowstore.ExecutionTargetSnapshot{
						Mode:       workflow.ExecutionTargetModeNone,
						Provenance: workflowstore.ExecutionTargetProvenanceResolved,
					},
					Root: workflowstore.ExecutionRoot{
						SourceWorkspaceID:   fixture.workspaceID,
						SourceWorkspaceRoot: fixture.workspace,
					},
				},
			)
		},
	)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("started Current Nodes = %+v, want one", started.Mutation.Created)
	}
	reference := started.Mutation.Created[0].Reference
	select {
	case <-fixture.providerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider preparation did not reach runtime client factory")
	}

	captureCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	capture, err := fixture.controller.CaptureLifecycle(captureCtx)
	if err != nil {
		t.Fatalf("CaptureLifecycle while provider preparation paused: %v", err)
	}
	currentNodes, err := capture.CurrentNodes(captureCtx, task.ID)
	if err != nil {
		_ = capture.Close()
		t.Fatalf("captured Current Nodes: %v", err)
	}
	queued := capture.QueuedCurrentNodes(task.ID)
	if err := capture.Close(); err != nil {
		t.Fatalf("close lifecycle capture: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(reference) ||
		len(queued) != 1 ||
		!queued[0].Equal(reference) {
		t.Fatalf(
			"captured provider-wait pair = Current Nodes:%+v queued:%+v, want %v queued",
			currentNodes,
			queued,
			reference,
		)
	}
	release()
}
