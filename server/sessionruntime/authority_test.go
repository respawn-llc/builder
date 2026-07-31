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

type authorityAutoReleaseLifecycle struct {
	release func() error
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

func (l *authorityAutoReleaseLifecycle) ResourceReady(_ context.Context, _ AgentResourceDescriptor, _ *runtime.Engine, retain AgentResourceRetainer) error {
	retention, err := retain()
	if err != nil {
		return err
	}
	l.release = retention.Close
	return nil
}

func (l *authorityAutoReleaseLifecycle) ResourceDraining(context.Context, AgentResourceDescriptor) error {
	if l.release == nil {
		return nil
	}
	return l.release()
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

	predecessor := workflowExecutionRefForTest(t, workflow.TaskID(uuid.NewString()), workflow.NodeID(uuid.NewString()), nil)
	successor := predecessor
	type startResult struct {
		handle ExecutionHandle
		err    error
	}
	successorStarted := make(chan startResult, 1)
	successorCancellationGrace := 50 * time.Millisecond
	var predecessorScopeID runtimeids.ExecutionScopeID

	var authority *Authority
	authority = NewAuthority(AuthorityOptions{
		ExecutionFinalized: ExecutionFinalizedFunc(func(finalized ExecutionScope) {
			finalizedRef, ok := finalized.Workflow()
			if !ok || !finalizedRef.CurrentNode.Equal(predecessor.CurrentNode) || finalized.ID() != predecessorScopeID {
				return
			}
			handle, startErr := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
				Workflow: releasedWorkflowLeaseForTest(t, authority, successor),
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
	predecessorLease, err := authority.NewWorkflowExecutionLease(predecessor)
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease predecessor: %v", err)
	}
	predecessorScopeID = predecessorLease.ScopeID()
	predecessorLease.Release()

	predecessorHandle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: &predecessorLease,
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
	if accessErr != nil {
		t.Fatalf("ready ownerless runtime rejected callback before retirement: %v", accessErr)
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

func TestExecutionRetirementKeepsRetainedRuntimeSteerableUntilDrain(t *testing.T) {
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
	executionStarted := make(chan struct{})
	finishExecution := make(chan struct{})
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   OpenAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			close(executionStarted)
			<-finishExecution
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start ownerless execution: %v", err)
	}
	resource, hasResource := handle.Scope().Resource()
	if !hasResource {
		t.Fatal("ownerless agent execution has no resource")
	}
	<-executionStarted
	if lifecycle.retain == nil {
		t.Fatal("resource lifecycle did not expose retention")
	}
	retention, err := lifecycle.retain()
	if err != nil {
		t.Fatalf("retain execution resource: %v", err)
	}

	close(finishExecution)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait ownerless execution: %v", err)
	}
	if err := authority.WithRuntime(context.Background(), resource, func(_ context.Context, engine *runtime.Engine) error {
		item := engine.QueueUserMessage("steer retained runtime")
		if !engine.DiscardQueuedUserMessage(item.ID) {
			return errors.New("discard retained runtime steering")
		}
		return nil
	}); err != nil {
		t.Fatalf("retained ownerless runtime rejected steering: %v", err)
	}

	if err := retention.Close(); err != nil {
		t.Fatalf("release execution resource retention: %v", err)
	}
	assertRuntimeUnavailable(t, authority, resource, "execution retention released")
}

func TestExecutionRetirementDrainsAcceptedQueuedWorkBeforeClosing(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	releaseModel := make(chan struct{})
	close(releaseModel)
	client := &ownerlessRetirementLLMClient{
		firstStarted: make(chan struct{}),
		releaseFirst: releaseModel,
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
	executionStarted := make(chan struct{})
	finishExecution := make(chan struct{})
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   OpenAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			close(executionStarted)
			<-finishExecution
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start ownerless execution: %v", err)
	}
	resource, hasResource := handle.Scope().Resource()
	if !hasResource {
		t.Fatal("ownerless agent execution has no resource")
	}
	<-executionStarted
	if err := authority.WithRuntime(context.Background(), resource, func(_ context.Context, engine *runtime.Engine) error {
		item := engine.QueueUserMessage("accepted before execution exit")
		if item.ID == "" {
			return errors.New("queued user message has no id")
		}
		return nil
	}); err != nil {
		t.Fatalf("queue accepted user work: %v", err)
	}

	close(finishExecution)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait execution retirement: %v", err)
	}
	if calls := client.callCount(); calls != 1 {
		t.Fatalf("model calls = %d, want accepted queue drained before retirement", calls)
	}
	assertRuntimeUnavailable(t, authority, resource, "accepted queue drained")

	statusMu.Lock()
	defer statusMu.Unlock()
	if len(statuses) != 2 ||
		statuses[0].Status != runtime.QueuedUserMessageAccepted ||
		statuses[1].Status != runtime.QueuedUserMessageSubmitted {
		t.Fatalf("queued message statuses = %+v, want accepted then submitted", statuses)
	}
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

	workflowRef := workflowExecutionRefForTest(t, workflow.TaskID(uuid.NewString()), workflow.NodeID(uuid.NewString()), nil)
	agent, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, _ ExecutionScope, _ AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	targets, err := authority.CurrentScopedTaskExecutionSnapshot(workflowRef.ProjectID, workflowRef.WorkflowID, workflowRef.CurrentNode.TaskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(targets.Executions) != 1 ||
		targets.Executions[0].Ref != workflowRef ||
		targets.Executions[0].Agent == nil ||
		targets.Executions[0].Agent.SessionID != sessionID ||
		targets.Executions[0].Script != nil {
		t.Fatalf("agent targets = %+v", targets)
	}

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	script, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(t, authority, workflowRef),
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

func TestAuthorityCurrentTaskExecutionTargetsPreservesParallelScriptRuns(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	taskID := workflow.TaskID("task-a")
	cancellationGrace := 50 * time.Millisecond
	handles := make([]ExecutionHandle, 0, 2)
	for _, nodeID := range []workflow.NodeID{"node-a", "node-b"} {
		handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
			Workflow: releasedWorkflowLeaseForTest(t, authority, workflowExecutionRefForTest(t, taskID, nodeID, nil)),
			Command: ScriptCommand{
				Path:              sleepPath,
				Args:              []string{"30"},
				CancellationGrace: &cancellationGrace,
			},
		})
		if err != nil {
			t.Fatalf("start script %s: %v", nodeID, err)
		}
		handles = append(handles, handle)
	}

	targets, err := authority.CurrentScopedTaskExecutionSnapshot("project-test", "workflow-test", taskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(targets.Executions) != 2 {
		t.Fatalf("targets = %+v", targets)
	}
	for index, nodeID := range []workflow.NodeID{"node-a", "node-b"} {
		if targets.Executions[index].Ref.CurrentNode.NodeID != nodeID ||
			targets.Executions[index].Agent != nil ||
			targets.Executions[index].Script == nil ||
			targets.Executions[index].Script.Path != sleepPath {
			t.Fatalf("executions = %+v", targets.Executions)
		}
	}

	for _, handle := range handles {
		if err := handle.Stop(context.Background()); err != nil {
			t.Fatalf("stop script: %v", err)
		}
	}
}

func TestScopedTaskExecutionSnapshotsExcludeUnrelatedScopesAndRemainImmutable(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	grace := 50 * time.Millisecond
	start := func(projectID string, workflowID workflow.WorkflowID, taskID workflow.TaskID) ExecutionHandle {
		t.Helper()
		ref := workflowExecutionRefForTest(t, taskID, workflow.NodeID(uuid.NewString()), nil)
		ref.ProjectID, ref.WorkflowID = projectID, workflowID
		handle, startErr := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
			Workflow: releasedWorkflowLeaseForTest(t, authority, ref),
			Command:  ScriptCommand{Path: sleepPath, Args: []string{"30"}, CancellationGrace: &grace},
		})
		if startErr != nil {
			t.Fatalf("start %s/%s/%s: %v", projectID, workflowID, taskID, startErr)
		}
		return handle
	}
	selected := start("project-a", "workflow-a", "task-a")
	unrelatedWorkflow := start("project-a", "workflow-b", "task-b")
	unrelatedProject := start("project-b", "workflow-a", "task-c")
	t.Cleanup(func() {
		for _, handle := range []ExecutionHandle{selected, unrelatedWorkflow, unrelatedProject} {
			_ = handle.Stop(context.Background())
		}
	})

	snapshot, err := authority.CurrentScopedTaskExecutionSnapshot("project-a", "workflow-a", "task-a")
	if err != nil {
		t.Fatalf("scoped snapshot: %v", err)
	}
	if len(snapshot.Executions) != 1 || snapshot.Executions[0].Ref.CurrentNode.TaskID != "task-a" {
		t.Fatalf("scoped snapshot included unrelated execution: %+v", snapshot)
	}
	snapshot.Executions[0].Script.Path = "mutated"
	again, err := authority.CurrentScopedTaskExecutionSnapshot("project-a", "workflow-a", "task-a")
	if err != nil {
		t.Fatalf("repeat scoped snapshot: %v", err)
	}
	if len(again.Executions) != 1 || again.Executions[0].Script.Path != sleepPath {
		t.Fatalf("snapshot mutation leaked into authority state: %+v", again)
	}
}

func TestScriptExecutionRetiresBeforeCompletionFinalizer(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	taskID := workflow.TaskID("task-finalizing-script")
	finalizeStarted := make(chan struct{})
	releaseFinalize := make(chan struct{})
	handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(t, authority, workflowExecutionRefForTest(t, taskID, "node-finalizing-script", nil)),
		Command:  ScriptCommand{Path: truePath},
		Finalize: func(context.Context, ExecutionScope, ScriptResult, error) error {
			close(finalizeStarted)
			<-releaseFinalize
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releaseFinalize:
		default:
			close(releaseFinalize)
		}
		_ = handle.Close(context.Background())
	})
	<-finalizeStarted

	targets, err := authority.CurrentScopedTaskExecutionSnapshot("project-test", "workflow-test", taskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(targets.Executions) != 0 {
		t.Fatalf("finalizing script remains interruptible: %+v", targets)
	}
	selectionCalled := false
	selectionErr := authority.WithWorkflowInterruptSelection(taskID, nil, func(WorkflowInterruptSelection) error {
		selectionCalled = true
		return nil
	})
	if !errors.Is(selectionErr, ErrExecutionNoLongerLive) {
		t.Fatalf("finalizing script selection error = %v, want %v", selectionErr, ErrExecutionNoLongerLive)
	}
	if selectionCalled {
		t.Fatal("finalizing script alone authorized task interrupt")
	}

	close(releaseFinalize)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestTaskInterruptSelectionIncludesFinalizingScriptAlongsideRunningTaskScope(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	taskID := workflow.TaskID("task-finalizing-selection")
	finalizeStarted := make(chan struct{})
	releaseFinalize := make(chan struct{})
	finalizing, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(t, authority, workflowExecutionRefForTest(t, taskID, "node-finalizing", nil)),
		Command:  ScriptCommand{Path: truePath},
		Finalize: func(context.Context, ExecutionScope, ScriptResult, error) error {
			close(finalizeStarted)
			<-releaseFinalize
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start finalizing script: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releaseFinalize:
		default:
			close(releaseFinalize)
		}
		_ = finalizing.Close(context.Background())
	})
	<-finalizeStarted

	grace := 50 * time.Millisecond
	running, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(t, authority, workflowExecutionRefForTest(t, taskID, "node-running", nil)),
		Command: ScriptCommand{
			Path:              sleepPath,
			Args:              []string{"30"},
			CancellationGrace: &grace,
		},
	})
	if err != nil {
		t.Fatalf("start running script: %v", err)
	}
	t.Cleanup(func() {
		_ = running.Stop(context.Background())
	})

	deadline := time.After(3 * time.Second)
	for {
		var selection WorkflowInterruptSelection
		selectionErr := authority.WithWorkflowInterruptSelection(taskID, nil, func(got WorkflowInterruptSelection) error {
			selection = got
			return nil
		})
		if selectionErr == nil &&
			len(selection.Interruptible) == 1 &&
			selection.Interruptible[0].Scope().ID() == running.Scope().ID() &&
			len(selection.Queued) == 0 &&
			len(selection.Finalizing) == 1 &&
			selection.Finalizing[0].Scope().ID() == finalizing.Scope().ID() {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("task interrupt selection = %+v, error = %v", selection, selectionErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestScriptStartupFailureLeavesNoWorkflowRunningOrInterruptibleState(t *testing.T) {
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	taskID := workflow.TaskID("task-script-startup-failure")
	finalizeStarted := make(chan struct{})
	releaseFinalize := make(chan struct{})
	handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(t, authority, workflowExecutionRefForTest(t, taskID, "node-startup-failure", nil)),
		Command:  ScriptCommand{Path: filepath.Join(t.TempDir(), "missing-script")},
		Finalize: func(_ context.Context, _ ExecutionScope, _ ScriptResult, startErr error) error {
			if startErr == nil {
				t.Error("startup finalizer error = nil, want command start error")
			}
			close(finalizeStarted)
			<-releaseFinalize
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releaseFinalize:
		default:
			close(releaseFinalize)
		}
		_ = handle.Close(context.Background())
	})
	<-finalizeStarted

	targets, err := authority.CurrentScopedTaskExecutionSnapshot("project-test", "workflow-test", taskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(targets.Executions) != 0 {
		t.Fatalf("startup failure published workflow execution: %+v", targets)
	}
	selectionCalled := false
	selectionErr := authority.WithWorkflowInterruptSelection(taskID, nil, func(WorkflowInterruptSelection) error {
		selectionCalled = true
		return nil
	})
	if !errors.Is(selectionErr, ErrExecutionNoLongerLive) {
		t.Fatalf("startup failure selection error = %v, want %v", selectionErr, ErrExecutionNoLongerLive)
	}
	if selectionCalled {
		t.Fatal("startup failure authorized task interrupt")
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

func TestResourceReplacementWaitsForRetainedGenerationToDrain(t *testing.T) {
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
	type replacementResult struct {
		handle ExecutionHandle
		err    error
	}
	replaced := make(chan replacementResult, 1)
	go func() {
		handle, replaceErr := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
			Descriptor: mustOpenSessionDescriptor(t, sessionID),
			Runtime:    &plan,
			Resource:   ReplaceAgentResource{},
			Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
		})
		replaced <- replacementResult{handle: handle, err: replaceErr}
	}()
	select {
	case outcome := <-replaced:
		t.Fatalf("replacement returned before retained generation drained: %v", outcome.err)
	case <-lifecycle.draining:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not begin retained generation drain")
	}
	if err := retention.Close(); err != nil {
		t.Fatalf("release resource retention: %v", err)
	}
	if err := retention.Close(); err != nil {
		t.Fatalf("release resource retention again: %v", err)
	}
	outcome := <-replaced
	if outcome.err != nil {
		t.Fatalf("replace after retained generation drain: %v", outcome.err)
	}
	if _, err := outcome.handle.Wait(context.Background()); err != nil {
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

func TestAuthorityWithDormantSessionStoreAdmitsExactlyOnePath(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
	})
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}

	callbackCalled := false
	admission, err := authority.WithDormantSessionStore(
		context.Background(),
		descriptor,
		func(context.Context, *session.Store) error {
			callbackCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("admit dormant Store callback: %v", err)
	}
	if admission.RuntimeAvailable || !callbackCalled {
		t.Fatalf("dormant admission = %+v callback=%t, want callback-only path", admission, callbackCalled)
	}

	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	defer func() {
		if _, releaseErr := attachment.Release(context.Background(), RuntimeReleaseClose); releaseErr != nil {
			t.Errorf("release runtime: %v", releaseErr)
		}
	}()
	callbackCalled = false
	admission, err = authority.WithDormantSessionStore(
		context.Background(),
		descriptor,
		func(context.Context, *session.Store) error {
			callbackCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("admit live resource: %v", err)
	}
	if !admission.RuntimeAvailable || callbackCalled {
		t.Fatalf("live admission = %+v callback=%t, want runtime-only path", admission, callbackCalled)
	}
}

func TestAuthorityWithDormantSessionStoreRejectsBlockedAndClosedAdmission(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}

	t.Run("blocked", func(t *testing.T) {
		authority := NewAuthority(AuthorityOptions{
			PersistenceRoot: fixture.config.PersistenceRoot,
			StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		})
		t.Cleanup(func() {
			if closeErr := authority.Close(context.Background()); closeErr != nil {
				t.Errorf("close authority: %v", closeErr)
			}
		})
		release, blockErr := authority.BlockSessionStarts(
			context.Background(),
			[]runtimeids.SessionID{sessionID},
			SessionStartBlockMaintenance,
		)
		if blockErr != nil {
			t.Fatalf("block session starts: %v", blockErr)
		}
		t.Cleanup(func() {
			if releaseErr := release.Close(context.Background()); releaseErr != nil {
				t.Errorf("release session-start block: %v", releaseErr)
			}
		})

		callbackCalled := false
		_, admissionErr := authority.WithDormantSessionStore(
			context.Background(),
			descriptor,
			func(context.Context, *session.Store) error {
				callbackCalled = true
				return nil
			},
		)
		if !errors.Is(admissionErr, ErrSessionStartsBlocked) {
			t.Fatalf("blocked dormant admission error = %v, want ErrSessionStartsBlocked", admissionErr)
		}
		if callbackCalled {
			t.Fatal("blocked dormant admission invoked the Store callback")
		}
	})

	t.Run("closed", func(t *testing.T) {
		authority := NewAuthority(AuthorityOptions{
			PersistenceRoot: fixture.config.PersistenceRoot,
			StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		})
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Fatalf("close authority: %v", closeErr)
		}

		callbackCalled := false
		_, admissionErr := authority.WithDormantSessionStore(
			context.Background(),
			descriptor,
			func(context.Context, *session.Store) error {
				callbackCalled = true
				return nil
			},
		)
		if !errors.Is(admissionErr, ErrAuthorityClosed) {
			t.Fatalf("closed dormant admission error = %v, want ErrAuthorityClosed", admissionErr)
		}
		if callbackCalled {
			t.Fatal("closed dormant admission invoked the Store callback")
		}
	})
}

func TestAuthorityWithDormantSessionStoreSelectsLiveForEveryRegisteredResourceState(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	feed := make(authorityPromptFeed, 1)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		PromptFeed:      feed,
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
	})
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}

	tests := []struct {
		name     string
		resource *agentResource
	}{
		{name: "building", resource: &agentResource{state: AgentResourceBuilding}},
		{name: "ownerless", resource: &agentResource{state: AgentResourceReady, owners: map[string]struct{}{}}},
		{name: "ready", resource: &agentResource{state: AgentResourceReady, owners: map[string]struct{}{"owner-a": {}}}},
		{name: "active", resource: &agentResource{state: AgentResourceReady, current: &execution{}}},
		{name: "draining", resource: &agentResource{state: AgentResourceDraining}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority.mu.Lock()
			authority.resources[sessionID] = test.resource
			authority.mu.Unlock()
			defer func() {
				authority.mu.Lock()
				delete(authority.resources, sessionID)
				authority.mu.Unlock()
			}()

			callbackCalled := false
			admission, admissionErr := authority.WithDormantSessionStore(
				context.Background(),
				descriptor,
				func(context.Context, *session.Store) error {
					callbackCalled = true
					return nil
				},
			)
			if admissionErr != nil {
				t.Fatalf("admit %s resource: %v", test.name, admissionErr)
			}
			if !admission.RuntimeAvailable || callbackCalled {
				t.Fatalf("%s admission = %+v callback=%t, want live path only", test.name, admission, callbackCalled)
			}
		})
	}
}

