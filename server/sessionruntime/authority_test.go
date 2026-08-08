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
	"core/server/metadata"
	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/server/workflowruntime"
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

type admissionObservationContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *admissionObservationContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.observed)
	})
	return c.Context.Done()
}

type ownerlessRetirementLLMClient struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func TestAuthorityCloseCancelsAndJoinsLifecycleTasks(t *testing.T) {
	authority := NewAuthority(AuthorityOptions{})
	started := make(chan struct{})
	stopped := make(chan struct{})
	if !authority.launchLifecycleTask(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	}) {
		t.Fatal("authority rejected lifecycle task before close")
	}
	<-started

	if err := authority.Close(context.Background()); err != nil {
		t.Fatalf("close authority: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Authority.Close returned before its lifecycle task stopped")
	}
	if authority.launchLifecycleTask(func(context.Context) {}) {
		t.Fatal("closed authority accepted another lifecycle task")
	}
}

func TestWithExactExecutionsDoesNotBlockTaskExecutionObservation(t *testing.T) {
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
	handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Command: ScriptCommand{Path: sleepPath, Args: []string{"30"}},
	})
	if err != nil {
		t.Fatalf("start script execution: %v", err)
	}
	t.Cleanup(func() {
		_ = handle.Stop(context.Background())
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- authority.WithExactExecutions([]ExecutionHandle{handle}, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("exact execution operation did not start")
	}

	observationDone := make(chan error, 1)
	go func() {
		observationDone <- authority.WithWorkflowTaskExecutionSnapshots(func(map[workflow.TaskID]TaskExecutionSnapshot) error {
			return nil
		})
	}()
	var observationBlocked bool
	select {
	case err := <-observationDone:
		if err != nil {
			t.Fatalf("observe workflow task executions: %v", err)
		}
	case <-time.After(time.Second):
		observationBlocked = true
	}

	close(release)
	if err := <-operationDone; err != nil {
		t.Fatalf("exact execution operation: %v", err)
	}
	if observationBlocked {
		select {
		case err := <-observationDone:
			if err != nil {
				t.Fatalf("observe workflow task executions after release: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("workflow task execution observation remained blocked after exact execution release")
		}
		t.Fatal("exact execution operation blocked workflow task execution observation")
	}
}

func TestAuthorityCloseCancelsLifecycleStartWaitingForSessionAdmission(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	descriptor := mustOpenSessionDescriptor(t, sessionID)

	admissionHeld := make(chan struct{})
	releaseAdmission := make(chan struct{})
	storeDone := make(chan error, 1)
	go func() {
		storeDone <- authority.WithSessionStore(
			context.Background(),
			descriptor,
			func(context.Context, *session.Store) error {
				close(admissionHeld)
				<-releaseAdmission
				return nil
			},
		)
	}()
	select {
	case <-admissionHeld:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Session admission holder")
	}

	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	admissionWaitObserved := make(chan struct{})
	startDone := make(chan error, 1)
	lifecycleFinished := make(chan struct{})
	if !authority.launchLifecycleTask(func(ctx context.Context) {
		defer close(lifecycleFinished)
		observedCtx := &admissionObservationContext{
			Context:  ctx,
			observed: admissionWaitObserved,
		}
		_, err := authority.StartAgentExecution(observedCtx, AgentExecutionRequest{
			Descriptor: descriptor,
			Runtime:    &plan,
			Resource:   OpenAgentResource{},
			Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
		})
		startDone <- err
	}) {
		close(releaseAdmission)
		t.Fatal("authority rejected lifecycle task before close")
	}
	select {
	case <-admissionWaitObserved:
	case <-time.After(3 * time.Second):
		close(releaseAdmission)
		<-storeDone
		_ = authority.Close(context.Background())
		t.Fatal("lifecycle start did not reach context-aware Session admission")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- authority.Close(context.Background())
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			close(releaseAdmission)
			t.Fatalf("close authority: %v", err)
		}
	case <-time.After(3 * time.Second):
		close(releaseAdmission)
		<-closeDone
		t.Fatal("Authority.Close did not cancel a lifecycle start waiting for Session admission")
	}

	select {
	case err := <-startDone:
		if !errors.Is(err, context.Canceled) {
			close(releaseAdmission)
			t.Fatalf("lifecycle start error = %v, want context canceled", err)
		}
	default:
		close(releaseAdmission)
		t.Fatal("Authority.Close returned before the blocked lifecycle start stopped")
	}
	select {
	case <-lifecycleFinished:
	default:
		close(releaseAdmission)
		t.Fatal("Authority.Close returned before its lifecycle task finished")
	}

	close(releaseAdmission)
	if err := <-storeDone; err != nil {
		t.Fatalf("Session admission holder: %v", err)
	}
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

func (f authorityPromptFeed) PromptPendingScope(scope ExecutionScope, req tools.AskQuestionRequest, _ time.Time) error {
	resource, _ := scope.Resource()
	f <- authorityPromptEvent{resource: resource, scopeID: scope.ID(), requestID: req.ID}
	return nil
}

func (f authorityPromptFeed) PromptResolvedScope(scope ExecutionScope, requestID string) error {
	resource, _ := scope.Resource()
	f <- authorityPromptEvent{resource: resource, scopeID: scope.ID(), requestID: requestID, resolved: true}
	return nil
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
		item, queueErr := engine.QueueUserMessage("steer retained runtime")
		if queueErr != nil {
			return queueErr
		}
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
		item, queueErr := engine.QueueUserMessage("accepted before execution exit")
		if queueErr != nil {
			return queueErr
		}
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

	targets, err := authority.CurrentScopedTaskExecutionSnapshot("project-test", authorityWorkflowID(t, "test"), taskID)
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
	start := func(projectID string, workflowID runtimeids.WorkflowID, taskID workflow.TaskID) ExecutionHandle {
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
	selected := start("project-a", authorityWorkflowID(t, "a"), "task-a")
	unrelatedWorkflow := start("project-a", authorityWorkflowID(t, "b"), "task-b")
	unrelatedProject := start("project-b", authorityWorkflowID(t, "a"), "task-c")
	t.Cleanup(func() {
		for _, handle := range []ExecutionHandle{selected, unrelatedWorkflow, unrelatedProject} {
			_ = handle.Stop(context.Background())
		}
	})

	snapshot, err := authority.CurrentScopedTaskExecutionSnapshot("project-a", authorityWorkflowID(t, "a"), "task-a")
	if err != nil {
		t.Fatalf("scoped snapshot: %v", err)
	}
	if len(snapshot.Executions) != 1 || snapshot.Executions[0].Ref.CurrentNode.TaskID != "task-a" {
		t.Fatalf("scoped snapshot included unrelated execution: %+v", snapshot)
	}
	snapshot.Executions[0].Script.Path = "mutated"
	again, err := authority.CurrentScopedTaskExecutionSnapshot("project-a", authorityWorkflowID(t, "a"), "task-a")
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

	targets, err := authority.CurrentScopedTaskExecutionSnapshot("project-test", authorityWorkflowID(t, "test"), taskID)
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

	targets, err := authority.CurrentScopedTaskExecutionSnapshot("project-test", authorityWorkflowID(t, "test"), taskID)
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

	var staleCallbackCalls int
	staleErr := authority.WithRuntime(context.Background(), first.Resource(), func(_ context.Context, engine *runtime.Engine) error {
		staleCallbackCalls++
		thinking, err := workflow.NewThinkingValue("max")
		if err != nil {
			return err
		}
		if err := engine.SetWorkflowThinkingValue(thinking); err != nil {
			return err
		}
		_, err = engine.SteerWorkflowAssignment(runtime.WorkflowAssignment{})
		return err
	})
	if staleErr == nil {
		t.Fatal("stale assignment callback unexpectedly succeeded")
	}
	if staleCallbackCalls != 0 {
		t.Fatalf("stale assignment callback calls = %d, want 0", staleCallbackCalls)
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

func TestResourceReplacementWaitsForCurrentExecutionToFinish(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	authority := fixture.authority
	currentStarted := make(chan struct{})
	releaseCurrent := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseCurrent)
		}
	}()
	current, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   OpenAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			close(currentStarted)
			<-releaseCurrent
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start current execution: %v", err)
	}
	<-currentStarted

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
		t.Fatalf("replacement returned before current execution finished: %v", outcome.err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseCurrent)
	released = true
	if _, err := current.Wait(context.Background()); err != nil {
		t.Fatalf("wait current execution: %v", err)
	}
	outcome := <-replaced
	if outcome.err != nil {
		t.Fatalf("replace after current execution finished: %v", outcome.err)
	}
	if _, err := outcome.handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait replacement execution: %v", err)
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
		Settings:          settings,
		EnabledTools:      []toolspec.ID{toolspec.ToolExecCommand},
		FilesystemContext: runtimeTestFilesystemContext(t, fixture.config.WorkspaceRoot),
		Client:            client,
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

func TestExecutionCleanupAlwaysReleasesWorkflowBinding(t *testing.T) {
	tests := []struct {
		name             string
		resourceMismatch bool
	}{
		{name: "missing resource"},
		{name: "resource mismatch", resourceMismatch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRuntimeFixture(t)
			sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
			if err != nil {
				t.Fatalf("parse session id: %v", err)
			}
			workflowRef := workflowExecutionRefForTest(t, "task-cleanup-binding", "node-cleanup-binding", nil)
			executionConfig := &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID: runtimeids.NewExecutionScopeID(),
				Instructions: workflowruntime.TaskInstructions{
					CurrentNode: workflowRef.CurrentNode,
				},
			}
			plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
			plan.options.CurrentNodeExecution = executionConfig
			attachment, err := fixture.authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
				SessionID: sessionID,
				OwnerID:   "workflow-cleanup-test",
				Runtime:   &plan,
			})
			if err != nil {
				t.Fatalf("open workflow runtime: %v", err)
			}
			t.Cleanup(func() {
				if _, releaseErr := attachment.Release(context.Background(), RuntimeReleaseClose); releaseErr != nil {
					t.Errorf("release workflow runtime: %v", releaseErr)
				}
			})

			var engine *runtime.Engine
			if err := fixture.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, current *runtime.Engine) error {
				engine = current
				return nil
			}); err != nil {
				t.Fatalf("resolve workflow runtime engine: %v", err)
			}
			binding, err := engine.BindCurrentNodeExecution(executionConfig)
			if err != nil {
				t.Fatalf("bind workflow execution: %v", err)
			}
			finalizing := &execution{
				scope: newAgentExecutionScope(
					executionConfig.ScopeID,
					ExecutionGeneration(1),
					attachment.Resource(),
					nil,
				),
				workflow: binding,
			}
			if test.resourceMismatch {
				finalizing.resource = &agentResource{
					ref:     attachment.Resource(),
					current: &execution{},
				}
			}
			cleanupErr := finalizing.cleanup()
			if test.resourceMismatch != (cleanupErr != nil) {
				t.Fatalf("cleanup error = %v, resource mismatch = %t", cleanupErr, test.resourceMismatch)
			}

			rebound, err := engine.BindCurrentNodeExecution(executionConfig)
			if err != nil {
				t.Fatalf("workflow execution binding remained owned after cleanup: %v", err)
			}
			if err := rebound.Close(); err != nil {
				t.Fatalf("close rebound workflow execution: %v", err)
			}
		})
	}
}

