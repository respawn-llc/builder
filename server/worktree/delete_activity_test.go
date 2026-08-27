package worktree

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type deleteInFlightStartLifecycle struct {
	*testsetup.StartBarrier
}

func (l *deleteInFlightStartLifecycle) ResourceReady(ctx context.Context, _ sessionruntime.AgentResourceDescriptor, _ *runtime.Engine, _ sessionruntime.AgentResourceRetainer) error {
	return l.ArriveAndWait(ctx)
}

func (l *deleteInFlightStartLifecycle) ResourceDraining(context.Context, sessionruntime.AgentResourceDescriptor) error {
	return nil
}

type deleteActivityTestLLMClient struct{}

func (deleteActivityTestLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("finished"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil
}

func (deleteActivityTestLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

type deleteActivityReviewerClient struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newDeleteActivityReviewerClient() *deleteActivityReviewerClient {
	return &deleteActivityReviewerClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *deleteActivityReviewerClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.startedOnce.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value(`{"suggestions":[]}`),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
}

func (*deleteActivityReviewerClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func (c *deleteActivityReviewerClient) Release() {
	c.releaseOnce.Do(func() { close(c.release) })
}

func deleteActivityTestRuntimePlan(t *testing.T, env *serviceTestEnv, workdir string) sessionruntime.AgentRuntimePlan {
	return deleteActivityRuntimePlan(t, env, workdir, deleteActivityTestLLMClient{}, "off", nil)
}

func deleteActivityRuntimePlan(
	t *testing.T,
	env *serviceTestEnv,
	workdir string,
	client llm.Client,
	reviewerFrequency string,
	reviewerClientFactory runtimewire.RuntimeClientFactory,
) sessionruntime.AgentRuntimePlan {
	t.Helper()
	settings := env.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = reviewerFrequency
	settings.Reviewer.Model = "gpt-5"
	settings.Reviewer.ThinkingLevel = "low"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:              settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(workdir, workdir, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client:                client,
		ReviewerClientFactory: reviewerClientFactory,
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	return plan
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

type deleteRemovalBarrierRunner struct {
	delegate gitCommandRunner
	barrier  *testsetup.StartBarrier
}

func (r *deleteRemovalBarrierRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
		if err := r.barrier.ArriveAndWait(ctx); err != nil {
			return nil, err
		}
	}
	return r.delegate.Output(ctx, dir, args...)
}

func (r *deleteRemovalBarrierRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	return r.delegate.Run(ctx, dir, args...)
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

func TestAcquireDeleteTargetActivityRejectsBlankPresentOptions(t *testing.T) {
	env := newServiceTestEnv(t)
	blankRoot := " \t "
	if _, err := env.service.acquireDeleteTargetActivity(env.ctx, nil, &blankRoot); err == nil {
		t.Fatal("acquireDeleteTargetActivity accepted a blank present target root")
	}
}

func TestDeleteWorktreeRejectsInFlightStartAndCompletesUnrelatedWorktree(t *testing.T) {
	lifecycle := &deleteInFlightStartLifecycle{
		StartBarrier: testsetup.NewStartBarrier(),
	}
	env := newServiceTestEnvWithResourceLifecycle(t, lifecycle)
	defer lifecycle.Unblock()

	busy := mustCreateWorktree(t, env, "feature/delete-in-flight-start-busy")
	unrelated := mustCreateWorktree(t, env, "feature/delete-in-flight-start-unrelated")
	busySession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, busySession.Meta().SessionID, env.binding.WorkspaceID, busy.WorktreeID, ".")

	state := captureDeleteTargetState(t, env, busySession.Meta().SessionID, busy)
	descriptor := openDeleteActivitySessionDescriptor(t, busySession.Meta().SessionID)
	plan := deleteActivityTestRuntimePlan(t, env, busy.CanonicalRoot)
	started := testsetup.Start(func() (sessionruntime.ExecutionHandle, error) {
		return env.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
			Descriptor: descriptor,
			Runtime:    &plan,
			Resource:   sessionruntime.OpenAgentResource{},
			Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
				return nil
			},
		})
	})
	select {
	case <-lifecycle.Entered():
	case result := <-started:
		t.Fatalf("agent start completed before entering resource lifecycle: handle=%v error=%v", result.Value, result.Err)
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
		if result.err != nil {
			t.Fatalf("DeleteWorktree unrelated = %+v, %v; want completed", result.result, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated delete did not complete while busy start remained held")
	}

	lifecycle.Unblock()
	start := <-started
	if start.Err != nil {
		t.Fatalf("StartAgentExecution: %v", start.Err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := start.Value.Wait(waitCtx); err != nil {
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
		if result.err != nil {
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

func TestDeleteWorktreeRejectsRunningReviewer(t *testing.T) {
	env := newServiceTestEnv(t)
	busy := mustCreateWorktree(t, env, "feature/delete-running-reviewer-busy")
	sessionID := env.session.Meta().SessionID
	updateServiceTestSessionTarget(t, env, sessionID, env.binding.WorkspaceID, busy.WorktreeID, ".")
	state := captureDeleteTargetState(t, env, sessionID, busy)
	descriptor := openDeleteActivitySessionDescriptor(t, sessionID)
	reviewer := newDeleteActivityReviewerClient()
	t.Cleanup(reviewer.Release)
	plan := deleteActivityRuntimePlan(
		t,
		env,
		busy.CanonicalRoot,
		deleteActivityTestLLMClient{},
		"all",
		runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return reviewer, nil
		}),
	)
	attachment, err := env.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: descriptor.SessionID(),
		OwnerID:   "delete-running-reviewer",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("OpenRuntime: %v", err)
	}
	t.Cleanup(func() {
		_, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		if releaseErr != nil && !errors.Is(releaseErr, serverapi.ErrRuntimeUnavailable) {
			t.Errorf("release Reviewer Runtime: %v", releaseErr)
		}
	})
	handle, err := env.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   sessionruntime.CurrentAgentResource{},
		Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			return bridge.WithEngine(ctx, func(engineCtx context.Context, engine *runtime.Engine) error {
				_, submitErr := engine.SubmitUserMessage(engineCtx, "finish before Reviewer")
				return submitErr
			})
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	select {
	case <-reviewer.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Reviewer provider request")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := handle.Wait(waitCtx); err != nil {
		t.Fatalf("wait for originating execution: %v", err)
	}
	active, err := env.authority.HasBlockingRuntimeActivity(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("HasBlockingRuntimeActivity: %v", err)
	}
	if !active {
		t.Fatal("running Reviewer was not reported as blocking Runtime activity")
	}
	retired, err := env.authority.RetireIdleRuntime(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("RetireIdleRuntime: %v", err)
	}
	if retired {
		t.Fatal("Runtime retired while its Reviewer provider request was running")
	}

	busyDeleted := deleteServiceTestWorktree(env, busy.WorktreeID)
	select {
	case result := <-busyDeleted:
		if !errors.Is(result.err, serverapi.ErrWorktreeBlocked) {
			t.Fatalf("DeleteWorktree busy target = %+v, %v; want ErrWorktreeBlocked", result.result, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy delete waited for the Reviewer provider request")
	}
	state.assertUnchanged(t, env, sessionID, busy.WorktreeID)
}

func TestDeleteWorktreeRetiresIdleRuntimeAndRetargetsSessionBeforePhysicalRemoval(t *testing.T) {
	env := newServiceTestEnv(t)
	target := mustCreateWorktree(t, env, "feature/delete-idle-runtime")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, target.WorktreeID, ".")
	descriptor := openDeleteActivitySessionDescriptor(t, otherSession.Meta().SessionID)
	plan := deleteActivityTestRuntimePlan(t, env, target.CanonicalRoot)
	attachment, err := env.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: descriptor.SessionID(),
		OwnerID:   "delete-idle-runtime",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("OpenRuntime: %v", err)
	}
	t.Cleanup(func() {
		_, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		if releaseErr != nil && !errors.Is(releaseErr, serverapi.ErrRuntimeUnavailable) {
			t.Errorf("release idle Runtime: %v", releaseErr)
		}
	})

	barrier := testsetup.NewStartBarrier()
	env.service.git = NewGitInspector(&deleteRemovalBarrierRunner{
		delegate: env.service.git.runner,
		barrier:  barrier,
	})
	deleted := testsetup.Start(func() (serverapi.WorktreeDeleteResult, error) {
		return env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, target.WorktreeID))
	})
	select {
	case <-barrier.Entered():
	case result := <-deleted:
		t.Fatalf("DeleteWorktree completed before physical-removal boundary: result=%+v error=%v", result.Value, result.Err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for physical Worktree removal")
	}
	defer barrier.Unblock()

	if err := env.authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
		return nil
	}); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("idle Runtime remained available before physical Worktree removal: %v", err)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	if _, err := os.Stat(target.CanonicalRoot); err != nil {
		t.Fatalf("Worktree root changed before physical removal: %v", err)
	}

	barrier.Unblock()
	result := <-deleted
	if result.Err != nil {
		t.Fatalf("DeleteWorktree = %+v, %v; want completed", result.Value, result.Err)
	}
	if _, err := os.Stat(target.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Worktree root still exists after delete: %v", err)
	}
}

func TestDeleteTaskWorktreeRejectsInFlightStartUnchanged(t *testing.T) {
	lifecycle := &deleteInFlightStartLifecycle{
		StartBarrier: testsetup.NewStartBarrier(),
	}
	env := newServiceTestEnvWithResourceLifecycle(t, lifecycle)
	defer lifecycle.Unblock()
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
	started := testsetup.Start(func() (sessionruntime.ExecutionHandle, error) {
		return env.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
			Descriptor: descriptor,
			Runtime:    &plan,
			Resource:   sessionruntime.OpenAgentResource{},
			Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
				return nil
			},
		})
	})
	select {
	case <-lifecycle.Entered():
	case result := <-started:
		t.Fatalf("agent start completed before entering resource lifecycle: handle=%v error=%v", result.Value, result.Err)
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

	lifecycle.Unblock()
	start := <-started
	if start.Err != nil {
		t.Fatalf("StartAgentExecution: %v", start.Err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := start.Value.Wait(waitCtx); err != nil {
		t.Fatalf("wait for started execution: %v", err)
	}
}

func TestDeleteWorktreeCurrentTargetRetargetsOtherSession(t *testing.T) {
	env := newServiceTestEnv(t)
	target := mustCreateWorktree(t, env, "feature/delete-scheduled-current")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, target.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, target.WorktreeID, ".")
	request := worktreeDeleteRequest(env, target.WorktreeID)

	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree current target = %+v, %v; want completed", result, err)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	otherTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if sessionTargetWorktreeID(otherTarget) != "" || otherTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("other session target after delete = %+v, want main workspace", otherTarget)
	}
	if _, err := os.Stat(target.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target root still exists: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, target.WorktreeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("target metadata = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteWorktreeCurrentTargetForceDeletesBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	target := mustCreateWorktree(t, env, "feature/delete-scheduled-force-branch")
	if err := os.WriteFile(filepath.Join(target.CanonicalRoot, "unmerged.txt"), []byte("unmerged"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(t, target.CanonicalRoot, "add", "unmerged.txt")
	runGit(t, target.CanonicalRoot, "commit", "-m", "unmerged branch change")
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, target.WorktreeID, ".")

	request := worktreeDeleteRequest(env, target.WorktreeID)
	request.BranchCleanupPolicy = serverapi.WorktreeBranchCleanupModeDeleteForce
	_, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, target.BranchName); err != nil || exists {
		t.Fatalf("force-deleted branch exists=%v err=%v", exists, err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, target.WorktreeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("target metadata = %v, want sql.ErrNoRows", err)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
}

func TestDeleteWorktreeRechecksDirtyStateBeforeRemoval(t *testing.T) {
	tests := []struct {
		name              string
		secondStatusError error
		wantKind          clientui.WorktreeDirtyStateKind
		wantCount         int
	}{
		{name: "dirty", wantKind: clientui.WorktreeDirtyStateDirty, wantCount: 1},
		{name: "unknown", secondStatusError: errors.New("status inspection failed"), wantKind: clientui.WorktreeDirtyStateUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newServiceTestEnv(t)
			target := mustCreateWorktree(t, env, "feature/delete-scheduled-race-"+test.name)
			updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, target.WorktreeID, ".")
			state := captureDeleteTargetState(t, env, env.session.Meta().SessionID, target)
			preview, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
				SessionID: env.session.Meta().SessionID,
				Selector:  target.WorktreeID,
			})
			if err != nil {
				t.Fatalf("PreviewWorktreeDelete: %v", err)
			}
			if preview.Cleanliness.Kind != clientui.WorktreeDirtyStateClean {
				t.Fatalf("preview cleanliness = %+v, want clean", preview.Cleanliness)
			}

			runner := newBlockingDeleteStatusRunner(test.secondStatusError)
			env.service.git = NewGitInspector(runner)
			deleteResult := make(chan deleteActivityResult, 1)
			go func() {
				result, deleteErr := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, preview.DeletionSelector))
				deleteResult <- deleteActivityResult{result: result, err: deleteErr}
			}()
			select {
			case <-runner.statusReached:
			case <-time.After(3 * time.Second):
				t.Fatal("delete did not reach final cleanliness check")
			}
			if test.secondStatusError == nil {
				if err := os.WriteFile(filepath.Join(target.CanonicalRoot, "dirty-after-schedule.txt"), []byte("dirty"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			runner.ReleaseStatus()

			var result deleteActivityResult
			select {
			case result = <-deleteResult:
			case <-time.After(3 * time.Second):
				t.Fatal("DeleteWorktree did not return after cleanliness check")
			}
			var precondition *serverapi.WorktreeDeletePreconditionError
			if !errors.As(result.err, &precondition) ||
				precondition.DirtyState.Kind != test.wantKind {
				t.Fatalf("DeleteWorktree = %+v, %v; want typed %s precondition", result.result, result.err, test.wantKind)
			}
			if test.wantCount != 0 {
				if precondition.DirtyState.DirtyFileCount == nil ||
					*precondition.DirtyState.DirtyFileCount != test.wantCount {
					t.Fatalf("dirty precondition = %+v, want count %d", precondition.DirtyState, test.wantCount)
				}
			}
			state.assertUnchanged(t, env, env.session.Meta().SessionID, target.WorktreeID)
		})
	}
}

type blockingDeleteStatusRunner struct {
	statusError   error
	statusReached chan struct{}
	releaseStatus chan struct{}
	releaseOnce   sync.Once
	mu            sync.Mutex
	statusCalls   int
}

func newBlockingDeleteStatusRunner(statusError error) *blockingDeleteStatusRunner {
	return &blockingDeleteStatusRunner{
		statusError:   statusError,
		statusReached: make(chan struct{}),
		releaseStatus: make(chan struct{}),
	}
}

func (r *blockingDeleteStatusRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) == 3 && args[0] == "status" && args[1] == "--porcelain=v1" && args[2] == "-z" {
		r.mu.Lock()
		r.statusCalls++
		call := r.statusCalls
		if call == 1 {
			close(r.statusReached)
		}
		r.mu.Unlock()
		if call == 1 {
			select {
			case <-r.releaseStatus:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if r.statusError != nil {
				return nil, r.statusError
			}
		}
	}
	return execGitCommandRunner{}.Output(ctx, dir, args...)
}

func (r *blockingDeleteStatusRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	return execGitCommandRunner{}.Run(ctx, dir, args...)
}

func (r *blockingDeleteStatusRunner) ReleaseStatus() {
	r.releaseOnce.Do(func() {
		close(r.releaseStatus)
	})
}
