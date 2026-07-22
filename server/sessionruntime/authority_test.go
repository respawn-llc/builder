package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/runtimewirefixture"
	"core/server/llm"
	"core/server/runlog"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type authorityLifecycleProbe struct {
	draining chan struct{}
	retain   AgentResourceRetainer
}

type authorityPromptEvent struct {
	resource  runtimeids.SessionResourceRef
	scopeID   runtimeids.ExecutionScopeID
	requestID string
	resolved  bool
}

type authorityPromptFeed chan authorityPromptEvent

type ownerlessRetirementLLMClient struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (c *ownerlessRetirementLLMClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	if call == 1 {
		close(c.firstStarted)
	}
	c.mu.Unlock()
	if call == 1 {
		select {
		case <-ctx.Done():
			return llm.Response{}, context.Cause(ctx)
		case <-c.releaseFirst:
		}
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *ownerlessRetirementLLMClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (f authorityPromptFeed) PromptPending(resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, req tools.AskQuestionRequest, _ time.Time) {
	f <- authorityPromptEvent{resource: resource, scopeID: scopeID, requestID: req.ID}
}

func (f authorityPromptFeed) PromptResolved(resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, requestID string) {
	f <- authorityPromptEvent{resource: resource, scopeID: scopeID, requestID: requestID, resolved: true}
}

func (p *authorityLifecycleProbe) ResourceReady(_ context.Context, _ AgentResourceDescriptor, _ *runtime.Engine, retain AgentResourceRetainer) error {
	p.retain = retain
	return nil
}

func (p *authorityLifecycleProbe) ResourceDraining(context.Context, AgentResourceDescriptor) error {
	p.draining <- struct{}{}
	return nil
}

func TestOpenRuntimeReturnsRunLoggerCreationError(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	if err := os.Mkdir(filepath.Join(fixture.store.Dir(), runlog.RunLogFileName), 0o755); err != nil {
		t.Fatalf("replace run log with directory: %v", err)
	}
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})

	_, err = fixture.authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
		Runtime:   &plan,
	})
	if err == nil {
		t.Fatal("open runtime succeeded when the run log could not be opened")
	}
	err = fixture.authority.WithCurrentRuntime(context.Background(), sessionID, func(context.Context, *runtime.Engine) error {
		return nil
	})
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("failed runtime activation lookup error = %v, want runtime unavailable", err)
	}
}

func TestStalePredecessorFinalizationCannotRemoveResumedSuccessor(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}

	predecessor := WorkflowExecutionRef{
		RunID:      workflow.RunID(uuid.NewString()),
		Generation: 1,
	}
	successor := WorkflowExecutionRef{
		RunID:      predecessor.RunID,
		Generation: 2,
	}
	type startResult struct {
		handle ExecutionHandle
		err    error
	}
	successorStarted := make(chan startResult, 1)
	successorCancellationGrace := 50 * time.Millisecond

	var authority *Authority
	authority = NewAuthority(AuthorityOptions{
		ExecutionFinalized: ExecutionFinalizedFunc(func(finalized WorkflowExecutionRef) {
			if finalized != predecessor {
				return
			}
			handle, startErr := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
				Workflow: &successor,
				Command: ScriptCommand{
					Path:              sleepPath,
					Args:              []string{"30"},
					CancellationGrace: &successorCancellationGrace,
				},
			})
			successorStarted <- startResult{handle: handle, err: startErr}
		}),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	predecessorHandle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: &predecessor,
		Command:  ScriptCommand{Path: truePath},
	})
	if err != nil {
		t.Fatalf("start predecessor: %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	if _, err := predecessorHandle.Wait(waitCtx); err != nil {
		t.Fatalf("wait predecessor: %v", err)
	}

	var successorResult startResult
	select {
	case successorResult = <-successorStarted:
	case <-waitCtx.Done():
		t.Fatal("successor was not admitted from predecessor finalization")
	}
	if successorResult.err != nil {
		t.Fatalf("start successor: %v", successorResult.err)
	}
	if successorResult.handle == nil {
		t.Fatal("successor handle is nil")
	}

	assertSuccessorCurrent := func(stage string) {
		t.Helper()
		current, ok := authority.ExecutionByWorkflow(successor)
		if !ok {
			t.Fatalf("%s: successor execution is not indexed", stage)
		}
		if current.Scope().ID() != successorResult.handle.Scope().ID() {
			t.Fatalf(
				"%s: indexed scope = %q, want successor scope %q",
				stage,
				current.Scope().ID(),
				successorResult.handle.Scope().ID(),
			)
		}
	}
	assertSuccessorCurrent("after predecessor wait")

	if err := predecessorHandle.Close(waitCtx); err != nil {
		t.Fatalf("close predecessor: %v", err)
	}
	if err := predecessorHandle.Close(waitCtx); err != nil {
		t.Fatalf("close predecessor again: %v", err)
	}
	assertSuccessorCurrent("after predecessor close")

	if err := successorResult.handle.Stop(waitCtx); err != nil {
		t.Fatalf("stop successor: %v", err)
	}
	if err := successorResult.handle.Close(waitCtx); err != nil {
		t.Fatalf("close successor: %v", err)
	}
}

