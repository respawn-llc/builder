package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/registry"
	runtimepkg "core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type lifecycleBuild struct {
	engine     *runtimepkg.Engine
	closeCount atomic.Int32
	rebindDir  atomic.Value
}

func newLifecycleBuilder(t *testing.T, fixture sessionRuntimeFixture) (*lifecycleBuild, RuntimeBuilder) {
	t.Helper()
	state := &lifecycleBuild{}
	build := func(ctx context.Context) (RuntimeBuildResult, error) {
		engine, err := runtimepkg.New(fixture.store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		state.engine = engine
		return RuntimeBuildResult{
			Engine: engine,
			LocalRebind: func(dir string) error {
				state.rebindDir.Store(dir)
				return nil
			},
			Close: func() {
				state.closeCount.Add(1)
				_ = engine.Close()
			},
		}, nil
	}
	return state, build
}

func newRuntimeServiceFixture(t *testing.T) (sessionRuntimeFixture, *registry.RuntimeRegistry) {
	t.Helper()
	fixture := newSessionRuntimeFixture(t)
	reg := registry.NewRuntimeRegistry()
	fixture.service.runtimes = reg
	reg.SetInterestObserver(fixture.service.runtimeInterestChanged)
	return fixture, reg
}

func releaseRequest(sessionID, ownerID string, onlyIfIdle, dropOwner bool) serverapi.SessionRuntimeReleaseRequest {
	return serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel",
		SessionID:       sessionID,
		OwnerID:         ownerID,
		OnlyIfIdle:      onlyIfIdle,
		DropOwner:       dropOwner,
	}
}

func TestAcquireRuntimeRegistersActiveRuntime(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	_, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	if !reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("expected runtime active after acquire")
	}
}

func TestAcquireRuntimeReuseSharesOwnership(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime owner-a: %v", err)
	}
	reuseBuild := func(ctx context.Context) (RuntimeBuildResult, error) {
		t.Error("reuse acquire must not rebuild the runtime")
		return RuntimeBuildResult{}, errors.New("unexpected build")
	}
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-b", reuseBuild); err != nil {
		t.Fatalf("AcquireRuntime owner-b: %v", err)
	}

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "owner-a", true, true))
	if err != nil {
		t.Fatalf("release owner-a: %v", err)
	}
	if resp.Released {
		t.Fatalf("dropping one of two owners should not release runtime: %+v", resp)
	}
	if !reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("runtime must stay active while a second owner remains")
	}
	if state.closeCount.Load() != 0 {
		t.Fatalf("runtime closed %d times, want 0", state.closeCount.Load())
	}

	resp, err = fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "owner-b", true, true))
	if err != nil {
		t.Fatalf("release owner-b: %v", err)
	}
	if !resp.Released {
		t.Fatalf("releasing last owner should release runtime: %+v", resp)
	}
	if reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("runtime must be gone after last owner released")
	}
	if state.closeCount.Load() != 1 {
		t.Fatalf("runtime closed %d times, want 1", state.closeCount.Load())
	}
}

func TestReleaseOnlyIfIdleKeepsActiveRun(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	release := startRegisteredActiveRun(t, fixture, reg)
	defer release()

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "test-owner", true, false))
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !resp.Active || resp.Released {
		t.Fatalf("release response = %+v, want active not released", resp)
	}
	if !reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("runtime with an active run must stay registered")
	}
}