func TestAuthorityWithDormantSessionStoreBlocksRuntimeRegistrationUntilCallbackReturns(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
	})
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	dormantDone := make(chan error, 1)
	go func() {
		_, callbackErr := authority.WithDormantSessionStore(
			context.Background(),
			descriptor,
			func(context.Context, *session.Store) error {
				close(entered)
				<-release
				return nil
			},
		)
		dormantDone <- callbackErr
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("dormant Store callback did not start")
	}

	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	type openResult struct {
		attachment RuntimeAttachment
		err        error
	}
	openDone := make(chan openResult, 1)
	go func() {
		attachment, openErr := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
			SessionID: sessionID,
			OwnerID:   "owner-a",
			Runtime:   &plan,
		})
		openDone <- openResult{attachment: attachment, err: openErr}
	}()
	select {
	case result := <-openDone:
		t.Fatalf("runtime opened while dormant callback held admission gate: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if callbackErr := <-dormantDone; callbackErr != nil {
		t.Fatalf("dormant Store callback: %v", callbackErr)
	}
	result := <-openDone
	if result.err != nil {
		t.Fatalf("open runtime after dormant callback: %v", result.err)
	}
	if _, releaseErr := result.attachment.Release(context.Background(), RuntimeReleaseClose); releaseErr != nil {
		t.Fatalf("release opened runtime: %v", releaseErr)
	}
}