func TestNewLazyWithIDUsesExactCanonicalSessionIdentity(t *testing.T) {
	containerDir := t.TempDir()
	sessionID := runtimeids.NewSessionID()
	store, err := session.NewLazyWithID(
		sessionID,
		containerDir,
		"sessions",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("new lazy with id: %v", err)
	}
	if store.Meta().SessionID != sessionID.String() {
		t.Fatalf("session id = %q, want %q", store.Meta().SessionID, sessionID)
	}
	wantDir := filepath.Join(containerDir, sessionID.String())
	if store.Dir() != wantDir {
		t.Fatalf("session dir = %q, want %q", store.Dir(), wantDir)
	}
}

func TestNewLazyWithIDRejectsNonCanonicalNewSessionIdentity(t *testing.T) {
	legacy, err := runtimeids.ParseSessionID("session-legacy")
	if err != nil {
		t.Fatalf("parse legacy session id: %v", err)
	}
	for name, sessionID := range map[string]runtimeids.SessionID{
		"zero":   {},
		"legacy": legacy,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := session.NewLazyWithID(
				sessionID,
				t.TempDir(),
				"sessions",
				t.TempDir(),
				sessioncontract.SessionCategoryMain,
			)
			if err == nil {
				t.Fatal("new lazy session accepted a non-canonical identity")
			}
		})
	}
}

func TestNewLazyWithIDPreservesCategoryValidation(t *testing.T) {
	_, err := session.NewLazyWithID(
		runtimeids.NewSessionID(),
		t.TempDir(),
		"sessions",
		t.TempDir(),
		sessioncontract.SessionCategory("invalid"),
	)
	if err == nil {
		t.Fatal("new lazy session accepted an invalid category")
	}
}

func TestCloseIfIdleRetiresOwnerlessRuntimeAfterCurrentExecutionFinishes(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	authority := newAuthorityWithEventFeed(t, fixture, func(AgentResourceDescriptor, runtime.Event) {})
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)

	entered := make(chan struct{})
	finish := make(chan struct{})
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runner: func(ctx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			if err := bridge.WithEngine(ctx, func(_ context.Context, engine *runtime.Engine) error {
				engine.QueueUserMessage("queued during current execution")
				return nil
			}); err != nil {
				return err
			}
			close(entered)
			<-finish
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start current agent execution: %v", err)
	}
	<-entered

	release, err := attachment.Release(context.Background(), RuntimeReleaseCloseIfIdle)
	if err != nil {
		t.Fatalf("release active runtime: %v", err)
	}
	if !release.Active || release.Released {
		t.Fatalf("active release = %+v, want active pending retirement", release)
	}
	accessErr := authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
		return nil
	})

	close(finish)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait current agent execution: %v", err)
	}
	if !errors.Is(accessErr, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("ownerless retiring runtime accepted new callback: %v", accessErr)
	}
	assertRuntimeUnavailable(t, authority, attachment.Resource(), "execution finished")
}

