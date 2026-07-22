package worktree

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type deleteInFlightStartLifecycle struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func (l *deleteInFlightStartLifecycle) ResourceReady(ctx context.Context, _ sessionruntime.AgentResourceDescriptor, _ *runtime.Engine, _ sessionruntime.AgentResourceRetainer) error {
	l.enteredOnce.Do(func() { close(l.entered) })
	select {
	case <-l.release:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (l *deleteInFlightStartLifecycle) ResourceDraining(context.Context, sessionruntime.AgentResourceDescriptor) error {
	return nil
}

func (l *deleteInFlightStartLifecycle) unblock() {
	l.releaseOnce.Do(func() { close(l.release) })
}

type deleteActivityTestLLMClient struct{}

func (deleteActivityTestLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func deleteActivityTestRuntimePlan(t *testing.T, env *serviceTestEnv, workdir string) sessionruntime.AgentRuntimePlan {
	t.Helper()
	settings := env.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  workdir,
		Client:   deleteActivityTestLLMClient{},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	return plan
}

type deleteActivityStartResult struct {
	handle sessionruntime.ExecutionHandle
	err    error
}

type deleteTargetState struct {
	sessionTarget clientui.SessionExecutionTarget
	topology      serviceTestWorktree
	record        metadata.WorktreeRecord
	git           GitWorktree
	root          string
}

type deleteActivityResult struct {
	result serverapi.WorktreeDeleteResult
	err    error
}

func openDeleteActivitySessionDescriptor(t *testing.T, sessionID string) session.SessionDescriptor {
	t.Helper()
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(id)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	return descriptor
}

func captureDeleteTargetState(t *testing.T, env *serviceTestEnv, sessionID string, worktree serviceTestWorktree) deleteTargetState {
	t.Helper()
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget before delete: %v", err)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, worktree.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID before delete: %v", err)
	}
	git, found, err := env.service.git.FindCreatedWorktree(env.ctx, env.workspaceRoot, worktree.CanonicalRoot)
	if err != nil {
		t.Fatalf("FindCreatedWorktree before delete: %v", err)
	}
	if !found {
		t.Fatal("busy worktree is absent from Git before delete")
	}
	return deleteTargetState{
		sessionTarget: target,
		topology:      findWorktreeByID(t, mustListWorktrees(t, env).Worktrees, worktree.WorktreeID),
		record:        record,
		git:           git,
		root:          worktree.CanonicalRoot,
	}
}

func (state deleteTargetState) assertUnchanged(t *testing.T, env *serviceTestEnv, sessionID string, worktreeID string) {
	t.Helper()
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget after rejected delete: %v", err)
	}
	if !reflect.DeepEqual(target, state.sessionTarget) {
		t.Fatalf("busy session target changed after rejected delete: before=%+v after=%+v", state.sessionTarget, target)
	}
	topology := findWorktreeByID(t, mustListWorktrees(t, env).Worktrees, worktreeID)
	if !reflect.DeepEqual(topology, state.topology) {
		t.Fatalf("busy worktree topology changed after rejected delete: before=%+v after=%+v", state.topology, topology)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID after rejected delete: %v", err)
	}
	if !reflect.DeepEqual(record, state.record) {
		t.Fatalf("busy worktree metadata changed after rejected delete: before=%+v after=%+v", state.record, record)
	}
	git, found, err := env.service.git.FindCreatedWorktree(env.ctx, env.workspaceRoot, state.root)
	if err != nil {
		t.Fatalf("FindCreatedWorktree after rejected delete: %v", err)
	}
	if !found || !reflect.DeepEqual(git, state.git) {
		t.Fatalf("busy Git worktree changed after rejected delete: before=%+v after=%+v found=%t", state.git, git, found)
	}
	if _, err := os.Stat(state.root); err != nil {
		t.Fatalf("busy worktree root changed after rejected delete: %v", err)
	}
}

func deleteServiceTestWorktree(env *serviceTestEnv, worktreeID string) <-chan deleteActivityResult {
	deleted := make(chan deleteActivityResult, 1)
	go func() {
		result, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, worktreeID))
		deleted <- deleteActivityResult{result: result, err: err}
	}()
	return deleted
}

