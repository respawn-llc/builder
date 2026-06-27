package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/auth"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	runtimepkg "core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type sessionRuntimeTestLLMClient struct {
	responses []llm.Response
}

func (c *sessionRuntimeTestLLMClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	if len(c.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	return resp, nil
}

type blockingLLMClient struct {
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
}

func (c *blockingLLMClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.enteredOnce.Do(func() { close(c.entered) })
	<-c.release
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

type sessionRuntimeTestTool struct {
	name toolspec.ID
}

func (t sessionRuntimeTestTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	out, _ := json.Marshal(map[string]string{"tool": string(t.name)})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

type patchDetailCapture struct {
	mu    sync.Mutex
	value string
}

func (c *patchDetailCapture) Set(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = value
}

func (c *patchDetailCapture) Get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func startRegisteredActiveRun(t *testing.T, fixture sessionRuntimeFixture, reg *registry.RuntimeRegistry) func() {
	t.Helper()
	client := &blockingLLMClient{entered: make(chan struct{}), release: make(chan struct{})}
	engine, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	reg.Register(fixture.store.Meta().SessionID, engine)
	t.Cleanup(func() { _ = engine.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "run")
		done <- err
	}()
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active run to start")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			close(client.release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("timed out waiting for active run to finish")
			}
		})
	}
}

func TestActivateSessionRuntimeWaitsForClosingHandleBeforeClaiming(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.authManager = auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "sk-test"},
		},
	}), nil, time.Now)
	ready := make(chan struct{})
	close(ready)
	handle := &runtimeHandle{
		closing: true,
		ready:   ready,
		closed:  make(chan struct{}),
	}
	fixture.service.handles = map[string]*runtimeHandle{fixture.store.Meta().SessionID: handle}

	done := make(chan serverapi.SessionRuntimeActivateResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
			ClientRequestID: "req-2",
			SessionID:       fixture.store.Meta().SessionID,
			ActiveSettings: config.Settings{
				Model: "gpt-5",
				Reviewer: config.ReviewerSettings{
					Frequency: "off",
				},
			},
		})
		done <- resp
		errCh <- err
	}()
	select {
	case <-done:
		t.Fatal("expected activation to wait for closing handle")
	default:
	}

	fixture.service.closeReleasedRuntimeHandle(fixture.store.Meta().SessionID, handle)
	if err := <-errCh; err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	<-done
	if fixture.service.handles[fixture.store.Meta().SessionID] == nil {
		t.Fatal("expected replacement activation to install a handle")
	}
}

func TestActivateSessionRuntimeReplaysDuplicateRequestAfterReady(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	handle := &runtimeHandle{
		ready: make(chan struct{}),
	}
	fixture.service.handles = map[string]*runtimeHandle{fixture.store.Meta().SessionID: handle}
	done := make(chan serverapi.SessionRuntimeActivateResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
			ClientRequestID: "req-1",
			SessionID:       fixture.store.Meta().SessionID,
		})
		done <- resp
		errCh <- err
	}()
	select {
	case <-done:
		t.Fatal("expected duplicate activation to wait for ready handle")
	default:
	}
	close(handle.ready)
	if err := <-errCh; err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	<-done
	if handle.ownerRefs != 1 {
		t.Fatalf("owner refs after replay = %d, want 1", handle.ownerRefs)
	}
}

func TestActivateSessionRuntimeReplayOwnerSurvivesOriginalDisconnect(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	closed := atomic.Int32{}
	handle := &runtimeHandle{
		ownerRefs: 1,
		ownerIDs:  map[string]struct{}{"owner-1": {}},
		ready:     make(chan struct{}),
		close: func() {
			closed.Add(1)
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle

	if _, err := fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-1",
		SessionID:       fixture.store.Meta().SessionID,
		OwnerID:         "owner-2",
	}); err != nil {
		t.Fatalf("ActivateSessionRuntime replay: %v", err)
	}
	release, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel-1",
		SessionID:       fixture.store.Meta().SessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
		OwnerID:         "owner-1",
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime original disconnect: %v", err)
	}
	if release.Released {
		t.Fatalf("release response = %+v, want unreleased while replay owner remains", release)
	}
	if closed.Load() != 0 {
		t.Fatalf("runtime closed despite replay owner, close count = %d", closed.Load())
	}
	if handle.ownerRefs != 1 {
		t.Fatalf("owner refs after original disconnect = %d, want replay owner", handle.ownerRefs)
	}
}