func TestCloseIfIdleFailsQueuedWorkAndRetiresOwnerlessRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	var statusMu sync.Mutex
	var statuses []runtime.QueuedUserMessageStatusEvent
	authority := newAuthorityWithEventFeed(t, fixture, func(_ AgentResourceDescriptor, event runtime.Event) {
		if event.QueuedUserMessageStatus == nil {
			return
		}
		statusMu.Lock()
		statuses = append(statuses, *event.QueuedUserMessageStatus)
		statusMu.Unlock()
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
		engine.QueueUserMessage("queued before disconnect")
		return nil
	}); err != nil {
		t.Fatalf("queue user message: %v", err)
	}

	release, err := attachment.Release(context.Background(), RuntimeReleaseCloseIfIdle)
	if err != nil {
		t.Fatalf("release queued runtime: %v", err)
	}
	if !release.Released || release.Active {
		t.Fatalf("queued release = %+v, want immediate retirement", release)
	}
	assertRuntimeUnavailable(t, authority, attachment.Resource(), "queued release")

	statusMu.Lock()
	defer statusMu.Unlock()
	if len(statuses) != 2 ||
		statuses[0].Status != runtime.QueuedUserMessageAccepted ||
		statuses[1].Status != runtime.QueuedUserMessageFailed ||
		statuses[1].FailureReason != runtime.QueuedUserMessageFailureClosing {
		t.Fatalf("queued message statuses = %+v, want accepted then failed-closing", statuses)
	}
}

func TestCloseIfIdleRetiresOwnerlessRuntimeAfterCallbackFinishes(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	authority := fixture.authority
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)

	entered := make(chan struct{})
	finish := make(chan struct{})
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
			close(entered)
			<-finish
			return nil
		})
	}()
	<-entered

	release, err := attachment.Release(context.Background(), RuntimeReleaseCloseIfIdle)
	if err != nil {
		t.Fatalf("release callback-active runtime: %v", err)
	}
	if !release.Active || release.Released {
		t.Fatalf("callback-active release = %+v, want active pending retirement", release)
	}

	close(finish)
	if err := <-callbackDone; err != nil {
		t.Fatalf("runtime callback: %v", err)
	}
	assertRuntimeUnavailable(t, authority, attachment.Resource(), "callback finished")
}

func TestCloseIfIdleRetiresOwnerlessRuntimeAfterRetentionRelease(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	lifecycle := &authorityLifecycleProbe{draining: make(chan struct{}, 1)}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	if lifecycle.retain == nil {
		t.Fatal("resource lifecycle did not expose retention")
	}
	retention, err := lifecycle.retain()
	if err != nil {
		t.Fatalf("retain runtime: %v", err)
	}

	release, err := attachment.Release(context.Background(), RuntimeReleaseCloseIfIdle)
	if err != nil {
		t.Fatalf("release retained runtime: %v", err)
	}
	if !release.Active || release.Released {
		t.Fatalf("retained release = %+v, want active pending retirement", release)
	}

	if err := retention.Close(); err != nil {
		t.Fatalf("release runtime retention: %v", err)
	}
	assertRuntimeUnavailable(t, authority, attachment.Resource(), "retention released")
}

func TestCloseIfIdleRetiresAfterActiveAutoDrainFinishes(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := &blockingLLMClient{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	plan := authorityTestRuntimePlan(t, fixture, client)
	authority := fixture.authority
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
		engine.QueueUserMessageForAutoDrain("queued before disconnect", "queued-request")
		return nil
	}); err != nil {
		t.Fatalf("queue auto-drained user message: %v", err)
	}
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("auto-drained model request did not start")
	}

	type releaseResult struct {
		result RuntimeReleaseResult
		err    error
	}
	released := make(chan releaseResult, 1)
	go func() {
		result, err := attachment.Release(context.Background(), RuntimeReleaseCloseIfIdle)
		released <- releaseResult{result: result, err: err}
	}()
	select {
	case outcome := <-released:
		if outcome.err != nil {
			t.Fatalf("release auto-draining runtime: %v", outcome.err)
		}
		if !outcome.result.Active || outcome.result.Released {
			t.Fatalf("auto-draining release = %+v, want active pending retirement", outcome.result)
		}
	case <-time.After(time.Second):
		close(client.release)
		<-released
		t.Fatal("close-if-idle blocked on active auto-drain instead of recording retirement")
	}

	close(client.release)
	waitRuntimeUnavailable(t, authority, attachment.Resource(), "auto-drain finished")
}