func TestClientNavigationReleasesDoNotInterruptActiveServerRun(t *testing.T) {
	navigations := []string{
		"new",
		"resume selected session",
		"resume create session",
		"back",
		"review",
		"init",
		"exit",
	}
	for _, navigation := range navigations {
		t.Run(navigation, func(t *testing.T) {
			fixture, reg := newRuntimeServiceFixture(t)
			sessionID := fixture.store.Meta().SessionID
			client := &releaseObservingLLMClient{
				entered:  make(chan struct{}),
				release:  make(chan struct{}),
				canceled: make(chan struct{}, 1),
			}
			engine, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
			if err != nil {
				t.Fatalf("runtime.New: %v", err)
			}
			t.Cleanup(func() { _ = engine.Close() })
			claim, _, _ := reg.AcquireRuntimeClaim(sessionID, "origin-client")
			claim.Resolve(engine, nil, nil)

			runDone := make(chan error, 1)
			go func() {
				_, runErr := engine.SubmitUserMessage(context.Background(), "run")
				runDone <- runErr
			}()
			select {
			case <-client.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("active run did not start")
			}

			resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "origin-client", true, true))
			if err != nil {
				t.Fatalf("release origin client: %v", err)
			}
			if !resp.Active || resp.Released {
				t.Fatalf("release response = %+v, want active origin run retained", resp)
			}
			if !reg.IsSessionRuntimeActive(sessionID) {
				t.Fatal("navigation release removed the active server runtime")
			}
			if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "independent-client", func(context.Context) (RuntimeBuildResult, error) {
				t.Fatal("independent owner must attach to the live origin runtime")
				return RuntimeBuildResult{}, nil
			}); err != nil {
				t.Fatalf("acquire independent owner: %v", err)
			}
			select {
			case <-client.canceled:
				t.Fatal("navigation release interrupted the active model run")
			default:
			}

			close(client.release)
			select {
			case err := <-runDone:
				if err != nil {
					t.Fatalf("active run finished with %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("active run did not finish")
			}
			if _, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "independent-client", true, true)); err != nil {
				t.Fatalf("release independent owner: %v", err)
			}
		})
	}
}

type releaseObservingLLMClient struct {
	entered  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (c *releaseObservingLLMClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	close(c.entered)
	select {
	case <-c.release:
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	case <-ctx.Done():
		select {
		case c.canceled <- struct{}{}:
		default:
		}
		return llm.Response{}, ctx.Err()
	}
}

func TestReleaseCloseIfIdlePolicyKeepsActiveRunWithoutLegacyOnlyIfIdle(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	release := startRegisteredActiveRun(t, fixture, reg)
	defer release()

	req := releaseRequest(sessionID, "test-owner", false, false)
	req.ClosePolicy = serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle
	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), req)
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !resp.Active || resp.Released {
		t.Fatalf("release response = %+v, want active not released", resp)
	}
	if !reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("close_if_idle policy must keep active runtime registered")
	}
}

func TestReleaseOnlyIfIdleKeepsSubscriberRuntime(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	sub, err := reg.SubscribeSessionActivity(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "owner-a", true, true))
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if resp.Released {
		t.Fatalf("runtime with subscribers must not be released: %+v", resp)
	}
	if !reg.IsSessionRuntimeActive(sessionID) || state.closeCount.Load() != 0 {
		t.Fatal("subscriber runtime must stay registered and open")
	}
}

func TestReleaseOnlyIfIdleClosesIdleRuntime(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "owner-a", true, true))
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !resp.Released {
		t.Fatalf("idle runtime should be released: %+v", resp)
	}
	if reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("idle runtime must be torn down")
	}
	if state.closeCount.Load() != 1 {
		t.Fatalf("runtime closed %d times, want 1", state.closeCount.Load())
	}
}

func TestReleaseDetachOnlyDropsOwnerWithoutClosingIdleRuntime(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	t.Cleanup(func() {
		if claim := reg.RuntimeClaimFor(sessionID); claim != nil {
			_, _ = claim.Close(context.Background(), nil)
		}
	})

	req := releaseRequest(sessionID, "owner-a", true, true)
	req.ClosePolicy = serverapi.SessionRuntimeReleaseClosePolicyDetachOnly
	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), req)
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !resp.Released || resp.Active {
		t.Fatalf("detach-only release response = %+v, want released without active close", resp)
	}
	if !reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("detach-only release must keep the runtime registered")
	}
	if state.closeCount.Load() != 0 {
		t.Fatalf("detach-only release closed runtime %d times, want 0", state.closeCount.Load())
	}
}

func TestReleaseFromNonOwnerKeepsSharedRuntime(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "owner-other", true, true))
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !resp.Released {
		t.Fatalf("non-owner release should report released no-op: %+v", resp)
	}
	if !reg.IsSessionRuntimeActive(sessionID) || state.closeCount.Load() != 0 {
		t.Fatal("non-owner release must not tear down the shared runtime")
	}
}

