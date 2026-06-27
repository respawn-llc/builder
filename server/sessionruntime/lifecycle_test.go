package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/registry"
	runtimepkg "core/server/runtime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
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

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), releaseRequest(sessionID, "", true, false))
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