func TestCloseIfIdleDrainsAcceptedQueuedWorkBeforeOwnerlessRetirement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := &ownerlessRetirementLLMClient{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	var statusMu sync.Mutex
	var statuses []runtime.QueuedUserMessageStatusEvent
	authority := newAuthorityWithEventFeed(t, fixture, func(_ AgentResourceDescriptor, event runtime.Event) {
		if event.QueuedUserMessageStatus == nil {
			return
		}
		statusMu.Lock()
		statuses = append(statuses, *event.QueuedUserMessageStatus)
		statusMu.Unlock()
	})
	plan := authorityTestRuntimePlan(t, fixture, client)
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runner: func(ctx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			return bridge.WithEngine(ctx, func(_ context.Context, engine *runtime.Engine) error {
				_, submitErr := engine.SubmitUserMessage(ctx, "active turn")
				return submitErr
			})
		},
	})
	if err != nil {
		t.Fatalf("start current agent execution: %v", err)
	}
	select {
	case <-client.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("active model request did not start")
	}
	if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
		engine.QueueUserMessageForAutoDrain("queued after active turn", "queued-request")
		return nil
	}); err != nil {
		t.Fatalf("queue follow-up user message: %v", err)
	}

	release, err := attachment.Release(context.Background(), RuntimeReleaseCloseIfIdle)
	if err != nil {
		t.Fatalf("release active runtime: %v", err)
	}
	if !release.Active || release.Released {
		t.Fatalf("active release = %+v, want active pending retirement", release)
	}
	close(client.releaseFirst)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait current agent execution: %v", err)
	}
	waitRuntimeUnavailable(t, authority, attachment.Resource(), "queued auto-drain completion")
	if calls := client.callCount(); calls != 2 {
		t.Fatalf("model calls = %d, want accepted queued follow-up drained before retirement", calls)
	}

	statusMu.Lock()
	defer statusMu.Unlock()
	if len(statuses) != 2 ||
		statuses[0].Status != runtime.QueuedUserMessageAccepted ||
		statuses[1].Status != runtime.QueuedUserMessageSubmitted {
		t.Fatalf("queued message statuses = %+v, want accepted then submitted", statuses)
	}
}

func TestRuntimeReleaseCloseInterruptsActiveAutoDrain(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := &ownerlessRetirementLLMClient{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	plan := authorityTestRuntimePlan(t, fixture, client)
	authority := fixture.authority
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
		engine.QueueUserMessageForAutoDrain("auto-drain before forced close", "queued-request")
		return nil
	}); err != nil {
		t.Fatalf("queue auto-drained user message: %v", err)
	}
	select {
	case <-client.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("auto-drained model request did not start")
	}

	type releaseResult struct {
		result RuntimeReleaseResult
		err    error
	}
	released := make(chan releaseResult, 1)
	go func() {
		result, err := attachment.Release(context.Background(), RuntimeReleaseClose)
		released <- releaseResult{result: result, err: err}
	}()
	select {
	case outcome := <-released:
		if outcome.err != nil {
			t.Fatalf("force-close active auto-drain: %v", outcome.err)
		}
		if !outcome.result.Released || outcome.result.Active {
			t.Fatalf("forced release = %+v, want closed runtime", outcome.result)
		}
	case <-time.After(time.Second):
		close(client.releaseFirst)
		<-released
		t.Fatal("forced close blocked instead of interrupting active auto-drain")
	}
	assertRuntimeUnavailable(t, authority, attachment.Resource(), "forced auto-drain close")
}

func TestAuthorityCloseInterruptsActiveAutoDrain(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := &ownerlessRetirementLLMClient{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	plan := authorityTestRuntimePlan(t, fixture, client)
	authority := fixture.authority
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
		engine.QueueUserMessageForAutoDrain("auto-drain before authority close", "queued-request")
		return nil
	}); err != nil {
		t.Fatalf("queue auto-drained user message: %v", err)
	}
	select {
	case <-client.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("auto-drained model request did not start")
	}

	closed := make(chan error, 1)
	go func() {
		closed <- authority.Close(context.Background())
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close authority with active auto-drain: %v", err)
		}
	case <-time.After(time.Second):
		close(client.releaseFirst)
		<-closed
		t.Fatal("authority close blocked instead of interrupting active auto-drain")
	}
	assertRuntimeUnavailable(t, authority, attachment.Resource(), "authority auto-drain close")
}