func TestRecreateRuntimeOvertakesExisting(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	first, firstBuild := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", firstBuild); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	firstEngine := first.engine

	second, secondBuild := newLifecycleBuilder(t, fixture)
	release, err := fixture.service.RecreateRuntime(context.Background(), sessionID, "owner-b", secondBuild)
	if err != nil {
		t.Fatalf("RecreateRuntime: %v", err)
	}
	if first.closeCount.Load() != 1 {
		t.Fatalf("previous runtime closed %d times, want 1", first.closeCount.Load())
	}
	engine, err := reg.ResolveRuntime(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ResolveRuntime: %v", err)
	}
	if engine == firstEngine || engine != second.engine {
		t.Fatal("recreate must install the freshly built runtime")
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("runtime must be gone after recreate release")
	}
}

func TestRecreateRuntimeHoldsRunBlockUntilRelease(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	_, build := newLifecycleBuilder(t, fixture)

	release, err := fixture.service.RecreateRuntime(context.Background(), sessionID, "owner-a", build)
	if err != nil {
		t.Fatalf("RecreateRuntime: %v", err)
	}

	blocked := make(chan func(), 1)
	go func() { blocked <- reg.BlockSessionRuns([]string{sessionID}) }()
	deadline := time.After(time.Second)
	for !reg.SessionRunsBlocked(sessionID) {
		select {
		case <-deadline:
			t.Fatal("BlockSessionRuns never registered the block")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-blocked:
		t.Fatal("BlockSessionRuns proceeded before the acquired runtime released its run block")
	default:
	}

	if err := release(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case unblock := <-blocked:
		unblock()
	case <-time.After(2 * time.Second):
		t.Fatal("BlockSessionRuns did not proceed after the acquired runtime released its run block")
	}
}

func TestRecreateRejectingActiveRunRejectsInFlightStart(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	_, build := newLifecycleBuilder(t, fixture)

	release, ok := reg.BeginSessionRun(sessionID)
	if !ok {
		t.Fatal("BeginSessionRun")
	}

	if _, err := fixture.service.RecreateRuntimeRejectingActiveRun(context.Background(), sessionID, "owner-headless", build); !errors.Is(err, ErrSessionRunActive) {
		t.Fatalf("RecreateRuntimeRejectingActiveRun err=%v, want ErrSessionRunActive while a run start is in flight", err)
	}
	if reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("rejected headless recreate must not build a runtime")
	}

	release()
	acquiredRelease, err := fixture.service.RecreateRuntimeRejectingActiveRun(context.Background(), sessionID, "owner-headless", build)
	if err != nil {
		t.Fatalf("RecreateRuntimeRejectingActiveRun after start cleared: %v", err)
	}
	if err := acquiredRelease(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestRecreateRejectingActiveRunRejectsQueuedUserWork(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	state.engine.QueueUserMessageForAutoDrain("queued user work", "queue-1")

	if _, err := fixture.service.RecreateRuntimeRejectingActiveRun(context.Background(), sessionID, "owner-headless", build); !errors.Is(err, ErrSessionRunActive) {
		t.Fatalf("RecreateRuntimeRejectingActiveRun err=%v, want ErrSessionRunActive while queued user work is accepted", err)
	}
	if !reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("rejected headless recreate must not close the queued runtime")
	}
	if !state.engine.HasQueuedUserWork() {
		t.Fatal("rejected headless recreate must leave accepted queued user work intact")
	}
}

func TestRecreateRuntimeRejectedWhileSessionBlocked(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)

	unblock := reg.BlockSessionRuns([]string{sessionID})
	defer unblock()

	if _, err := fixture.service.RecreateRuntime(context.Background(), sessionID, "owner-a", build); !errors.Is(err, ErrSessionRunsBlocked) {
		t.Fatalf("RecreateRuntime error=%v, want ErrSessionRunsBlocked", err)
	}
	if reg.IsSessionRuntimeActive(sessionID) || state.engine != nil {
		t.Fatal("blocked recreate must not build or install a runtime")
	}

	unblock()
	if _, err := fixture.service.RecreateRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("RecreateRuntime after unblock: %v", err)
	}
	if !reg.IsSessionRuntimeActive(sessionID) {
		t.Fatal("recreate must install the runtime once unblocked")
	}
}

func TestSyncExecutionTargetRebindsActiveRuntime(t *testing.T) {
	fixture, _ := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	state, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	target := clientui.SessionExecutionTarget{EffectiveWorkdir: fixture.config.WorkspaceRoot}
	if err := fixture.service.SyncExecutionTarget(context.Background(), sessionID, target, nil); err != nil {
		t.Fatalf("SyncExecutionTarget: %v", err)
	}
	if got, _ := state.rebindDir.Load().(string); got != fixture.config.WorkspaceRoot {
		t.Fatalf("rebind workdir = %q, want %q", got, fixture.config.WorkspaceRoot)
	}
}