func TestWithSessionStoreSkipsCanceledWaiterAfterAdmission(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
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
	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first Store callback did not enter")
	}

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	secondCalled := make(chan struct{}, 1)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- authority.WithSessionStore(waiterCtx, descriptor, func(context.Context, *session.Store) error {
			secondCalled <- struct{}{}
			return nil
		})
	}()
	cancelWaiter()
	close(releaseFirst)

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Store callback: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first Store callback did not complete")
	}
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Store waiter error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled Store waiter did not complete")
	}
	select {
	case <-secondCalled:
		t.Fatal("canceled Store waiter invoked its callback")
	default:
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
	workflowRef := workflowExecutionRefForTest(t, "task-pending-question", "node-pending-question", nil)
	promptStart := make(chan struct{})
	runnerStarted := make(chan struct{})
	runnerRelease := make(chan struct{})
	runnerReleased := false
	t.Cleanup(func() {
		if !runnerReleased {
			close(runnerRelease)
		}
	})
	responseDone := make(chan executionPromptResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			close(runnerStarted)
			select {
			case <-promptStart:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
			response, askErr := authority.AwaitPromptResponse(ctx, scope.ID(), request)
			responseDone <- executionPromptResult{response: response, err: askErr}
			select {
			case <-runnerRelease:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
			return askErr
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}

	resource, _ := handle.Scope().Resource()
	<-runnerStarted
	_, revisionBeforePendingPrompt, err := authority.CurrentWorkflowTaskExecutionSnapshotsWithRevision()
	if err != nil {
		t.Fatalf("snapshot before pending prompt: %v", err)
	}
	close(promptStart)
	pending := <-feed
	if pending != (authorityPromptEvent{resource: resource, scopeID: handle.Scope().ID(), requestID: askID}) {
		t.Fatalf("pending prompt = %+v, want exact resource %v scope %s ask %s", pending, resource, handle.Scope().ID(), askID)
	}
	_, revisionWithPendingPrompt, err := authority.CurrentWorkflowTaskExecutionSnapshotsWithRevision()
	if err != nil {
		t.Fatalf("snapshot with pending prompt: %v", err)
	}
	if revisionWithPendingPrompt <= revisionBeforePendingPrompt {
		t.Fatalf(
			"pending prompt revision = %d, want greater than pre-prompt revision %d",
			revisionWithPendingPrompt,
			revisionBeforePendingPrompt,
		)
	}
	snapshot, err := authority.CurrentScopedTaskExecutionSnapshot(workflowRef.ProjectID, workflowRef.WorkflowID, workflowRef.CurrentNode.TaskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(snapshot.Executions) != 1 ||
		snapshot.Executions[0].Ref != workflowRef ||
		snapshot.Executions[0].Agent == nil ||
		snapshot.Executions[0].Agent.SessionID != sessionID ||
		snapshot.Executions[0].Script != nil ||
		!snapshot.Executions[0].WaitingQuestion {
		t.Fatalf("pending question snapshot = %+v", snapshot)
	}

	want := tools.AskQuestionResponse{RequestID: askID, Answer: "yes"}
	if err := authority.SubmitPromptResponse(sessionID, want, nil); err != nil {
		t.Fatalf("submit prompt response: %v", err)
	}
	resolved := <-feed
	if resolved != (authorityPromptEvent{resource: resource, scopeID: handle.Scope().ID(), requestID: askID, resolved: true}) {
		t.Fatalf("resolved prompt = %+v, want exact resource %v scope %s ask %s", resolved, resource, handle.Scope().ID(), askID)
	}
	_, revisionAfterPromptResolution, err := authority.CurrentWorkflowTaskExecutionSnapshotsWithRevision()
	if err != nil {
		t.Fatalf("snapshot after prompt resolution: %v", err)
	}
	if revisionAfterPromptResolution <= revisionWithPendingPrompt {
		t.Fatalf(
			"resolved prompt revision = %d, want greater than pending revision %d",
			revisionAfterPromptResolution,
			revisionWithPendingPrompt,
		)
	}
	if result := <-responseDone; result.err != nil || result.response != want {
		t.Fatalf("prompt response = %+v error = %v, want %+v", result.response, result.err, want)
	}
	close(runnerRelease)
	runnerReleased = true
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait agent execution: %v", err)
	}
}

func TestResolvePendingWorkflowPromptUsesExactTaskScope(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	feed := make(authorityPromptFeed, 1)
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
	request := tools.AskQuestionRequest{ID: askID, StepID: uuid.NewString(), Question: "Proceed?"}
	workflowRef := workflowExecutionRefForTest(t, "task-exact-prompt", "node-exact-prompt", nil)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	responseDone := make(chan executionPromptResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
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
	if pending := <-feed; pending.scopeID != handle.Scope().ID() || pending.requestID != askID {
		t.Fatalf("pending prompt = %+v, want scope %s ask %s", pending, handle.Scope().ID(), askID)
	}

	resolved, err := authority.ResolvePendingWorkflowPrompt(workflowRef.CurrentNode.TaskID, askID)
	if err != nil {
		t.Fatalf("ResolvePendingWorkflowPrompt: %v", err)
	}
	if resolved.ScopeID != handle.Scope().ID() || resolved.SessionID != sessionID || !resolved.CurrentNode.Equal(workflowRef.CurrentNode) {
		t.Fatalf("prompt resolution = %+v, want scope %s session %s node %v", resolved, handle.Scope().ID(), sessionID, workflowRef.CurrentNode)
	}
	response := tools.AskQuestionResponse{RequestID: askID, Answer: "yes"}
	if err := authority.SubmitPromptResponseForScope(resolved.ScopeID, response, nil); err != nil {
		t.Fatalf("SubmitPromptResponseForScope: %v", err)
	}
	if result := <-responseDone; result.err != nil || result.response != response {
		t.Fatalf("prompt response = %+v error = %v, want %+v", result.response, result.err, response)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait agent execution: %v", err)
	}
	if _, err := authority.ResolvePendingWorkflowPrompt(workflowRef.CurrentNode.TaskID, askID); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("retired prompt resolution error = %v, want prompt not found", err)
	}
}

func TestQuestionCompletionReplacesRetainedRuntimeAfterDrain(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	promptFeed := make(authorityPromptFeed, 1)
	lifecycle := &authorityAutoReleaseLifecycle{}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		PromptFeed:        promptFeed,
		ResourceLifecycle: lifecycle,
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	askID := uuid.NewString()
	request := tools.AskQuestionRequest{
		ID: askID, StepID: uuid.NewString(), Question: "Proceed?",
	}
	workflowRef := workflowExecutionRefForTest(t, "task-question-replacement", "node-question-replacement", nil)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			_, awaitErr := authority.AwaitPromptResponse(ctx, scope.ID(), request)
			return awaitErr
		},
	})
	if err != nil {
		t.Fatalf("start questioning execution: %v", err)
	}
	pending := <-promptFeed
	if pending.scopeID != handle.Scope().ID() || pending.requestID != askID {
		t.Fatalf("pending question = %+v", pending)
	}
	if err := authority.SubmitPromptResponse(sessionID, tools.AskQuestionResponse{
		RequestID: askID,
		Answer:    "yes",
	}, nil); err != nil {
		t.Fatalf("submit prompt response: %v", err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait questioning execution: %v", err)
	}

	successorRef := workflowExecutionRefForTest(t, workflowRef.CurrentNode.TaskID, "node-question-successor", nil)
	successor, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, successorRef),
		Resource:   ReplaceAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	})
	if err != nil {
		t.Fatalf("replace retained runtime after question completion: %v", err)
	}
	if _, err := successor.Wait(context.Background()); err != nil {
		t.Fatalf("wait successor execution: %v", err)
	}
	if err := authority.Close(context.Background()); err != nil {
		t.Fatalf("close authority: %v", err)
	}
}

func workflowExecutionRefForTest(
	t *testing.T,
	taskID workflow.TaskID,
	nodeID workflow.NodeID,
	branchKey *workflow.TransitionBranchKey,
) WorkflowExecutionRef {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, branchKey)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return WorkflowExecutionRef{ProjectID: "project-test", WorkflowID: "workflow-test", CurrentNode: reference}
}

func releasedWorkflowLeaseForTest(t *testing.T, authority *Authority, ref WorkflowExecutionRef) *WorkflowExecutionLease {
	t.Helper()
	lease, err := authority.NewWorkflowExecutionLease(ref)
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	return &lease
}

func workflowExecutionRefForTestPointer(
	t *testing.T,
	taskID workflow.TaskID,
	nodeID workflow.NodeID,
	branchKey *workflow.TransitionBranchKey,
) *WorkflowExecutionRef {
	t.Helper()
	ref := workflowExecutionRefForTest(t, taskID, nodeID, branchKey)
	return &ref
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