func TestActivateSessionRuntimeIdempotentReplayDoesNotDoubleCountSameOwner(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	closed := atomic.Int32{}
	handle := &runtimeHandle{
		ownerRefs: 1,
		ownerIDs:  map[string]struct{}{"owner-1": {}},
		ready:     make(chan struct{}),
		close: func() {
			closed.Add(1)
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle

	if _, err := fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-1",
		SessionID:       fixture.store.Meta().SessionID,
		OwnerID:         "owner-1",
	}); err != nil {
		t.Fatalf("ActivateSessionRuntime replay: %v", err)
	}
	if handle.ownerRefs != 1 {
		t.Fatalf("owner refs after same-owner replay = %d, want 1", handle.ownerRefs)
	}
	release, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel-1",
		SessionID:       fixture.store.Meta().SessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
		OwnerID:         "owner-1",
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !release.Released {
		t.Fatalf("release response = %+v, want released after single owner disconnect", release)
	}
	if closed.Load() != 1 {
		t.Fatalf("runtime close count = %d, want 1", closed.Load())
	}
}

func TestActivateSessionRuntimeHonorsCanceledContextBeforeInstallingHandle(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.service.ActivateSessionRuntime(ctx, serverapi.SessionRuntimeActivateRequest{ClientRequestID: "req-1", SessionID: fixture.store.Meta().SessionID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ActivateSessionRuntime error = %v, want context canceled", err)
	}
	if len(fixture.service.handles) != 0 {
		t.Fatalf("expected no installed handles after canceled activation, got %+v", fixture.service.handles)
	}
}

func TestActivateSessionRuntimeSurfacesErrorAndCleansHandleOnActivationFailure(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	_, err := fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-1",
		SessionID:       fixture.store.Meta().SessionID,
		EnabledToolIDs:  []string{"not-a-tool"},
	})
	if !errors.Is(err, errUnknownToolID) {
		t.Fatalf("ActivateSessionRuntime error = %v, want errUnknownToolID", err)
	}
	if len(fixture.service.handles) != 0 {
		t.Fatalf("expected activation failure to clean up handles, got %+v", fixture.service.handles)
	}
}

func TestActivateSessionRuntimeIgnoresRecoveredWarningProviderError(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.authManager = auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "sk-test"},
		},
	}), nil, time.Now)
	fixture.service.WithGeneratedRecoveredWarningProvider(func() (string, bool, error) {
		return "", false, errors.New("recovered dir unreadable")
	})

	if _, err := fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-warning-error",
		SessionID:       fixture.store.Meta().SessionID,
		ActiveSettings: config.Settings{
			Model: "gpt-5",
			Reviewer: config.ReviewerSettings{
				Frequency: "off",
			},
		},
	}); err != nil {
		t.Fatalf("ActivateSessionRuntime should ignore recovered warning lookup errors: %v", err)
	}
	if fixture.service.handles[fixture.store.Meta().SessionID] == nil {
		t.Fatal("expected runtime activation to install a handle")
	}
}