func TestWorktreeTransitionBoundaryRejectsInactiveOrigin(t *testing.T) {
	fixture, _ := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	_, build := newLifecycleBuilder(t, fixture)
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	runID, err := runtimeids.ParseRunID("018fdd67-89ab-4cde-8123-456789abc001")
	if err != nil {
		t.Fatalf("ParseRunID: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("018fdd67-89ab-4cde-8123-456789abc002")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	err = fixture.service.RunWorktreeTransitionAtStepBoundary(context.Background(), sessionID, serverapi.RuntimeStepOrigin{
		RunID:  runID,
		StepID: stepID,
	}, func(
		ctx context.Context,
		syncTarget func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
	) error {
		return syncTarget(ctx, clientui.SessionExecutionTarget{EffectiveWorkdir: t.TempDir()}, nil)
	}, nil)
	if err == nil {
		t.Fatal("RunWorktreeTransitionAtStepBoundary succeeded without the originating active model step")
	}
}

func TestWorktreeTransitionAppliesAfterCommandResultBeforeSameTurnFollowup(t *testing.T) {
	fixture, _ := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	targetWorkdir := t.TempDir()
	client := &worktreeBoundaryClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "entering", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call-enter", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent worktree enter feature"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "checking", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call-probe", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	var engine *runtimepkg.Engine
	var rebindDir atomic.Value
	handler := worktreeBoundaryHandler{
		service:       fixture.service,
		sessionID:     sessionID,
		workspaceRoot: fixture.config.WorkspaceRoot,
		targetWorkdir: targetWorkdir,
		engine:        func() *runtimepkg.Engine { return engine },
		rebindDir:     &rebindDir,
	}
	build := func(context.Context) (RuntimeBuildResult, error) {
		created, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: &handler,
		}), runtimepkg.Config{Model: "gpt-5"})
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		engine = created
		return RuntimeBuildResult{
			Engine:      created,
			LocalRebind: func(workdir string) error { rebindDir.Store(workdir); return nil },
			Close:       func() { _ = created.Close() },
		}, nil
	}
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "start"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if got, _ := rebindDir.Load().(string); got != targetWorkdir {
		t.Fatalf("following local tool rebind root = %q, want %q", got, targetWorkdir)
	}
	if !handler.probeObservedTarget.Load() {
		t.Fatal("following tool in the same assistant turn did not observe the retargeted root")
	}
	request := client.request(t, 1)
	var sawCommandResult, sawSteer, sawWorktreeContext bool
	for _, item := range request.Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call-enter" {
			sawCommandResult = true
		}
		if item.Type != llm.ResponseItemTypeMessage {
			continue
		}
		if item.Role == llm.RoleUser && item.Content == "steered after enter acknowledgement" {
			sawSteer = true
		}
		if item.Role == llm.RoleDeveloper &&
			item.MessageType == llm.MessageTypeWorktreeMode &&
			item.WorktreeContext != nil &&
			item.WorktreeContext.EffectiveCwd == targetWorkdir {
			sawWorktreeContext = true
		}
	}
	if !sawCommandResult || !sawSteer || !sawWorktreeContext {
		t.Fatalf("same-turn follow-up request missing command result, steer, or target context: %+v", request.Items)
	}
}

type worktreeBoundaryHandler struct {
	service             *Service
	sessionID           string
	workspaceRoot       string
	targetWorkdir       string
	engine              func() *runtimepkg.Engine
	rebindDir           *atomic.Value
	calls               atomic.Int32
	probeObservedTarget atomic.Bool
}