func TestBackgroundTerminalEventFromPredecessorGenerationRoutesToCurrentRuntime(t *testing.T) {
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
	route := func(event shelltool.Event, generation runtimeids.ResourceGeneration) {
		correlation, err := runtimeids.NewExecutionCorrelation(runtimeids.NewExecutionScopeID(), generation)
		if err != nil {
			t.Fatalf("new execution correlation: %v", err)
		}
		event.Snapshot.ExecutionCorrelation = &correlation
		authority.routeBackgroundEvent(event)
	}
	route(event, predecessor.Resource().Generation())
	select {
	case update := <-updates:
		if update.Type != runtime.BackgroundShellEventCompleted || update.ID != event.Snapshot.ID || update.ActivityID != event.Snapshot.ActivityID {
			t.Fatalf("predecessor terminal event update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("predecessor terminal event did not route to current runtime")
	}

	backgrounded := event
	backgrounded.Type = shelltool.EventBackgrounded
	route(backgrounded, predecessor.Resource().Generation())
	select {
	case update := <-updates:
		t.Fatalf("stale predecessor registration routed background update: %+v", update)
	default:
	}

	route(backgrounded, successor.Resource().Generation())
	select {
	case update := <-updates:
		if update.Type != runtime.BackgroundShellEventBackgrounded || update.ID != event.Snapshot.ID || update.ActivityID != event.Snapshot.ActivityID {
			t.Fatalf("current generation registration update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("current resource generation did not receive background registration")
	}
}

func TestBackgroundTerminalEventWaitsForNextRuntimeWhenSessionHasNoRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	updates := make(chan runtime.BackgroundShellEvent, 1)
	client := make(lifecycleRequestCaptureClient, 1)
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(50 * time.Millisecond))
	if err != nil {
		t.Fatalf("new background shell manager: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close background shell manager: %v", err)
		}
	})
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		Background:      manager,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, &client, func(event runtime.Event) {
		if event.Kind == runtime.EventBackgroundUpdated && event.Background != nil {
			updates <- *event.Background
		}
	})

	predecessor := openLifecycleRuntime(t, authority, sessionID, "predecessor", &plan)
	correlation, err := runtimeids.NewExecutionCorrelation(
		runtimeids.NewExecutionScopeID(),
		predecessor.Resource().Generation(),
	)
	if err != nil {
		t.Fatalf("new execution correlation: %v", err)
	}
	releasePath := filepath.Join(t.TempDir(), "release")
	started, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:              []string{"/bin/sh", "-c", fmt.Sprintf("while [ ! -f %q ]; do sleep 0.01; done; printf done", releasePath)},
		DisplayCommand:       "wait for release",
		OwnerSessionID:       sessionID.String(),
		ExecutionCorrelation: &correlation,
		Workdir:              t.TempDir(),
		YieldTime:            50 * time.Millisecond,
		MaxOutputChars:       16_000,
	})
	if err != nil {
		t.Fatalf("start background shell: %v", err)
	}
	if !started.Backgrounded {
		t.Fatalf("shell must transition to background, got %+v", started)
	}
	select {
	case update := <-updates:
		if update.Type != runtime.BackgroundShellEventBackgrounded || update.ID != started.SessionID {
			t.Fatalf("background registration update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("background registration did not reach predecessor runtime")
	}
	if _, err := predecessor.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release predecessor runtime: %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release background shell: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var terminal shelltool.Snapshot
	for {
		terminal, err = manager.Snapshot(started.SessionID)
		if err != nil {
			t.Fatalf("snapshot background shell: %v", err)
		}
		if !terminal.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for background shell completion")
		}
		time.Sleep(10 * time.Millisecond)
	}

	_ = openLifecycleRuntime(t, authority, sessionID, "successor", &plan)
	select {
	case update := <-updates:
		if update.Type != runtime.BackgroundShellEventCompleted ||
			update.ID != terminal.ID ||
			update.ActivityID != terminal.ActivityID {
			t.Fatalf("retried terminal event update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("undelivered terminal event did not reach the next runtime")
	}

	request := client.await(t)
	foundNotice := false
	for _, message := range llm.MessagesFromItems(request.Items) {
		if message.Role == llm.RoleDeveloper &&
			message.MessageType != nil &&
			*message.MessageType == llm.MessageTypeBackgroundNotice &&
			message.BackgroundActivityID != nil &&
			*message.BackgroundActivityID == terminal.ActivityID.String() {
			foundNotice = true
			break
		}
	}
	if !foundNotice {
		t.Fatal("retried terminal event did not steer a developer background notice")
	}
}

func TestOwnerlessBackgroundContinuationPublishesQuestionFromExactExecution(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	feed := make(authorityPromptFeed, 2)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		PromptFeed:      feed,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("I need a decision."),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-background-question",
				Name:  string(toolspec.ToolAskQuestion),
				Input: json.RawMessage(`{"question":"Proceed?"}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	settings := fixture.config.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "off"
	plan, err := NewAgentRuntimePlan(AgentRuntimePlanOptions{
		Settings:          settings,
		EnabledTools:      []toolspec.ID{toolspec.ToolAskQuestion},
		FilesystemContext: runtimeTestFilesystemContext(t, fixture.config.WorkspaceRoot),
		Client:            client,
	})
	if err != nil {
		t.Fatalf("new runtime plan: %v", err)
	}
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner", &plan)

	event := runtimewirefixture.BackgroundCompletionEvent("1000", sessionID.String(), t.TempDir())
	correlation, err := runtimeids.NewExecutionCorrelation(
		runtimeids.NewExecutionScopeID(),
		attachment.Resource().Generation(),
	)
	if err != nil {
		t.Fatalf("new execution correlation: %v", err)
	}
	event.Snapshot.ExecutionCorrelation = &correlation
	if !authority.routeBackgroundEvent(event) {
		t.Fatal("terminal background event was not delivered")
	}

	var pending authorityPromptEvent
	select {
	case pending = <-feed:
	case <-time.After(5 * time.Second):
		t.Fatal("background continuation question was not published from an Exact Execution Scope")
	}
	if pending.resource != attachment.Resource() || pending.scopeID.IsZero() || pending.requestID == "" {
		t.Fatalf("pending background question = %+v", pending)
	}
	if err := authority.SubmitPromptResolution(
		sessionID,
		pending.requestID,
		testQuestionResolution("yes"),
		nil,
	); err != nil {
		t.Fatalf("submit background question response: %v", err)
	}
	select {
	case resolved := <-feed:
		if !resolved.resolved ||
			resolved.resource != pending.resource ||
			resolved.scopeID != pending.scopeID ||
			resolved.requestID != pending.requestID {
			t.Fatalf("resolved background question = %+v, want resolution for %+v", resolved, pending)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background continuation question was not resolved")
	}

	deadline := time.Now().Add(5 * time.Second)
	for authority.sessionExecution(sessionID) != nil {
		if time.Now().After(deadline) {
			t.Fatal("background continuation Exact Execution Scope did not retire")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCompletedWorkflowSessionDoesNotStartBackgroundContinuation(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	mode := sessioncontract.WorkflowCompletionModeTool
	if err := fixture.store.MarkModelDispatchLocked(session.LockedContract{
		Model:                  "gpt-5",
		Temperature:            1,
		ContextWindow:          200000,
		ContextPercent:         95,
		EnabledTools:           []string{string(toolspec.ToolAskQuestion)},
		HasEnabledTools:        true,
		WorkflowCompletionMode: &mode,
	}); err != nil {
		t.Fatalf("mark workflow Session contract locked: %v", err)
	}
	updates := make(chan runtime.BackgroundShellEvent, 1)
	client := make(lifecycleRequestCaptureClient, 1)
	plan := authorityTestRuntimePlan(t, fixture, &client, func(event runtime.Event) {
		if event.Kind == runtime.EventBackgroundUpdated && event.Background != nil {
			updates <- *event.Background
		}
	})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "owner", &plan)

	event := runtimewirefixture.BackgroundCompletionEvent("1000", sessionID.String(), t.TempDir())
	correlation, err := runtimeids.NewExecutionCorrelation(
		runtimeids.NewExecutionScopeID(),
		attachment.Resource().Generation(),
	)
	if err != nil {
		t.Fatalf("new execution correlation: %v", err)
	}
	event.Snapshot.ExecutionCorrelation = &correlation
	if !fixture.authority.routeBackgroundEvent(event) {
		t.Fatal("workflow terminal background event was not delivered")
	}

	select {
	case update := <-updates:
		if update.Type != runtime.BackgroundShellEventCompleted ||
			update.ID != event.Snapshot.ID ||
			update.ActivityID != event.Snapshot.ActivityID {
			t.Fatalf("workflow background completion update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("workflow background completion was not appended to the Session")
	}
	select {
	case request := <-client:
		t.Fatalf("completed Workflow Session started another model turn: %+v", request)
	case <-time.After(200 * time.Millisecond):
	}
	if execution := fixture.authority.sessionExecution(sessionID); execution != nil {
		t.Fatalf("completed Workflow Session started Exact Execution Scope %s", execution.scope.ID())
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
	responseDone := make(chan promptAwaitTestResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			resolution, askErr := authority.AwaitPromptResolution(ctx, scope.ID(), request)
			responseDone <- promptAwaitTestResult{resolution: resolution, err: askErr}
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
	snapshot, err := authority.CurrentScopedTaskExecutionSnapshot(workflowRef.ProjectID, workflowRef.WorkflowID, workflowRef.CurrentNode.TaskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(snapshot.Executions) != 1 ||
		snapshot.Executions[0].Ref != workflowRef ||
		snapshot.Executions[0].Agent == nil ||
		snapshot.Executions[0].Agent.SessionID != sessionID ||
		snapshot.Executions[0].Script != nil ||
		!snapshot.Executions[0].HasPendingPromptKind(PendingPromptKindQuestion) {
		t.Fatalf("pending question snapshot = %+v", snapshot)
	}

	if err := authority.SubmitPromptResolution(
		sessionID,
		askID,
		testQuestionResolution("yes"),
		nil,
	); err != nil {
		t.Fatalf("submit prompt response: %v", err)
	}
	resolved := <-feed
	if resolved != (authorityPromptEvent{resource: resource, scopeID: handle.Scope().ID(), requestID: askID, resolved: true}) {
		t.Fatalf("resolved prompt = %+v, want exact resource %v scope %s ask %s", resolved, resource, handle.Scope().ID(), askID)
	}
	if result := <-responseDone; result.err != nil {
		t.Fatalf("prompt resolution error = %v", result.err)
	} else {
		requireQuestionAnswer(t, result.resolution, "yes")
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait agent execution: %v", err)
	}
}

func TestPromptStoreMutationsDoNotRequireAuthorityLock(t *testing.T) {
	authority := NewAuthority(AuthorityOptions{})
	sessionID := runtimeids.NewSessionID()
	resource, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("new session resource ref: %v", err)
	}
	workflowRef := workflowExecutionRefForTest(t, "task-prompt-lock", "node-prompt-lock", nil)
	scope := newAgentExecutionScope(
		runtimeids.NewExecutionScopeID(),
		1,
		resource,
		&workflowRef,
	)
	feed := make(authorityPromptFeed, 2)
	store := newExecutionPromptStore(authority, scope, feed)
	request := tools.AskQuestionRequest{
		ID: uuid.NewString(), StepID: uuid.NewString(), Question: "Proceed?",
	}
	resolution := testQuestionResolution("yes")

	authority.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			authority.mu.Unlock()
		}
	}()

	awaitDone := make(chan promptAwaitTestResult, 1)
	go func() {
		answer, awaitErr := store.Await(context.Background(), request)
		awaitDone <- promptAwaitTestResult{resolution: answer, err: awaitErr}
	}()
	select {
	case pending := <-feed:
		if pending.requestID != request.ID || pending.resolved {
			t.Fatalf("pending prompt event = %+v, want pending request %q", pending, request.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt registration waited for the Authority lock")
	}

	submitDone := make(chan error, 1)
	go func() {
		submitDone <- store.Submit(request.ID, resolution, nil)
	}()
	select {
	case submitErr := <-submitDone:
		if submitErr != nil {
			t.Fatalf("submit prompt response: %v", submitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt response waited for the Authority lock")
	}
	select {
	case result := <-awaitDone:
		if result.err != nil {
			t.Fatalf("prompt result error = %v", result.err)
		}
		requireQuestionAnswer(t, result.resolution, "yes")
	case <-time.After(time.Second):
		t.Fatal("prompt cleanup waited for the Authority lock")
	}
	authority.mu.Unlock()
	unlocked = true
}

func TestCurrentTaskExecutionSnapshotExposesPendingPromptKinds(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
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

	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	workflowRef := workflowExecutionRefForTest(t, "task-pending-prompts", "node-pending-prompts", nil)
	requests := []tools.AskQuestionRequest{
		{ID: "question-z", StepID: uuid.NewString(), Question: "Question"},
		{
			ID:              "approval-a",
			StepID:          uuid.NewString(),
			Approval:        true,
			ApprovalOptions: []tools.AskQuestionApprovalOption{{Decision: tools.AskQuestionApprovalDecisionAllowOnce, Label: "Allow"}},
		},
	}
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			for _, request := range requests {
				request := request
				go func() {
					_, _ = authority.AwaitPromptResolution(ctx, scope.ID(), request)
				}()
			}
			<-ctx.Done()
			return context.Cause(ctx)
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	t.Cleanup(func() {
		_ = handle.Stop(context.Background())
	})
	for range requests {
		<-feed
	}

	snapshot, err := authority.CurrentScopedTaskExecutionSnapshot(workflowRef.ProjectID, workflowRef.WorkflowID, workflowRef.CurrentNode.TaskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(snapshot.Executions) != 1 {
		t.Fatalf("executions = %+v, want one execution", snapshot.Executions)
	}
	prompts := snapshot.Executions[0].PendingPrompts
	if len(prompts) != 2 {
		t.Fatalf("pending prompts = %+v, want two prompts", prompts)
	}
	want := []PendingPromptReference{
		{ID: "approval-a", Kind: PendingPromptKindSessionApproval},
		{ID: "question-z", Kind: PendingPromptKindQuestion},
	}
	for index, expected := range want {
		if prompts[index] != expected {
			t.Fatalf("pending prompt %d = %+v, want %+v", index, prompts[index], expected)
		}
	}
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop agent execution: %v", err)
	}
	afterRetirement, err := authority.CurrentScopedTaskExecutionSnapshot(workflowRef.ProjectID, workflowRef.WorkflowID, workflowRef.CurrentNode.TaskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot after retirement: %v", err)
	}
	if len(afterRetirement.Executions) != 0 {
		t.Fatalf("retired execution snapshot = %+v, want no executions", afterRetirement.Executions)
	}
}

func TestCurrentTaskExecutionSnapshotRejectsDuplicatePendingPromptIDs(t *testing.T) {
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

	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	request := tools.AskQuestionRequest{ID: "duplicate-prompt", StepID: uuid.NewString(), Question: "Question"}
	workflowRef := workflowExecutionRefForTest(t, "task-duplicate-prompt", "node-duplicate-prompt", nil)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			_, awaitErr := authority.AwaitPromptResolution(ctx, scope.ID(), request)
			return awaitErr
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	t.Cleanup(func() {
		_ = handle.Stop(context.Background())
	})
	<-feed
	if _, err := authority.AwaitPromptResolution(context.Background(), handle.Scope().ID(), request); err == nil {
		t.Fatal("duplicate pending prompt was accepted")
	}
	snapshot, err := authority.CurrentScopedTaskExecutionSnapshot(workflowRef.ProjectID, workflowRef.WorkflowID, workflowRef.CurrentNode.TaskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(snapshot.Executions) != 1 || len(snapshot.Executions[0].PendingPrompts) != 1 {
		t.Fatalf("snapshot after duplicate prompt = %+v", snapshot)
	}
}

func TestTaskExecutionRejectsPendingPromptsForQueuedAndScript(t *testing.T) {
	ref := workflowExecutionRefForTest(t, "task-invalid-prompt-state", "node-invalid-prompt-state", nil)
	pending := []PendingPromptReference{{ID: "question", Kind: PendingPromptKindQuestion}}
	for name, execution := range map[string]TaskExecution{
		"queued": {
			Ref:            ref,
			Agent:          &TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
			Queued:         true,
			PendingPrompts: pending,
		},
		"script": {
			Ref:            ref,
			Script:         &TaskScriptExecutionTarget{Path: "/bin/true"},
			PendingPrompts: pending,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := execution.validate(); err == nil {
				t.Fatalf("%s execution accepted pending prompts", name)
			}
		})
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
	responseDone := make(chan promptAwaitTestResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   releasedWorkflowLeaseForTest(t, authority, workflowRef),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			resolution, askErr := authority.AwaitPromptResolution(ctx, scope.ID(), request)
			responseDone <- promptAwaitTestResult{resolution: resolution, err: askErr}
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
	if err := authority.SubmitPromptResolutionForScope(
		resolved.ScopeID,
		askID,
		testQuestionResolution("yes"),
		nil,
	); err != nil {
		t.Fatalf("SubmitPromptResolutionForScope: %v", err)
	}
	if result := <-responseDone; result.err != nil {
		t.Fatalf("prompt resolution error = %v", result.err)
	} else {
		requireQuestionAnswer(t, result.resolution, "yes")
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
			_, awaitErr := authority.AwaitPromptResolution(ctx, scope.ID(), request)
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
	if err := authority.SubmitPromptResolution(
		sessionID,
		askID,
		testQuestionResolution("yes"),
		nil,
	); err != nil {
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

func authorityWorkflowID(t *testing.T, name string) runtimeids.WorkflowID {
	t.Helper()
	raw, found := map[string]string{
		"test": "550e8400-e29b-41d4-a716-446655440101",
		"a":    "550e8400-e29b-41d4-a716-446655440102",
		"b":    "550e8400-e29b-41d4-a716-446655440103",
	}[name]
	if !found {
		t.Fatalf("unknown authority Workflow fixture %q", name)
	}
	workflowID, err := runtimeids.ParseWorkflowID(raw)
	if err != nil {
		t.Fatalf("parse authority Workflow fixture %q: %v", raw, err)
	}
	return workflowID
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
	return WorkflowExecutionRef{ProjectID: "project-test", WorkflowID: authorityWorkflowID(t, "test"), CurrentNode: reference}
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
		Settings:          settings,
		FilesystemContext: runtimeTestFilesystemContext(t, fixture.config.WorkspaceRoot),
		Client:            client,
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

func runtimeTestFilesystemContext(t *testing.T, root string) tools.FilesystemContext {
	t.Helper()
	context, err := runtimewire.NewFilesystemContext(root, root, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	return context
}

func mustOpenSessionDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	return descriptor
}