func TestAppendRecoveredWarningIfNeededPersistsOnce(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	warning := "generated warning"
	if err := fixture.store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	fixture.service.WithGeneratedRecoveredWarning(warning)
	if err := fixture.service.appendRecoveredWarningIfNeeded(fixture.store); err != nil {
		t.Fatalf("append warning: %v", err)
	}
	if err := fixture.service.appendRecoveredWarningIfNeeded(fixture.store); err != nil {
		t.Fatalf("append duplicate warning: %v", err)
	}
	count := 0
	if err := fixture.store.WalkEvents(func(evt session.Event) error {
		if evt.Kind != "local_entry" {
			return nil
		}
		var entry recoveredWarningEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			return err
		}
		if entry.Role == "warning" && entry.Text == warning {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk events: %v", err)
	}
	if count != 1 {
		t.Fatalf("warning count = %d, want 1", count)
	}
	if !fixture.store.Meta().GeneratedRecoveredWarningIssued {
		t.Fatal("expected generated recovered warning marker to be persisted")
	}
	reopened, err := session.OpenByID(fixture.config.PersistenceRoot, fixture.store.Meta().SessionID, fixture.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Meta().GeneratedRecoveredWarningIssued {
		t.Fatal("expected generated recovered warning marker to survive reopen")
	}
	if err := fixture.service.appendRecoveredWarningIfNeeded(reopened); err != nil {
		t.Fatalf("append warning after reopen: %v", err)
	}
	reopenedCount := 0
	if err := reopened.WalkEvents(func(evt session.Event) error {
		if evt.Kind != "local_entry" {
			return nil
		}
		var entry recoveredWarningEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			return err
		}
		if entry.Role == "warning" && entry.Text == warning {
			reopenedCount++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk reopened events: %v", err)
	}
	if reopenedCount != 1 {
		t.Fatalf("reopened warning count = %d, want 1", reopenedCount)
	}
}

func TestAppendRecoveredWarningIfNeededIgnoresProviderError(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.WithGeneratedRecoveredWarningProvider(func() (string, bool, error) {
		return "", false, errors.New("recovered dir unreadable")
	})
	if err := fixture.service.appendRecoveredWarningIfNeeded(fixture.store); err != nil {
		t.Fatalf("expected warning lookup errors to be non-fatal, got %v", err)
	}
}

func TestReleaseSessionRuntimeWaitsForHandleReadyBeforeClose(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	closed := make(chan struct{}, 1)
	handle := &runtimeHandle{
		ready: make(chan struct{}),
		close: func() {
			closed <- struct{}{}
		},
	}
	fixture.service.handles = map[string]*runtimeHandle{fixture.store.Meta().SessionID: handle}
	done := make(chan error, 1)
	go func() {
		_, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "rel-1",
			SessionID:       fixture.store.Meta().SessionID,
		})
		done <- err
	}()
	select {
	case <-closed:
		t.Fatal("expected release to wait for ready handle before close")
	default:
	}
	close(handle.ready)
	if err := <-done; err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("expected close after ready handle release")
	}
}