func (handler *worktreeBoundaryHandler) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	switch handler.calls.Add(1) {
	case 1:
		runID, err := runtimeids.ParseRunID(call.RunID)
		if err != nil {
			return tools.Result{}, err
		}
		stepID, err := runtimeids.ParseStepID(call.StepID)
		if err != nil {
			return tools.Result{}, err
		}
		reminder := &session.WorktreeReminderState{
			Mode: session.WorktreeReminderModeEnter,
			WorktreeContext: session.WorktreeContext{
				Branch:        session.OptionalWorktreeBranch("feature/boundary"),
				WorktreePath:  handler.targetWorkdir,
				WorkspaceRoot: handler.workspaceRoot,
				EffectiveCwd:  handler.targetWorkdir,
			},
		}
		err = handler.service.RunWorktreeTransitionAtStepBoundary(ctx, handler.sessionID, serverapi.RuntimeStepOrigin{RunID: runID, StepID: stepID}, func(syncCtx context.Context, syncTarget func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error {
			return syncTarget(syncCtx, clientui.SessionExecutionTarget{
				WorkspaceRoot:    handler.workspaceRoot,
				EffectiveWorkdir: handler.targetWorkdir,
			}, reminder)
		}, nil)
		if err != nil {
			return tools.Result{}, err
		}
		handler.engine().QueueUserMessageForAutoDrain("steered after enter acknowledgement", "steered-after-enter")
		return tools.Result{CallID: call.ID, Name: call.Name, Output: json.RawMessage(`"scheduled"`)}, nil
	case 2:
		if got, _ := handler.rebindDir.Load().(string); got == handler.targetWorkdir {
			handler.probeObservedTarget.Store(true)
		}
		return tools.Result{CallID: call.ID, Name: call.Name, Output: json.RawMessage(`"probed"`)}, nil
	default:
		return tools.Result{}, errors.New("unexpected worktree boundary tool call")
	}
}

type worktreeBoundaryClient struct {
	mu        sync.Mutex
	responses []llm.Response
	requests  []llm.Request
}

func (client *worktreeBoundaryClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.requests = append(client.requests, request)
	if len(client.responses) == 0 {
		return llm.Response{}, errors.New("unexpected model request")
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}

func (client *worktreeBoundaryClient) request(t *testing.T, index int) llm.Request {
	t.Helper()
	client.mu.Lock()
	defer client.mu.Unlock()
	if index >= len(client.requests) {
		t.Fatalf("request %d is unavailable in %+v", index, client.requests)
	}
	return client.requests[index]
}

type worktreeTransitionSteerClient struct {
	mu           sync.Mutex
	calls        []llm.Request
	firstStarted chan struct{}
	releaseFirst chan struct{}
	secondCall   chan struct{}
	firstOnce    sync.Once
	secondOnce   sync.Once
}

func newWorktreeTransitionSteerClient() *worktreeTransitionSteerClient {
	return &worktreeTransitionSteerClient{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		secondCall:   make(chan struct{}),
	}
}

func (c *worktreeTransitionSteerClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	callNumber := len(c.calls)
	c.mu.Unlock()
	if callNumber == 1 {
		c.firstOnce.Do(func() { close(c.firstStarted) })
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-c.releaseFirst:
		}
	} else {
		c.secondOnce.Do(func() { close(c.secondCall) })
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *worktreeTransitionSteerClient) request(t *testing.T, index int) llm.Request {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if index >= len(c.calls) {
		t.Fatalf("request %d is unavailable in %+v", index, c.calls)
	}
	return c.calls[index]
}

type lifecycleRequestCaptureClient struct {
	mu      sync.Mutex
	calls   []llm.Request
	callCh  chan struct{}
	callOne sync.Once
}

func newLifecycleRequestCaptureClient() *lifecycleRequestCaptureClient {
	return &lifecycleRequestCaptureClient{callCh: make(chan struct{})}
}

func (c *lifecycleRequestCaptureClient) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	c.mu.Unlock()
	c.callOne.Do(func() { close(c.callCh) })
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *lifecycleRequestCaptureClient) firstCall(t *testing.T) llm.Request {
	t.Helper()
	select {
	case <-c.callCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued user model request")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		t.Fatal("expected captured model request")
	}
	return c.calls[0]
}