func startDeleteActivityWithOpenResource(
	authority *sessionruntime.Authority,
	descriptor session.SessionDescriptor,
	plan *sessionruntime.AgentRuntimePlan,
) <-chan deleteActivityStartResult {
	started := make(chan deleteActivityStartResult, 1)
	go func() {
		handle, err := authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
			Descriptor: descriptor,
			Runtime:    plan,
			Resource:   sessionruntime.OpenAgentResource{},
			Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
				return nil
			},
		})
		started <- deleteActivityStartResult{handle: handle, err: err}
	}()
	return started
}

func waitForDeleteActivityTransitionOutcome(t *testing.T, publisher *serviceTestPublisher) clientui.WorktreeTransitionOutcome {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		publisher.mu.Lock()
		if len(publisher.outcomes) > 0 {
			outcome := publisher.outcomes[len(publisher.outcomes)-1]
			publisher.mu.Unlock()
			return outcome
		}
		ready := publisher.ready
		publisher.mu.Unlock()
		if ready == nil {
			select {
			case <-deadline.C:
				t.Fatal("timed out waiting for scheduled worktree delete outcome")
			case <-time.After(5 * time.Millisecond):
			}
			continue
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for scheduled worktree delete outcome")
		case <-ready:
		}
	}
}

func TestDeleteWorktreeRejectsInFlightStartAndCompletesUnrelatedWorktree(t *testing.T) {
	lifecycle := &deleteInFlightStartLifecycle{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	env := newServiceTestEnvWithResourceLifecycle(t, lifecycle)
	defer lifecycle.unblock()

	busy := mustCreateWorktree(t, env, "feature/delete-in-flight-start-busy")
	unrelated := mustCreateWorktree(t, env, "feature/delete-in-flight-start-unrelated")
	busySession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, busySession.Meta().SessionID, env.binding.WorkspaceID, busy.WorktreeID, ".")

	state := captureDeleteTargetState(t, env, busySession.Meta().SessionID, busy)
	descriptor := openDeleteActivitySessionDescriptor(t, busySession.Meta().SessionID)
	plan := deleteActivityTestRuntimePlan(t, env, busy.CanonicalRoot)
	started := startDeleteActivityWithOpenResource(env.authority, descriptor, &plan)
	select {
	case <-lifecycle.entered:
	case result := <-started:
		t.Fatalf("agent start completed before entering resource lifecycle: handle=%v error=%v", result.handle, result.err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for agent start to enter resource lifecycle")
	}

	busyDeleted := deleteServiceTestWorktree(env, busy.WorktreeID)
	select {
	case result := <-busyDeleted:
		if !errors.Is(result.err, serverapi.ErrWorktreeBlocked) {
			t.Fatalf("DeleteWorktree busy target = %+v, %v; want ErrWorktreeBlocked", result.result, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy delete waited for the in-flight start")
	}

	state.assertUnchanged(t, env, busySession.Meta().SessionID, busy.WorktreeID)

	unrelatedDeleted := deleteServiceTestWorktree(env, unrelated.WorktreeID)
	select {
	case result := <-unrelatedDeleted:
		if result.err != nil || result.result.Kind != serverapi.WorktreeDeleteResultKindCompleted {
			t.Fatalf("DeleteWorktree unrelated = %+v, %v; want completed", result.result, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated delete did not complete while busy start remained held")
	}

	lifecycle.unblock()
	start := <-started
	if start.err != nil {
		t.Fatalf("StartAgentExecution: %v", start.err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := start.handle.Wait(waitCtx); err != nil {
		t.Fatalf("wait for started execution: %v", err)
	}
}

func TestDeleteWorktreeRejectsLiveRunAndCompletesUnrelatedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	busy := mustCreateWorktree(t, env, "feature/delete-live-run-busy")
	unrelated := mustCreateWorktree(t, env, "feature/delete-live-run-unrelated")
	busySession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, busySession.Meta().SessionID, env.binding.WorkspaceID, busy.WorktreeID, ".")
	state := captureDeleteTargetState(t, env, busySession.Meta().SessionID, busy)
	plan := deleteActivityTestRuntimePlan(t, env, busy.CanonicalRoot)
	descriptor := openDeleteActivitySessionDescriptor(t, busySession.Meta().SessionID)
	attachment, err := env.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: descriptor.SessionID(),
		OwnerID:   "delete-live-run",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("OpenRuntime: %v", err)
	}
	runStarted := make(chan struct{})
	runRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseRun := func() {
		releaseOnce.Do(func() { close(runRelease) })
	}
	t.Cleanup(releaseRun)
	handle, err := env.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   sessionruntime.CurrentAgentResource{},
		Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
			close(runStarted)
			<-runRelease
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	select {
	case <-runStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live execution to begin")
	}

	busyDeleted := deleteServiceTestWorktree(env, busy.WorktreeID)
	select {
	case result := <-busyDeleted:
		if !errors.Is(result.err, serverapi.ErrWorktreeBlocked) {
			t.Fatalf("DeleteWorktree busy target = %+v, %v; want ErrWorktreeBlocked", result.result, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy delete waited for the live run to finish")
	}
	state.assertUnchanged(t, env, busySession.Meta().SessionID, busy.WorktreeID)

	unrelatedDeleted := deleteServiceTestWorktree(env, unrelated.WorktreeID)
	select {
	case result := <-unrelatedDeleted:
		if result.err != nil || result.result.Kind != serverapi.WorktreeDeleteResultKindCompleted {
			t.Fatalf("DeleteWorktree unrelated = %+v, %v; want completed", result.result, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated delete did not complete while live run remained held")
	}

	releaseRun()
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := handle.Wait(waitCtx); err != nil {
		t.Fatalf("wait for live execution: %v", err)
	}
	if _, err := attachment.Release(waitCtx, sessionruntime.RuntimeReleaseClose); err != nil {
		t.Fatalf("release runtime attachment: %v", err)
	}
}

func TestDeleteTaskWorktreeRejectsInFlightStartUnchanged(t *testing.T) {
	lifecycle := &deleteInFlightStartLifecycle{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	env := newServiceTestEnvWithResourceLifecycle(t, lifecycle)
	defer lifecycle.unblock()
	task, _ := createTaskWorktreeTestTask(t, env)
	materialized, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	busy := serviceTestWorktree{
		WorktreeID:    taskWorktreeID(materialized.Worktree),
		CanonicalRoot: taskWorktreeRoot(materialized.Worktree),
	}
	busySession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, busySession.Meta().SessionID, env.binding.WorkspaceID, busy.WorktreeID, ".")
	state := captureDeleteTargetState(t, env, busySession.Meta().SessionID, busy)
	descriptor := openDeleteActivitySessionDescriptor(t, busySession.Meta().SessionID)
	plan := deleteActivityTestRuntimePlan(t, env, busy.CanonicalRoot)
	started := startDeleteActivityWithOpenResource(env.authority, descriptor, &plan)
	select {
	case <-lifecycle.entered:
	case result := <-started:
		t.Fatalf("agent start completed before entering resource lifecycle: handle=%v error=%v", result.handle, result.err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for agent start to enter resource lifecycle")
	}

	deleted := make(chan error, 1)
	go func() {
		_, deleteErr := env.service.DeleteTaskWorktree(env.ctx, DeleteTaskWorktreeRequest{TaskID: string(task.ID)})
		deleted <- deleteErr
	}()
	select {
	case err := <-deleted:
		if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
			t.Fatalf("DeleteTaskWorktree error = %v, want ErrWorktreeBlocked", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DeleteTaskWorktree waited for the in-flight start")
	}
	state.assertUnchanged(t, env, busySession.Meta().SessionID, busy.WorktreeID)

	lifecycle.unblock()
	start := <-started
	if start.err != nil {
		t.Fatalf("StartAgentExecution: %v", start.err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := start.handle.Wait(waitCtx); err != nil {
		t.Fatalf("wait for started execution: %v", err)
	}
}

func TestDeleteWorktreeScheduledCurrentTargetRetargetsOtherSession(t *testing.T) {
	env := newServiceTestEnv(t)
	target := mustCreateWorktree(t, env, "feature/delete-scheduled-current")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, target.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, target.WorktreeID, ".")
	request := worktreeDeleteRequest(env, target.WorktreeID)

	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree scheduled current target: %v", err)
	}
	if result.Kind != serverapi.WorktreeDeleteResultKindScheduled || result.Scheduled == nil || result.Scheduled.OperationID != request.OperationID {
		t.Fatalf("DeleteWorktree scheduled result = %+v", result)
	}
	outcome := waitForDeleteActivityTransitionOutcome(t, env.publisher)
	if outcome.OperationID != request.OperationID || outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("scheduled delete outcome = %+v, want completed operation %q", outcome, request.OperationID)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	otherTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if sessionTargetWorktreeID(otherTarget) != "" || otherTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("other session target after scheduled delete = %+v, want main workspace", otherTarget)
	}
	if _, err := os.Stat(target.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scheduled target root still exists: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, target.WorktreeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("scheduled target metadata = %v, want sql.ErrNoRows", err)
	}
}
