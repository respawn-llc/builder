package workflowrunner

import (
	"context"
	"testing"
	"time"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestExecutionTargetPreparerLocksNoneTargetUnderMutationPermit(t *testing.T) {
	fixture := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, fixture.store)
	task := fixture.createTask(t, workflowID)
	reference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeID("node-preparation"), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	permit := workflowexecution.NewMutationPermit()
	preparer := newExecutionTargetPreparer(fixture.store, permit, nil, nil)

	held := make(chan struct{})
	release := make(chan struct{})
	holdDone := make(chan error, 1)
	go func() {
		_, holdErr := workflowexecution.RunMutation(context.Background(), permit, func(context.Context) (struct{}, error) {
			close(held)
			<-release
			return struct{}{}, nil
		})
		holdDone <- holdErr
	}()
	<-held

	type result struct {
		root workflowstore.ExecutionRoot
		err  error
	}
	prepared := make(chan result, 1)
	go func() {
		root, prepareErr := preparer.PrepareExecutionTarget(
			context.Background(),
			reference,
			workflowexecution.NewEstablishUnlockedNoneLaunchPreparation(
				workflowexecution.LaunchSourceWorkspaceSnapshot{
					ID:   fixture.workspaceID,
					Root: fixture.workspace,
				},
				serverapi.NewWorktreeSetupOperationID(),
				nil,
			),
		)
		prepared <- result{root: root, err: prepareErr}
	}()

	select {
	case got := <-prepared:
		t.Fatalf("target preparation completed while mutation permit was held: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	target, err := fixture.store.GetTaskExecutionTargetContext(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext while permit held: %v", err)
	}
	if target.Task.ExecutionTarget != nil {
		t.Fatalf("execution target while permit held = %+v, want unlocked", target.Task.ExecutionTarget)
	}

	close(release)
	if err := <-holdDone; err != nil {
		t.Fatalf("release held mutation: %v", err)
	}
	select {
	case got := <-prepared:
		if got.err != nil {
			t.Fatalf("PrepareExecutionTarget: %v", got.err)
		}
		if got.root.SourceWorkspaceID != fixture.workspaceID || got.root.SourceWorkspaceRoot != fixture.workspace {
			t.Fatalf("prepared root = %+v, want captured source workspace", got.root)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("target preparation did not complete after mutation permit release")
	}
}