func TestSyncExecutionTargetPersistsReminderBeforeQueuedUserAutoDrain(t *testing.T) {
	fixture, _ := newRuntimeServiceFixture(t)
	sessionID := fixture.store.Meta().SessionID
	client := newLifecycleRequestCaptureClient()
	targetWorkdir := t.TempDir()
	var engine *runtimepkg.Engine
	build := func(context.Context) (RuntimeBuildResult, error) {
		created, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		engine = created
		return RuntimeBuildResult{
			Engine: created,
			LocalRebind: func(string) error {
				created.QueueUserMessageForAutoDrain("queued after switch", "req-queued-after-switch")
				return nil
			},
			Close: func() { _ = created.Close() },
		}, nil
	}
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	if engine == nil {
		t.Fatal("expected active engine")
	}
	err := fixture.service.SyncExecutionTarget(context.Background(), sessionID, clientui.SessionExecutionTarget{
		WorkspaceRoot:    fixture.config.WorkspaceRoot,
		EffectiveWorkdir: targetWorkdir,
	}, &session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/queued-switch"),
			WorktreePath:  targetWorkdir,
			WorkspaceRoot: fixture.config.WorkspaceRoot,
			EffectiveCwd:  targetWorkdir,
		},
	})
	if err != nil {
		t.Fatalf("SyncExecutionTarget: %v", err)
	}
	req := client.firstCall(t)
	for _, item := range req.Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.Role == llm.RoleDeveloper &&
			item.MessageType == llm.MessageTypeWorktreeMode &&
			item.WorktreeContext != nil &&
			item.WorktreeContext.EffectiveCwd == targetWorkdir {
			return
		}
	}
	t.Fatalf("queued request missing worktree developer message: %+v", req.Items)
}

type armedWorktreeReminderFailObserver struct {
	armed atomic.Bool
}

func (o *armedWorktreeReminderFailObserver) ObservePersistedStore(context.Context, session.PersistedStoreSnapshot) error {
	if o.armed.Load() {
		return errors.New("observer persistence failed")
	}
	return nil
}

func TestSyncExecutionTargetRollsBackRebindWhenReminderPersistenceFails(t *testing.T) {
	observer := &armedWorktreeReminderFailObserver{}
	workspaceRoot := t.TempDir()
	store, err := session.Create(t.TempDir(), "workspace", workspaceRoot, session.WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	reg := registry.NewRuntimeRegistry()
	service := NewService("", nil, nil, nil, nil, nil, reg, registry.NewSessionStoreRegistry(), session.WithPersistenceObserver(observer))
	sessionID := store.Meta().SessionID
	targetWorkdir := t.TempDir()
	var (
		engine   *runtimepkg.Engine
		rebindMu sync.Mutex
		rebinds  []string
	)
	build := func(context.Context) (RuntimeBuildResult, error) {
		created, err := runtimepkg.New(store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		engine = created
		return RuntimeBuildResult{
			Engine: created,
			LocalRebind: func(dir string) error {
				rebindMu.Lock()
				rebinds = append(rebinds, dir)
				rebindMu.Unlock()
				return nil
			},
			Close: func() { _ = created.Close() },
		}, nil
	}
	if err := service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	if engine == nil {
		t.Fatal("expected active engine")
	}
	observer.armed.Store(true)
	err = service.SyncExecutionTarget(context.Background(), sessionID, clientui.SessionExecutionTarget{
		WorkspaceRoot:    workspaceRoot,
		EffectiveWorkdir: targetWorkdir,
	}, &session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/persist-fails"),
			WorktreePath:  targetWorkdir,
			WorkspaceRoot: workspaceRoot,
			EffectiveCwd:  targetWorkdir,
		},
	})
	if err == nil {
		t.Fatal("expected SyncExecutionTarget to fail when reminder persistence fails")
	}
	rebindMu.Lock()
	gotRebinds := append([]string(nil), rebinds...)
	rebindMu.Unlock()
	wantRebinds := []string{targetWorkdir, workspaceRoot}
	if !reflect.DeepEqual(gotRebinds, wantRebinds) {
		t.Fatalf("rebinds = %+v, want %+v", gotRebinds, wantRebinds)
	}
	if got := engine.TranscriptWorkingDir(); got != workspaceRoot {
		t.Fatalf("transcript workdir after rollback = %q, want %q", got, workspaceRoot)
	}
}

