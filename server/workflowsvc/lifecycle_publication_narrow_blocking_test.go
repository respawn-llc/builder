package workflowsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestLifecycleCaptureDoesNotWaitForFilesystemWorktreeMaterialization(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	publication, err := workflowstore.NewLifecyclePublication(service.store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store:             service.store,
		startPreparations: preparations,
		startPublication:  publication,
	}
	requestedRef := "HEAD"
	commitOID := "1111111111111111111111111111111111111111"
	materializationEntered := make(chan struct{})
	releaseMaterialization := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseMaterialization) }) }
	t.Cleanup(release)
	expectedErr := errors.New("stop after controlled worktree materialization")
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materialize: func(workflow.TaskID) (ExecutionTargetMaterialization, error) {
			if err := os.MkdirAll(worktreeRoot, 0o700); err != nil {
				return ExecutionTargetMaterialization{}, err
			}
			close(materializationEntered)
			<-releaseMaterialization
			return ExecutionTargetMaterialization{}, expectedErr
		},
	}

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want published placement", response, err)
	}
	preparation := <-preparations
	prepared := make(chan error, 1)
	go func() {
		prepared <- preparation(ctx)
	}()
	select {
	case <-materializationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("filesystem/worktree materialization did not reach controlled barrier")
	}

	captureCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	capture, err := publication.Capture(captureCtx)
	if err != nil {
		t.Fatalf("Capture while worktree materialization paused: %v", err)
	}
	currentNodes, err := capture.CurrentNodes(captureCtx, workflow.TaskID(task.Task.ID))
	if err != nil {
		_ = capture.Close()
		t.Fatalf("captured Current Nodes: %v", err)
	}
	queued := capture.QueuedCurrentNodes(workflow.TaskID(task.Task.ID))
	if err := capture.Close(); err != nil {
		t.Fatalf("close lifecycle capture: %v", err)
	}
	if len(currentNodes) != 1 || len(queued) != 1 {
		t.Fatalf("captured lifecycle pair = Current Nodes:%+v queued:%+v, want published queued Task", currentNodes, queued)
	}

	release()
	if err := <-prepared; !errors.Is(err, expectedErr) {
		t.Fatalf("worktree preparation error = %v, want %v", err, expectedErr)
	}
}