func TestDetachKeepsOwnerlessRuntimeAvailable(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	authority := fixture.authority
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)

	release, err := attachment.Release(context.Background(), RuntimeReleaseDetach)
	if err != nil {
		t.Fatalf("detach runtime: %v", err)
	}
	if !release.Released || release.Active {
		t.Fatalf("detach release = %+v, want released attachment with retained runtime", release)
	}
	if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
		return nil
	}); err != nil {
		t.Fatalf("detached ownerless runtime is unavailable: %v", err)
	}
}

func assertRuntimeUnavailable(t *testing.T, authority *Authority, resource runtimeids.SessionResourceRef, stage string) {
	t.Helper()
	err := authority.WithRuntime(context.Background(), resource, func(context.Context, *runtime.Engine) error {
		return nil
	})
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("ownerless runtime remained available after %s: %v", stage, err)
	}
}

func waitRuntimeUnavailable(t *testing.T, authority *Authority, resource runtimeids.SessionResourceRef, stage string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := authority.WithRuntime(context.Background(), resource, func(context.Context, *runtime.Engine) error {
			return nil
		})
		if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
			return
		}
		if err != nil {
			t.Fatalf("runtime availability after %s: %v", stage, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("ownerless runtime remained available after %s", stage)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newAuthorityWithEventFeed(t *testing.T, fixture sessionRuntimeFixture, feed AgentResourceEventFeed) *Authority {
	t.Helper()
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		EventFeed:       feed,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	return authority
}

func TestNewLazyStillAllocatesCanonicalSessionIdentity(t *testing.T) {
	containerDir := t.TempDir()
	store, err := session.NewLazy(
		containerDir,
		"sessions",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("new lazy session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse allocated session id: %v", err)
	}
	if !sessionID.IsCanonicalUUIDv4() {
		t.Fatalf("allocated session id %q is not canonical UUIDv4", sessionID)
	}
	wantDir := filepath.Join(containerDir, sessionID.String())
	if store.Dir() != wantDir {
		t.Fatalf("session dir = %q, want %q", store.Dir(), wantDir)
	}
}

func TestExactWorkflowExecutionCannotBeLiveAsAgentAndScript(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	workflowRef := WorkflowExecutionRef{
		RunID:      workflow.RunID(uuid.NewString()),
		Generation: 1,
	}
	agent, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   &workflowRef,
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, _ ExecutionScope, _ AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	script, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: &workflowRef,
		Command:  ScriptCommand{Path: truePath},
	})
	if err == nil {
		if script != nil {
			_ = script.Close(context.Background())
		}
		t.Fatal("same exact workflow execution was admitted as both agent and script")
	}

	if err := agent.Stop(context.Background()); err != nil {
		t.Fatalf("stop agent execution: %v", err)
	}
}

func TestStaleRuntimeAttachmentReleaseCannotAffectReplacement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	first, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open first runtime: %v", err)
	}
	replacement, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   ReplaceAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("replace runtime: %v", err)
	}
	if _, err := replacement.Wait(context.Background()); err != nil {
		t.Fatalf("wait replacement execution: %v", err)
	}
	second, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-b",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open replacement runtime: %v", err)
	}
	if first.Resource() == second.Resource() {
		t.Fatal("replacement reused the retired resource generation")
	}

	if _, err := first.Release(context.Background(), RuntimeReleaseDetach); err != nil {
		t.Fatalf("release stale attachment: %v", err)
	}
	if err := authority.WithRuntime(context.Background(), second.Resource(), func(context.Context, *runtime.Engine) error {
		return nil
	}); err != nil {
		t.Fatalf("stale release affected replacement: %v", err)
	}
	if _, err := second.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release replacement attachment: %v", err)
	}
}

func TestResourceRetentionBlocksReplacementUntilReleased(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	lifecycle := &authorityLifecycleProbe{draining: make(chan struct{}, 2)}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	_, err = authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	retention, err := lifecycle.retain()
	if err != nil {
		t.Fatalf("retain resource: %v", err)
	}
	if _, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   ReplaceAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	}); err == nil {
		t.Fatal("replacement succeeded while an exact resource retention was live")
	}
	if err := retention.Close(); err != nil {
		t.Fatalf("release resource retention: %v", err)
	}
	if err := retention.Close(); err != nil {
		t.Fatalf("release resource retention again: %v", err)
	}
	replacement, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   ReplaceAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	})
	if err != nil {
		t.Fatalf("replace after retention release: %v", err)
	}
	if _, err := replacement.Wait(context.Background()); err != nil {
		t.Fatalf("wait replacement: %v", err)
	}
}