func TestSyncExecutionTargetFailsQueuedUserWorkWhenRollbackRebindFails(t *testing.T) {
	observer := &armedWorktreeReminderFailObserver{}
	workspaceRoot := t.TempDir()
	store, err := session.Create(t.TempDir(), "workspace", workspaceRoot, session.WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	reg := registry.NewRuntimeRegistry()
	service := NewService("", nil, nil, nil, nil, nil, reg, registry.NewSessionStoreRegistry(), session.WithPersistenceObserver(observer))
	sessionID := store.Meta().SessionID
	targetWorkdir := t.TempDir()
	client := newLifecycleRequestCaptureClient()
	var (
		engine      *runtimepkg.Engine
		rebindCount atomic.Int32
	)
	build := func(context.Context) (RuntimeBuildResult, error) {
		created, err := runtimepkg.New(store, client, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		engine = created
		return RuntimeBuildResult{
			Engine: created,
			LocalRebind: func(string) error {
				if rebindCount.Add(1) == 1 {
					created.QueueUserMessageForAutoDrain("queued during failing switch", "req-failing-switch")
					return nil
				}
				return errors.New("rollback rebind failed")
			},
			Close: func() { _ = created.Close() },
		}, nil
	}
	if err := service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	if engine == nil {
		t.Fatal("expected active engine")
	}
	observer.armed.Store(true)
	err = service.SyncExecutionTarget(context.Background(), sessionID, clientui.SessionExecutionTarget{
		WorkspaceRoot:    workspaceRoot,
		EffectiveWorkdir: targetWorkdir,
	}, &session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/rollback-rebind-fails"),
			WorktreePath:  targetWorkdir,
			WorkspaceRoot: workspaceRoot,
			EffectiveCwd:  targetWorkdir,
		},
	})
	if err == nil {
		t.Fatal("expected SyncExecutionTarget to fail when rollback rebind fails")
	}
	if got := rebindCount.Load(); got != 2 {
		t.Fatalf("rebind count = %d, want 2", got)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("queued user work must be failed when rollback cannot prove a coherent target")
	}
	select {
	case <-client.callCh:
		t.Fatal("queued user work reached the model after rollback failure")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "new work after failed switch"); !errors.Is(err, runtimepkg.ErrEngineClosed) {
		t.Fatalf("SubmitUserMessage after failed rollback = %v, want ErrEngineClosed", err)
	}
	var rebuiltEngine *runtimepkg.Engine
	rebuilt := false
	rebuild := func(context.Context) (RuntimeBuildResult, error) {
		created, err := runtimepkg.New(store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		rebuilt = true
		rebuiltEngine = created
		return RuntimeBuildResult{Engine: created, Close: func() { _ = created.Close() }}, nil
	}
	if err := service.AcquireRuntime(context.Background(), sessionID, "owner-b", rebuild); err != nil {
		t.Fatalf("AcquireRuntime after retired rollback failure: %v", err)
	}
	if !rebuilt {
		t.Fatal("expected acquire after retired rollback failure to build a fresh runtime")
	}
	if rebuiltEngine == nil || rebuiltEngine == engine {
		t.Fatalf("rebuilt engine = %p, old engine = %p", rebuiltEngine, engine)
	}
}

func TestIdleUnloadTimerReleasesOrphanedRuntime(t *testing.T) {
	fixture, reg := newRuntimeServiceFixture(t)
	fixture.service.idleUnloadDelay = 20 * time.Millisecond
	fixture.service.runFinishedUnloadDelay = 20 * time.Millisecond
	sessionID := fixture.store.Meta().SessionID

	client := &blockingLLMClient{entered: make(chan struct{}), release: make(chan struct{})}
	var engine *runtimepkg.Engine
	build := func(ctx context.Context) (RuntimeBuildResult, error) {
		e, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(), runtimepkg.Config{
			Model:   "gpt-5",
			OnEvent: func(evt runtimepkg.Event) { reg.PublishRuntimeEvent(sessionID, evt) },
		})
		if err != nil {
			return RuntimeBuildResult{}, err
		}
		engine = e
		return RuntimeBuildResult{Engine: e, Close: func() { _ = e.Close() }}, nil
	}
	if err := fixture.service.AcquireRuntime(context.Background(), sessionID, "owner-a", build); err != nil {
		t.Fatalf("AcquireRuntime: %v", err)
	}
	var once sync.Once
	finishRun := func() { once.Do(func() { close(client.release) }) }
	defer finishRun()
	runDone := make(chan struct{})
	go func() {
		_, _ = engine.SubmitUserMessage(context.Background(), "run")
		close(runDone)
	}()
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("active run did not start")
	}

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "owner-a", true, true))
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !resp.Active {
		t.Fatalf("release while active should orphan, got %+v", resp)
	}
	finishRun()
	<-runDone

	deadline := time.After(3 * time.Second)
	for reg.IsSessionRuntimeActive(sessionID) {
		select {
		case <-deadline:
			t.Fatal("orphaned idle runtime was not unloaded")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
