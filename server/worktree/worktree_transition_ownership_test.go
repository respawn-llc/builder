package worktree

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestScheduledWorktreeTransitionRetainsMatchingOperationOwnership(t *testing.T) {
	env := newServiceTestEnv(t)
	plan := deleteActivityTestRuntimePlan(t, env, env.workspaceRoot)
	attachment, err := env.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: openDeleteActivitySessionDescriptor(t, env.session.Meta().SessionID).SessionID(),
		OwnerID:   "worktree-operation-ownership",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		if _, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); releaseErr != nil {
			t.Errorf("close runtime: %v", releaseErr)
		}
	})

	env.publisher.mu.Lock()
	env.publisher.ready = make(chan struct{}, 1)
	env.publisher.mu.Unlock()
	releaseOperation := make(chan struct{})
	request := worktreeTransitionRequest{
		operationID: serverapi.NewWorktreeOperationID(),
		sessionID:   env.session.Meta().SessionID,
		kind:        clientui.WorktreeTransitionEnter,
	}
	var executions atomic.Int32
	execute := func(context.Context, transitionAuthority, transitionTargetSync) error {
		executions.Add(1)
		<-releaseOperation
		return nil
	}

	ack, err := env.service.scheduleWorktreeTransition(context.Background(), request, execute, nil)
	if err != nil {
		t.Fatalf("schedule Worktree transition: %v", err)
	}
	retry, err := env.service.scheduleWorktreeTransition(context.Background(), request, execute, nil)
	if err != nil || retry != ack {
		t.Fatalf("matching retry = %+v, %v; want %+v", retry, err, ack)
	}
	different := request
	different.operationID = serverapi.NewWorktreeOperationID()
	_, err = env.service.scheduleWorktreeTransition(context.Background(), different, execute, nil)
	var pending *serverapi.WorktreeTransitionPendingError
	if !errors.As(err, &pending) || pending.PendingOperationID != request.operationID {
		t.Fatalf("different operation error = %v, want pending %s", err, request.operationID)
	}

	close(releaseOperation)
	select {
	case <-env.publisher.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("accepted Worktree transition did not publish an outcome")
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("Worktree executions = %d, want one", got)
	}
}