func TestAgentExecutionBindsAndClearsShellCorrelation(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(20 * time.Millisecond))
	if err != nil {
		t.Fatalf("new shell manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	toolResponse := func(callID string) llm.Response {
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("scoped"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:    callID,
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"sleep 5","shell":"/bin/sh","login":false,"yield_time_ms":20}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		}
	}
	done := llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		toolResponse("call-scoped"), done, toolResponse("call-idle"), done,
	}}
	settings := fixture.config.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.MinimumExecToBgSeconds = 1
	settings.ShellOutputMaxChars = 16_000
	settings.Reviewer.Frequency = "off"
	plan, err := NewAgentRuntimePlan(AgentRuntimePlanOptions{
		Settings:     settings,
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
		Workdir:      fixture.config.WorkspaceRoot,
		Client:       client,
	})
	if err != nil {
		t.Fatalf("new runtime plan: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		Background:      manager,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	startBackground := func(callID string) (shelltool.Snapshot, error) {
		before := make(map[string]struct{})
		for _, snapshot := range manager.List() {
			before[snapshot.ID] = struct{}{}
		}
		if err := authority.WithCurrentRuntime(context.Background(), sessionID, func(ctx context.Context, engine *runtime.Engine) error {
			_, submitErr := engine.SubmitUserMessage(ctx, callID)
			return submitErr
		}); err != nil {
			return shelltool.Snapshot{}, err
		}
		for _, snapshot := range manager.List() {
			if _, existed := before[snapshot.ID]; !existed {
				return snapshot, nil
			}
		}
		return shelltool.Snapshot{}, fmt.Errorf("new background process is unavailable")
	}

	type backgroundStartResult struct {
		snapshot shelltool.Snapshot
		err      error
	}
	attachment, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "test-owner",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	started := make(chan backgroundStartResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			snapshot, startErr := startBackground("scoped")
			started <- backgroundStartResult{snapshot: snapshot, err: startErr}
			return startErr
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	startResult := <-started
	if startResult.err != nil {
		t.Fatalf("start scoped process: %v", startResult.err)
	}
	scoped := startResult.snapshot
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait agent execution: %v", err)
	}
	resource, ok := handle.Scope().Resource()
	if !ok {
		t.Fatal("agent scope has no resource")
	}
	want, err := runtimeids.NewExecutionCorrelation(handle.Scope().ID(), resource.Generation())
	if err != nil {
		t.Fatalf("new expected correlation: %v", err)
	}
	if scoped.ExecutionCorrelation == nil || *scoped.ExecutionCorrelation != want {
		t.Fatalf("scoped process correlation = %#v, want %#v", scoped.ExecutionCorrelation, want)
	}

	unscoped, err := startBackground("idle")
	if err != nil {
		t.Fatalf("start idle process: %v", err)
	}
	if unscoped.ExecutionCorrelation != nil {
		t.Fatalf("idle process correlation = %#v, want nil", *unscoped.ExecutionCorrelation)
	}
	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release runtime: %v", err)
	}
}