func TestReleaseSessionRuntimeOnlyIfIdleKeepsActiveRun(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimeRegistry := registry.NewRuntimeRegistry()
	fixture.service.runtimes = runtimeRegistry
	closed := atomic.Int32{}
	handle := &runtimeHandle{
		ownerRefs: 1,
		ready:     make(chan struct{}),
		close: func() {
			closed.Add(1)
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle
	release := startRegisteredActiveRun(t, fixture, runtimeRegistry)
	defer release()

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel-1",
		SessionID:       fixture.store.Meta().SessionID,
		OnlyIfIdle:      true,
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime OnlyIfIdle: %v", err)
	}
	if !resp.Active || resp.Released {
		t.Fatalf("release response = %+v, want active not released", resp)
	}
	if closed.Load() != 0 {
		t.Fatalf("runtime close count = %d, want 0", closed.Load())
	}
	if got := fixture.service.handles[fixture.store.Meta().SessionID]; got != handle {
		t.Fatalf("runtime handle = %+v, want preserved active handle", got)
	}
	if handle.ownerRefs != 1 {
		t.Fatalf("connected owner refs = %d, want preserved owner", handle.ownerRefs)
	}
}

func TestReleaseSessionRuntimeOnlyIfIdleDropsOwnerWhenRequested(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimeRegistry := registry.NewRuntimeRegistry()
	fixture.service.runtimes = runtimeRegistry
	handle := &runtimeHandle{
		ownerRefs: 1,
		ready:     make(chan struct{}),
		close:     func() {},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle
	release := startRegisteredActiveRun(t, fixture, runtimeRegistry)
	defer release()

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel-1",
		SessionID:       fixture.store.Meta().SessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime OnlyIfIdle DropOwner: %v", err)
	}
	if !resp.Active || resp.Released {
		t.Fatalf("release response = %+v, want active not released", resp)
	}
	if handle.ownerRefs != 0 {
		t.Fatalf("owner refs = %d, want dropped owner", handle.ownerRefs)
	}
}

func TestReleaseSessionRuntimeOnlyIfIdleKeepsSubscriberRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimeRegistry := registry.NewRuntimeRegistry()
	fixture.service.runtimes = runtimeRegistry
	engine, err := runtimepkg.New(fixture.store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimeRegistry.Register(fixture.store.Meta().SessionID, engine)
	closed := atomic.Int32{}
	handle := &runtimeHandle{
		ownerRefs: 1,
		ready:     make(chan struct{}),
		close: func() {
			closed.Add(1)
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle
	sub, err := runtimeRegistry.SubscribeSessionActivity(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel-1",
		SessionID:       fixture.store.Meta().SessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime OnlyIfIdle with subscriber: %v", err)
	}
	if resp.Released {
		t.Fatalf("release response = %+v, want unreleased while subscriber is connected", resp)
	}
	if closed.Load() != 0 {
		t.Fatalf("runtime close count = %d, want 0", closed.Load())
	}
	if handle.ownerRefs != 0 {
		t.Fatalf("owner refs = %d, want dropped owner", handle.ownerRefs)
	}
}

func TestReleaseSessionRuntimeOnlyIfIdleClosesIdleRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.runtimes = registry.NewRuntimeRegistry()
	closed := atomic.Int32{}
	handle := &runtimeHandle{
		ready: make(chan struct{}),
		close: func() {
			closed.Add(1)
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel-1",
		SessionID:       fixture.store.Meta().SessionID,
		OnlyIfIdle:      true,
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime OnlyIfIdle: %v", err)
	}
	if !resp.Released || resp.Active {
		t.Fatalf("release response = %+v, want released not active", resp)
	}
	if closed.Load() != 1 {
		t.Fatalf("runtime close count = %d, want 1", closed.Load())
	}
	if _, ok := fixture.service.handles[fixture.store.Meta().SessionID]; ok {
		t.Fatal("expected runtime handle removed after idle release")
	}
}

func TestRecreateRuntimeClosesExistingHandleBeforeRebuilding(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.runtimes = registry.NewRuntimeRegistry()
	sessionID := fixture.store.Meta().SessionID
	closed := atomic.Int32{}
	existing := &runtimeHandle{
		ownerRefs: 1,
		ownerIDs:  map[string]struct{}{"owner-A": {}},
		ready:     make(chan struct{}),
		close:     func() { closed.Add(1) },
	}
	close(existing.ready)
	fixture.service.handles[sessionID] = existing

	builderCalls := atomic.Int32{}
	wantErr := errors.New("build failed")
	_, err := fixture.service.RecreateRuntime(context.Background(), sessionID, "owner-B", func(context.Context) (RuntimeBuildResult, error) {
		builderCalls.Add(1)
		return RuntimeBuildResult{}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("RecreateRuntime err = %v, want %v", err, wantErr)
	}
	if closed.Load() != 1 {
		t.Fatalf("existing close count = %d, want 1 (overtake must close the prior engine)", closed.Load())
	}
	if builderCalls.Load() != 1 {
		t.Fatalf("builder calls = %d, want 1 (must rebuild after closing the prior engine)", builderCalls.Load())
	}
	if _, ok := fixture.service.handles[sessionID]; ok {
		t.Fatal("expected no handle after a failed rebuild")
	}
}

func TestReleaseSessionRuntimeFromNonOwnerDoesNotTearDownSharedRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.runtimes = registry.NewRuntimeRegistry()
	closed := atomic.Int32{}
	handle := &runtimeHandle{
		ownerRefs: 1,
		ownerIDs:  map[string]struct{}{"owner-A": {}},
		ready:     make(chan struct{}),
		close: func() {
			closed.Add(1)
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle

	resp, err := fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "rel-1",
		SessionID:       fixture.store.Meta().SessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
		OwnerID:         "owner-B",
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime non-owner: %v", err)
	}
	if !resp.Released {
		t.Fatalf("release response = %+v, want released no-op", resp)
	}
	if closed.Load() != 0 {
		t.Fatalf("runtime close count = %d, want 0 (non-owner must not close)", closed.Load())
	}
	current, ok := fixture.service.handles[fixture.store.Meta().SessionID]
	if !ok {
		t.Fatal("expected runtime handle to remain after non-owner release")
	}
	if current.ownerRefs != 1 {
		t.Fatalf("ownerRefs = %d, want 1 (non-owner must not drop another owner's ref)", current.ownerRefs)
	}
	if _, stillOwner := current.ownerIDs["owner-A"]; !stillOwner {
		t.Fatal("expected owner-A to remain after non-owner release")
	}
}

func TestIdleRuntimeUnloadReleasesOrphanedRuntimeAfterRunFinishes(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimeRegistry := registry.NewRuntimeRegistry()
	runtimeRegistry.SetInterestObserver(fixture.service.runtimeInterestChanged)
	fixture.service.runtimes = runtimeRegistry
	fixture.service.idleUnloadDelay = 100 * time.Millisecond
	fixture.service.runFinishedUnloadDelay = 40 * time.Millisecond
	closed := make(chan struct{}, 1)
	handle := &runtimeHandle{
		ownerRefs: 0,
		ready:     make(chan struct{}),
		close: func() {
			closed <- struct{}{}
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle
	release := startRegisteredActiveRun(t, fixture, runtimeRegistry)
	defer release()

	fixture.service.runtimeInterestChanged(fixture.store.Meta().SessionID, registry.RuntimeInterestChanged)
	select {
	case <-closed:
		t.Fatal("idle reaper closed runtime while run was active")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	fixture.service.runtimeInterestChanged(fixture.store.Meta().SessionID, registry.RuntimeInterestRunFinished)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for idle reaper to close orphaned runtime")
	}
}

func TestIdleRuntimeUnloadUsesThreeMinuteDefaultAfterRunFinishes(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	if fixture.service.runFinishedIdleUnloadDelay() != 3*time.Minute {
		t.Fatalf("run finished idle unload delay = %s, want 3m", fixture.service.runFinishedIdleUnloadDelay())
	}
}

func TestIdleRuntimeUnloadUsesRunFinishedDebounceInsteadOfDisconnectDebounce(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.runtimes = registry.NewRuntimeRegistry()
	fixture.service.idleUnloadDelay = 10 * time.Millisecond
	fixture.service.runFinishedUnloadDelay = 40 * time.Millisecond
	closed := make(chan struct{}, 1)
	handle := &runtimeHandle{
		ownerRefs: 0,
		ready:     make(chan struct{}),
		close: func() {
			closed <- struct{}{}
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle

	fixture.service.runtimeInterestChanged(fixture.store.Meta().SessionID, registry.RuntimeInterestRunFinished)
	select {
	case <-closed:
		t.Fatal("idle reaper used disconnect debounce after run finished")
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for run-finished idle reaper")
	}
}

func TestIdleRuntimeUnloadWaitsForTransientSubscriberReconnect(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimeRegistry := registry.NewRuntimeRegistry()
	runtimeRegistry.SetInterestObserver(fixture.service.runtimeInterestChanged)
	fixture.service.runtimes = runtimeRegistry
	fixture.service.idleUnloadDelay = 30 * time.Millisecond
	engine, err := runtimepkg.New(fixture.store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimeRegistry.Register(fixture.store.Meta().SessionID, engine)
	closed := make(chan struct{}, 1)
	handle := &runtimeHandle{
		ownerRefs: 0,
		ready:     make(chan struct{}),
		close: func() {
			closed <- struct{}{}
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle
	sub, err := runtimeRegistry.SubscribeSessionActivity(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	fixture.service.runtimeInterestChanged(fixture.store.Meta().SessionID, registry.RuntimeInterestChanged)
	select {
	case <-closed:
		t.Fatal("idle reaper closed runtime while subscriber was connected")
	case <-time.After(60 * time.Millisecond):
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close subscription: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for idle reaper to close after subscriber disconnect")
	}
}

func TestIdleRuntimeUnloadWaitsForSubscriberReconnectBeforeDebounce(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimeRegistry := registry.NewRuntimeRegistry()
	runtimeRegistry.SetInterestObserver(fixture.service.runtimeInterestChanged)
	fixture.service.runtimes = runtimeRegistry
	fixture.service.idleUnloadDelay = 40 * time.Millisecond
	engine, err := runtimepkg.New(fixture.store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimeRegistry.Register(fixture.store.Meta().SessionID, engine)
	closed := make(chan struct{}, 1)
	handle := &runtimeHandle{
		ownerRefs: 0,
		ready:     make(chan struct{}),
		close: func() {
			closed <- struct{}{}
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle

	fixture.service.runtimeInterestChanged(fixture.store.Meta().SessionID, registry.RuntimeInterestChanged)
	time.Sleep(15 * time.Millisecond)
	sub, err := runtimeRegistry.SubscribeSessionActivity(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("SubscribeSessionActivity reconnect: %v", err)
	}
	select {
	case <-closed:
		t.Fatal("idle reaper closed runtime despite subscriber reconnect before debounce")
	case <-time.After(80 * time.Millisecond):
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close subscription: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for idle reaper to close after reconnected subscriber disconnect")
	}
}

func TestIdleRuntimeUnloadIgnoresStaleTimerAfterOwnerReconnect(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.runtimes = registry.NewRuntimeRegistry()
	fixture.service.idleUnloadDelay = 30 * time.Millisecond
	closed := make(chan struct{}, 1)
	handle := &runtimeHandle{
		ownerRefs: 0,
		ready:     make(chan struct{}),
		close: func() {
			closed <- struct{}{}
		},
	}
	close(handle.ready)
	fixture.service.handles[fixture.store.Meta().SessionID] = handle

	fixture.service.runtimeInterestChanged(fixture.store.Meta().SessionID, registry.RuntimeInterestChanged)
	fixture.service.mu.Lock()
	handle.ownerRefs = 1
	fixture.service.mu.Unlock()
	fixture.service.cancelScheduledIdleUnload(fixture.store.Meta().SessionID)

	select {
	case <-closed:
		t.Fatal("idle reaper closed runtime after owner reconnected")
	case <-time.After(90 * time.Millisecond):
	}
	if got := fixture.service.handles[fixture.store.Meta().SessionID]; got != handle {
		t.Fatalf("runtime handle = %+v, want preserved handle after owner reconnect", got)
	}
}

func TestActiveRuntimeHandleReturnsActivationError(t *testing.T) {
	activationErr := errors.New("activation failed")
	handle := &runtimeHandle{activationErr: activationErr, ready: make(chan struct{})}
	close(handle.ready)
	svc := &Service{handles: map[string]*runtimeHandle{"session-1": handle}}

	resolved, err := svc.activeRuntimeHandle(context.Background(), "session-1")
	if !errors.Is(err, activationErr) {
		t.Fatalf("activeRuntimeHandle error = %v, want %v", err, activationErr)
	}
	if resolved != nil {
		t.Fatalf("activeRuntimeHandle returned handle %+v, want nil", resolved)
	}
}

func TestSyncExecutionTargetPersistsReminderWithoutActiveRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)

	err := fixture.service.SyncExecutionTarget(context.Background(), fixture.store.Meta().SessionID, clientui.SessionExecutionTarget{
		WorkspaceRoot:    " /tmp/workspace ",
		EffectiveWorkdir: " /tmp/workspace ",
	}, &session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeExit,
		Branch:        " feature/worktree ",
		WorktreePath:  " /tmp/worktree-a ",
		WorkspaceRoot: " /tmp/workspace ",
		EffectiveCwd:  " /tmp/workspace ",
	})
	if err != nil {
		t.Fatalf("SyncExecutionTarget: %v", err)
	}

	resolved, err := fixture.service.resolveStore(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolveStore: %v", err)
	}
	state := resolved.Meta().WorktreeReminder
	if state == nil {
		t.Fatal("expected persisted worktree reminder state")
	}
	if state.Mode != session.WorktreeReminderModeExit {
		t.Fatalf("mode = %q, want exit", state.Mode)
	}
	if state.Branch != "feature/worktree" {
		t.Fatalf("branch = %q, want feature/worktree", state.Branch)
	}
	if state.WorktreePath != "/tmp/worktree-a" {
		t.Fatalf("worktree path = %q, want /tmp/worktree-a", state.WorktreePath)
	}
	if state.EffectiveCwd != "/tmp/workspace" {
		t.Fatalf("effective cwd = %q, want /tmp/workspace", state.EffectiveCwd)
	}
}

func TestSyncExecutionTargetRebindsActiveRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimes := registry.NewRuntimeRegistry()
	fixture.service.runtimes = runtimes
	engine, err := runtimepkg.New(fixture.store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimes.Register(fixture.store.Meta().SessionID, engine)
	reboundRoot := ""
	handle := &runtimeHandle{
		ready: make(chan struct{}),
		rebind: func(root string) error {
			reboundRoot = root
			return nil
		},
	}
	close(handle.ready)
	fixture.service.handles = map[string]*runtimeHandle{fixture.store.Meta().SessionID: handle}

	err = fixture.service.SyncExecutionTarget(context.Background(), fixture.store.Meta().SessionID, clientui.SessionExecutionTarget{
		EffectiveWorkdir: " /tmp/workspace/pkg ",
	}, nil)
	if err != nil {
		t.Fatalf("SyncExecutionTarget: %v", err)
	}
	if reboundRoot != "/tmp/workspace/pkg" {
		t.Fatalf("rebound root = %q, want /tmp/workspace/pkg", reboundRoot)
	}
}

func TestSyncExecutionTargetRebindsExternalRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimes := registry.NewRuntimeRegistry()
	fixture.service.runtimes = runtimes
	patchText := "*** Begin Patch\n*** Add File: probe.txt\n+hello\n*** End Patch\n"
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "patching", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call-patch", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{"patch":` + strconv.Quote(patchText) + `}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	var detail patchDetailCapture
	engine, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: sessionRuntimeTestTool{name: toolspec.ToolPatch}}), runtimepkg.Config{
		Model:                "gpt-5",
		TranscriptWorkingDir: "/old-worktree",
		OnEvent: func(evt runtimepkg.Event) {
			if evt.Kind != runtimepkg.EventToolCallStarted || evt.ToolCall == nil {
				return
			}
			meta, ok := transcript.DecodeToolCallMeta(evt.ToolCall.Presentation)
			if ok {
				detail.Set(meta.PatchDetail)
			}
		},
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimes.Register(fixture.store.Meta().SessionID, engine)

	if err := fixture.service.SyncExecutionTarget(context.Background(), fixture.store.Meta().SessionID, clientui.SessionExecutionTarget{EffectiveWorkdir: "/new-worktree"}, nil); err != nil {
		t.Fatalf("SyncExecutionTarget: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "apply patch"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	gotDetail := detail.Get()
	if !strings.Contains(gotDetail, "/new-worktree/probe.txt") {
		t.Fatalf("expected patch detail to use retargeted external workdir, got %q", gotDetail)
	}
	if strings.Contains(gotDetail, "/old-worktree/probe.txt") {
		t.Fatalf("did not expect old workdir in patch detail, got %q", gotDetail)
	}
}

func TestSyncExecutionTargetUpdatesActiveRuntimePatchTranscriptWorkdir(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimes := registry.NewRuntimeRegistry()
	fixture.service.runtimes = runtimes
	patchText := "*** Begin Patch\n*** Add File: probe.txt\n+hello\n*** End Patch\n"
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "patching", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call-patch", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{"patch":` + strconv.Quote(patchText) + `}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	var detail patchDetailCapture
	engine, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: sessionRuntimeTestTool{name: toolspec.ToolPatch}}), runtimepkg.Config{
		Model:                "gpt-5",
		TranscriptWorkingDir: "/old-worktree",
		OnEvent: func(evt runtimepkg.Event) {
			if evt.Kind != runtimepkg.EventToolCallStarted || evt.ToolCall == nil {
				return
			}
			meta, ok := transcript.DecodeToolCallMeta(evt.ToolCall.Presentation)
			if ok {
				detail.Set(meta.PatchDetail)
			}
		},
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimes.Register(fixture.store.Meta().SessionID, engine)
	handle := &runtimeHandle{
		ready:  make(chan struct{}),
		rebind: runtimeRebindFunc(func(string) error { return nil }, engine),
	}
	close(handle.ready)
	fixture.service.handles = map[string]*runtimeHandle{fixture.store.Meta().SessionID: handle}

	if err := fixture.service.SyncExecutionTarget(context.Background(), fixture.store.Meta().SessionID, clientui.SessionExecutionTarget{EffectiveWorkdir: "/new-worktree"}, nil); err != nil {
		t.Fatalf("SyncExecutionTarget: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "apply patch"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	gotDetail := detail.Get()
	if !strings.Contains(gotDetail, "/new-worktree/probe.txt") {
		t.Fatalf("expected patch detail to use retargeted workdir, got %q", gotDetail)
	}
	if strings.Contains(gotDetail, "/old-worktree/probe.txt") {
		t.Fatalf("did not expect old workdir in patch detail, got %q", gotDetail)
	}
}

func TestSyncExecutionTargetDoesNotPersistReminderWhenActiveRuntimeRebindFails(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	runtimes := registry.NewRuntimeRegistry()
	fixture.service.runtimes = runtimes
	engine, err := runtimepkg.New(fixture.store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	runtimes.Register(fixture.store.Meta().SessionID, engine)
	handle := &runtimeHandle{
		ready: make(chan struct{}),
		rebind: func(string) error {
			return errors.New("rebind failed")
		},
	}
	close(handle.ready)
	fixture.service.handles = map[string]*runtimeHandle{fixture.store.Meta().SessionID: handle}

	err = fixture.service.SyncExecutionTarget(context.Background(), fixture.store.Meta().SessionID, clientui.SessionExecutionTarget{
		EffectiveWorkdir: "/tmp/workspace/pkg",
	}, &session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeExit,
		Branch:        "feature/worktree",
		WorktreePath:  "/tmp/worktree-a",
		WorkspaceRoot: "/tmp/workspace",
		EffectiveCwd:  "/tmp/workspace",
	})
	if err == nil || !strings.Contains(err.Error(), "rebind failed") {
		t.Fatalf("SyncExecutionTarget error = %v, want rebind failure", err)
	}

	resolved, err := fixture.service.resolveStore(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolveStore: %v", err)
	}
	if state := resolved.Meta().WorktreeReminder; state != nil {
		t.Fatalf("expected reminder state not persisted after failed rebind, got %+v", state)
	}
}

func TestRuntimeRebindDoesNotAdvanceTranscriptWorkdirWhenLocalRebindFails(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	patchText := "*** Begin Patch\n*** Add File: probe.txt\n+hello\n*** End Patch\n"
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "patching", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call-patch", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{"patch":` + strconv.Quote(patchText) + `}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	var detail patchDetailCapture
	engine, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: sessionRuntimeTestTool{name: toolspec.ToolPatch}}), runtimepkg.Config{
		Model:                "gpt-5",
		TranscriptWorkingDir: "/old-worktree",
		OnEvent: func(evt runtimepkg.Event) {
			if evt.Kind != runtimepkg.EventToolCallStarted || evt.ToolCall == nil {
				return
			}
			meta, ok := transcript.DecodeToolCallMeta(evt.ToolCall.Presentation)
			if ok {
				detail.Set(meta.PatchDetail)
			}
		},
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	defer func() { _ = engine.Close() }()
	rebindErr := runtimeRebindFunc(func(string) error { return errors.New("local rebind failed") }, engine)("/new-worktree")
	if rebindErr == nil || !strings.Contains(rebindErr.Error(), "local rebind failed") {
		t.Fatalf("runtimeRebindFunc error = %v, want local rebind failed", rebindErr)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "apply patch"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	gotDetail := detail.Get()
	if !strings.Contains(gotDetail, "/old-worktree/probe.txt") {
		t.Fatalf("expected patch detail to keep old workdir, got %q", gotDetail)
	}
	if strings.Contains(gotDetail, "/new-worktree/probe.txt") {
		t.Fatalf("did not expect failed rebind workdir in patch detail, got %q", gotDetail)
	}
}

func TestHasActiveRun(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	reg := registry.NewRuntimeRegistry()
	fixture.service.runtimes = reg
	if active, err := fixture.service.HasActiveRun(context.Background(), fixture.store.Meta().SessionID); err != nil || active {
		t.Fatalf("HasActiveRun before run = (%v, %v), want (false, nil)", active, err)
	}
	release := startRegisteredActiveRun(t, fixture, reg)
	defer release()
	active, err := fixture.service.HasActiveRun(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("HasActiveRun: %v", err)
	}
	if !active {
		t.Fatal("HasActiveRun = false, want true while run active")
	}
	release()
	if active, err := fixture.service.HasActiveRun(context.Background(), fixture.store.Meta().SessionID); err != nil || active {
		t.Fatalf("HasActiveRun after run = (%v, %v), want (false, nil)", active, err)
	}
}

func TestResolveStoreFallsBackThroughMetadataAuthority(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	resolved, err := fixture.service.resolveStore(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolveStore: %v", err)
	}
	if resolved.Meta().SessionID != fixture.store.Meta().SessionID {
		t.Fatalf("resolved session id = %q, want %q", resolved.Meta().SessionID, fixture.store.Meta().SessionID)
	}
}

func TestResolveStoreRejectsUnknownSession(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	_, err := fixture.service.resolveStore(context.Background(), "session-missing")
	if err == nil {
		t.Fatal("expected resolveStore to reject unknown session")
	}
}

func TestActivateSessionRuntimeRejectsPathLikeSessionID(t *testing.T) {
	svc := &Service{handles: make(map[string]*runtimeHandle)}
	_, err := svc.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-1",
		SessionID:       "../session-1",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestReleaseSessionRuntimeRejectsPathLikeSessionID(t *testing.T) {
	svc := &Service{handles: make(map[string]*runtimeHandle)}
	_, err := svc.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "req-1",
		SessionID:       "sessions/workspace-a/session-1",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

type sessionRuntimeFixture struct {
	config   config.App
	metadata *metadata.Store
	store    *session.Store
	service  *Service
}

func newSessionRuntimeFixture(t *testing.T) sessionRuntimeFixture {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	appCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore, err := metadata.Open(appCfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), appCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	projectSessionsDir := filepath.Join(filepath.Join(appCfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), appCfg.WorkspaceRoot, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.SetName("session-a"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	service := NewService(appCfg.PersistenceRoot, metadataStore, nil, nil, nil, nil, nil, registry.NewSessionStoreRegistry(), metadataStore.AuthoritativeSessionStoreOptions()...)
	return sessionRuntimeFixture{config: appCfg, metadata: metadataStore, store: store, service: service}
}