func TestBackgroundEventRoutesOnlyToExactCurrentResourceGeneration(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	updates := make(chan runtime.BackgroundShellEvent, 1)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{}, func(event runtime.Event) {
		if event.Kind == runtime.EventBackgroundUpdated && event.Background != nil {
			updates <- *event.Background
		}
	})
	authority := fixture.authority

	predecessor := openLifecycleRuntime(t, authority, sessionID, "predecessor", &plan)
	if _, err := predecessor.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release predecessor runtime: %v", err)
	}
	successor := openLifecycleRuntime(t, authority, sessionID, "successor", &plan)

	event := runtimewirefixture.BackgroundCompletionEvent("1000", sessionID.String(), t.TempDir())
	event.NoticeSuppressed = true
	route := func(generation runtimeids.ResourceGeneration) {
		correlation, err := runtimeids.NewExecutionCorrelation(runtimeids.NewExecutionScopeID(), generation)
		if err != nil {
			t.Fatalf("new execution correlation: %v", err)
		}
		event.Snapshot.ExecutionCorrelation = &correlation
		authority.routeBackgroundEvent(event)
	}
	route(predecessor.Resource().Generation())
	select {
	case update := <-updates:
		t.Fatalf("stale predecessor generation routed background update: %+v", update)
	default:
	}

	route(successor.Resource().Generation())
	select {
	case update := <-updates:
		if update.ID != event.Snapshot.ID || update.ActivityID != event.Snapshot.ActivityID {
			t.Fatalf("current generation background update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("current resource generation did not receive background update")
	}
}

func TestDormantSessionStoreCallbacksAreSerialized(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- authority.WithSessionStore(context.Background(), descriptor, func(context.Context, *session.Store) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- authority.WithSessionStore(context.Background(), descriptor, func(context.Context, *session.Store) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second dormant Store callback overlapped the first")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Store callback: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("second dormant Store callback did not enter after the first completed")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Store callback: %v", err)
	}
}

func TestAuthorityMaterializesCreateSessionDescriptor(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := runtimeids.NewSessionID()
	containerDir := filepath.Dir(fixture.store.Dir())
	descriptor, err := session.NewCreateSessionDescriptor(
		sessionID,
		containerDir,
		filepath.Base(containerDir),
		fixture.config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("new create session descriptor: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})

	err = authority.WithSessionStore(context.Background(), descriptor, func(_ context.Context, store *session.Store) error {
		if store.Meta().SessionID != sessionID.String() {
			t.Fatalf("materialized session id = %q, want %q", store.Meta().SessionID, sessionID)
		}
		wantDir := filepath.Join(containerDir, sessionID.String())
		if store.Dir() != wantDir {
			t.Fatalf("materialized session dir = %q, want %q", store.Dir(), wantDir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("with materialized session store: %v", err)
	}
	reopened, err := session.OpenByID(
		fixture.config.PersistenceRoot,
		sessionID.String(),
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("reopen materialized session: %v", err)
	}
	if reopened.Meta().SessionID != sessionID.String() {
		t.Fatalf("reopened session id = %q, want %q", reopened.Meta().SessionID, sessionID)
	}
}

func TestPromptResponseResolvesCurrentExactExecutionScope(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	feed := make(authorityPromptFeed, 2)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		PromptFeed:      feed,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	askID := uuid.NewString()
	request := tools.AskQuestionRequest{
		ID: askID, StepID: uuid.NewString(), Question: "Proceed?",
	}
	responseDone := make(chan executionPromptResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			response, askErr := authority.AwaitPromptResponse(ctx, scope.ID(), request)
			responseDone <- executionPromptResult{response: response, err: askErr}
			return askErr
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}

	resource, _ := handle.Scope().Resource()
	pending := <-feed
	if pending != (authorityPromptEvent{resource: resource, scopeID: handle.Scope().ID(), requestID: askID}) {
		t.Fatalf("pending prompt = %+v, want exact resource %v scope %s ask %s", pending, resource, handle.Scope().ID(), askID)
	}

	want := tools.AskQuestionResponse{RequestID: askID, Answer: "yes"}
	if err := authority.SubmitPromptResponse(sessionID, want, nil); err != nil {
		t.Fatalf("submit prompt response: %v", err)
	}
	resolved := <-feed
	if resolved != (authorityPromptEvent{resource: resource, scopeID: handle.Scope().ID(), requestID: askID, resolved: true}) {
		t.Fatalf("resolved prompt = %+v, want exact resource %v scope %s ask %s", resolved, resource, handle.Scope().ID(), askID)
	}
	if result := <-responseDone; result.err != nil || result.response != want {
		t.Fatalf("prompt response = %+v error = %v, want %+v", result.response, result.err, want)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait agent execution: %v", err)
	}
}

func authorityTestRuntimePlan(t *testing.T, fixture sessionRuntimeFixture, client llm.Client, onEvent ...func(runtime.Event)) AgentRuntimePlan {
	settings := fixture.config.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "off"
	options := AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  fixture.config.WorkspaceRoot,
		Client:   client,
	}
	if len(onEvent) != 0 {
		options.OnEvent = onEvent[0]
	}
	plan, err := NewAgentRuntimePlan(options)
	if err != nil {
		t.Fatalf("new authority test runtime plan: %v", err)
	}
	return plan
}

func mustOpenSessionDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	return descriptor
}
