package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func lifecycleSessionID(t *testing.T, fixture sessionRuntimeFixture) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	return sessionID
}

func lifecycleWorktreeTarget(workspaceRoot, worktreeRoot string) clientui.SessionExecutionTarget {
	return clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceRoot:         workspaceRoot,
		WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		Worktree: &clientui.SessionExecutionWorktreeTarget{
			ID:           "worktree-1",
			Root:         worktreeRoot,
			Availability: string(clientui.ProjectAvailabilityAvailable),
		},
		CwdRelpath:       ".",
		EffectiveWorkdir: worktreeRoot,
	}
}

func lifecycleReminder(workspaceRoot, worktreeRoot string) *session.WorktreeReminderState {
	return &session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/lifecycle"),
			WorktreePath:  worktreeRoot,
			WorkspaceRoot: workspaceRoot,
			EffectiveCwd:  worktreeRoot,
		},
	}
}

func openLifecycleRuntime(t *testing.T, authority *Authority, sessionID runtimeids.SessionID, ownerID string, plan *AgentRuntimePlan) RuntimeAttachment {
	t.Helper()
	attachment, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   ownerID,
		Runtime:   plan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	return attachment
}

type lifecycleReminderQueueObserver struct {
	queue func()
	once  sync.Once
}

type lifecycleStepProbe struct {
	began chan runtime.StepLifecycleSnapshot
	ended chan runtime.StepLifecycleSnapshot
}

func (p *lifecycleStepProbe) StepBegan(
	_ context.Context,
	_ AgentResourceDescriptor,
	snapshot runtime.StepLifecycleSnapshot,
) error {
	p.began <- snapshot
	return nil
}

func (p *lifecycleStepProbe) StepEnded(
	_ context.Context,
	_ AgentResourceDescriptor,
	snapshot runtime.StepLifecycleSnapshot,
) error {
	p.ended <- snapshot
	return nil
}

func (o *lifecycleReminderQueueObserver) ObservePersistedStore(_ context.Context, snapshot session.PersistedStoreSnapshot) error {
	if snapshot.Meta.WorktreeReminder != nil {
		o.once.Do(o.queue)
	}
	return nil
}

type lifecycleRequestCaptureClient chan llm.Request

type lifecycleRuntimeAbort struct {
	committed bool
	cause     error
}

type lifecycleMalformedRuntimeAbort struct {
	cause error
}

type lifecycleBarrierFailureObserver struct {
	armed   atomic.Bool
	failure error
}

func (o *lifecycleBarrierFailureObserver) ObservePersistedStore(
	context.Context,
	session.PersistedStoreSnapshot,
) error {
	if o.armed.CompareAndSwap(true, false) {
		return o.failure
	}
	return nil
}

type lifecycleQuestionBarrierClient struct {
	calls atomic.Int32
}

func (c *lifecycleQuestionBarrierClient) Generate(
	context.Context,
	llm.Request,
) (llm.Response, error) {
	call := c.calls.Add(1)
	if call != 1 {
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("unexpected continuation"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	}
	question := llm.ToolCall{
		ID:    "lifecycle-question",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Continue?"}`),
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("working"),
			Phase:   textutil.Value(llm.MessagePhaseCommentary),
		},
		ToolCalls: []llm.ToolCall{question},
		OutputItems: []llm.ResponseItem{
			{
				Type: llm.ResponseItemTypeOther,
				ID:   textutil.Value("lifecycle-hosted"),
				Raw:  json.RawMessage(`{"type":"web_search_call","id":"lifecycle-hosted","status":"completed","action":{"type":"search","query":"kent"}}`),
			},
			{
				Type:   llm.ResponseItemTypeFunctionCall,
				ID:     textutil.Value(question.ID),
				CallID: textutil.Value(question.ID),
			},
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil
}

func (e *lifecycleRuntimeAbort) Error() string {
	return e.cause.Error()
}

func (e *lifecycleRuntimeAbort) Unwrap() error {
	return e.cause
}

func (e *lifecycleRuntimeAbort) RuntimeAbortDisposition() (bool, error) {
	return e.committed, e.cause
}

func (e *lifecycleMalformedRuntimeAbort) Error() string {
	return e.cause.Error()
}

func (e *lifecycleMalformedRuntimeAbort) RuntimeAbortDisposition() (bool, error) {
	return false, errors.New("unexposed abort cause")
}

func TestRuntimeAbortContractViolationReturnsDiagnostic(t *testing.T) {
	abort, err := runtimeAbortFromError(&lifecycleMalformedRuntimeAbort{
		cause: errors.New("runtime failed"),
	})
	if !abort || err == nil {
		t.Fatalf("malformed runtime abort = abort:%t error:%v, want true diagnostic", abort, err)
	}
}

func (c *lifecycleRequestCaptureClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	*c <- request
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c lifecycleRequestCaptureClient) await(t *testing.T) llm.Request {
	t.Helper()
	select {
	case request := <-c:
		return request
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued user work to reach the model")
		return llm.Request{}
	}
}

func TestRuntimeAbortRetiresCurrentOpenAndReplaceResourceGenerations(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection AgentResourceSelection
		prepare   bool
	}{
		{name: "current", selection: CurrentAgentResource{}, prepare: true},
		{name: "open", selection: OpenAgentResource{}},
		{name: "replace", selection: ReplaceAgentResource{}, prepare: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRuntimeFixture(t)
			sessionID := lifecycleSessionID(t, fixture)
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
			plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
			if test.prepare {
				openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
			}
			cause := errors.New("result group durability failure")
			abort := &lifecycleRuntimeAbort{committed: true, cause: cause}
			if _, current := test.selection.(CurrentAgentResource); current {
				authority.mu.Lock()
				failedResource := authority.resources[sessionID]
				authority.mu.Unlock()
				failedRef := failedResource.ref
				runErr := authority.RunCurrentAgentExecution(
					context.Background(),
					mustOpenSessionDescriptor(t, sessionID),
					func(context.Context, *runtime.Engine) error {
						return abort
					},
				)
				assertRuntimeAbortResourceRetired(
					t,
					authority,
					lifecycle,
					sessionID,
					failedResource,
					failedRef,
					runErr,
					abort,
					&plan,
				)
				return
			}
			handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
				Descriptor: mustOpenSessionDescriptor(t, sessionID),
				Runtime:    &plan,
				Resource:   test.selection,
				Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
					return abort
				},
			})
			if err != nil {
				t.Fatalf("start aborting execution: %v", err)
			}
			execution := handle.(executionHandle).execution
			failedResource := execution.resource
			failedRef := failedResource.ref
			_, waitErr := handle.Wait(context.Background())
			assertRuntimeAbortResourceRetired(
				t,
				authority,
				lifecycle,
				sessionID,
				failedResource,
				failedRef,
				waitErr,
				abort,
				&plan,
			)
		})
	}
}

func TestRuntimeAbortRetiresResourceWhileLifecycleRetentionIsPinned(t *testing.T) {
	for _, test := range []struct {
		name    string
		current bool
	}{
		{name: "current resource", current: true},
		{name: "opened execution resource"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRuntimeFixture(t)
			sessionID := lifecycleSessionID(t, fixture)
			lifecycle := &authorityAutoReleaseLifecycle{}
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
			plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
			cause := errors.New("retained runtime durability failure")
			abort := &lifecycleRuntimeAbort{committed: true, cause: cause}
			var failedResource *agentResource
			var runErr error
			if test.current {
				openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
				authority.mu.Lock()
				failedResource = authority.resources[sessionID]
				authority.mu.Unlock()
				runErr = authority.RunCurrentAgentExecution(
					context.Background(),
					mustOpenSessionDescriptor(t, sessionID),
					func(context.Context, *runtime.Engine) error {
						return abort
					},
				)
			} else {
				handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
					Descriptor: mustOpenSessionDescriptor(t, sessionID),
					Runtime:    &plan,
					Resource:   OpenAgentResource{},
					Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
						return abort
					},
				})
				if err != nil {
					t.Fatalf("start retained aborting execution: %v", err)
				}
				failedResource = handle.(executionHandle).execution.resource
				_, runErr = handle.Wait(context.Background())
			}
			failedRef := failedResource.ref
			if runErr != abort {
				t.Fatalf("retained runtime abort error = %v, want exact abort %p", runErr, abort)
			}
			if state := failedResource.descriptor().State; state != AgentResourceClosed {
				t.Fatalf("retained runtime abort state = %v, want closed", state)
			}
			authority.mu.Lock()
			admitted := authority.resources[sessionID]
			authority.mu.Unlock()
			if admitted == failedResource {
				t.Fatal("retained runtime abort resource remained admitted")
			}
			reopened := openLifecycleRuntime(t, authority, sessionID, "owner-b", &plan)
			if reopened.Resource() == failedRef {
				t.Fatal("retained runtime abort reused the failed resource generation")
			}
		})
	}
}

func TestQuestionBarrierDurabilityAbortClosesLifecycleAndRetiresCurrentGeneration(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	failure := errors.New("Question barrier observer failure")
	observer := &lifecycleBarrierFailureObserver{failure: failure}
	lifecycle := &authorityLifecycleProbe{draining: make(chan struct{}, 1)}
	storeOptions := append(
		[]session.StoreOption(nil),
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	storeOptions = append(storeOptions, session.WithPersistenceObserver(observer))
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      storeOptions,
		ResourceLifecycle: lifecycle,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	client := &lifecycleQuestionBarrierClient{}
	var askCalls atomic.Int32
	capabilities := llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsNativeWebSearch:       true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}
	plan := authorityTestRuntimePlan(t, fixture, client, func(event runtime.Event) {
		if event.Kind == runtime.EventToolCallStarted &&
			event.ToolCall != nil &&
			event.ToolCall.ID == "lifecycle-question" {
			observer.armed.Store(true)
		}
	})
	plan.options.EnabledTools = []toolspec.ID{
		toolspec.ToolAskQuestion,
		toolspec.ToolWebSearch,
	}
	plan.options.Settings.WebSearch = "native"
	plan.options.ProviderCapabilitiesOverride = &capabilities
	openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	authority.mu.Lock()
	failedResource := authority.resources[sessionID]
	authority.mu.Unlock()

	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Ask: func(
			context.Context,
			ExecutionScope,
			tools.AskQuestionRequest,
		) (tools.AskQuestionResponse, error) {
			askCalls.Add(1)
			return tools.AskQuestionResponse{}, errors.New("Question handler must remain blocked")
		},
		Runner: func(ctx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			return bridge.WithEngine(ctx, func(ctx context.Context, engine *runtime.Engine) error {
				_, err := engine.SubmitUserMessage(ctx, "ask after the hosted result")
				return err
			})
		},
	})
	if err != nil {
		t.Fatalf("start Question barrier execution: %v", err)
	}
	_, runErr := handle.Wait(context.Background())
	type runtimeAbort interface {
		RuntimeAbortDisposition() (committed bool, cause error)
	}
	var abort runtimeAbort
	if !errors.As(runErr, &abort) {
		t.Fatalf("Question barrier execution error = %v, want typed runtime abort", runErr)
	}
	committed, cause := abort.RuntimeAbortDisposition()
	if !committed || !errors.Is(cause, failure) {
		t.Fatalf(
			"Question barrier abort = committed:%t cause:%v, want true/%v",
			committed,
			cause,
			failure,
		)
	}
	if askCalls.Load() != 0 {
		t.Fatalf("Question handler calls = %d, want none", askCalls.Load())
	}
	if client.calls.Load() != 1 {
		t.Fatalf("model calls = %d, want one with no idle continuation", client.calls.Load())
	}
	if failedResource.store.Meta().PendingModelRecovery == nil {
		t.Fatal("Question barrier runtime abort cleared PendingModelRecovery")
	}
	if _, err := failedResource.engine.SubmitUserMessage(context.Background(), "later"); !errors.Is(err, runtime.ErrEngineClosed) {
		t.Fatalf("later failed-Engine submission error = %v, want ErrEngineClosed", err)
	}
	if state := failedResource.descriptor().State; state != AgentResourceClosed {
		t.Fatalf("Question barrier resource state = %v, want closed", state)
	}
	select {
	case <-lifecycle.draining:
	default:
		t.Fatal("Question barrier resource did not publish draining retirement")
	}
	authority.mu.Lock()
	admitted := authority.resources[sessionID]
	authority.mu.Unlock()
	if admitted == failedResource {
		t.Fatal("Question barrier failed resource generation remained admitted")
	}
}

func assertRuntimeAbortResourceRetired(
	t *testing.T,
	authority *Authority,
	lifecycle *authorityLifecycleProbe,
	sessionID runtimeids.SessionID,
	failedResource *agentResource,
	failedRef runtimeids.SessionResourceRef,
	runErr error,
	abort error,
	plan *AgentRuntimePlan,
) {
	t.Helper()
	if runErr != abort {
		t.Fatalf("execution error = %v, want exact runtime abort %p", runErr, abort)
	}
	if state := failedResource.descriptor().State; state != AgentResourceClosed {
		t.Fatalf("failed resource state = %v, want closed", state)
	}
	select {
	case <-lifecycle.draining:
	default:
		t.Fatal("failed resource did not publish Ready to Draining retirement")
	}
	authority.mu.Lock()
	admitted := authority.resources[sessionID]
	authority.mu.Unlock()
	if admitted == failedResource {
		t.Fatal("failed resource generation remained admitted")
	}
	reopened := openLifecycleRuntime(t, authority, sessionID, "owner-b", plan)
	if reopened.Resource() == failedRef {
		t.Fatal("later open reused the failed resource generation")
	}
}

func TestOwnerlessRuntimeStepPublishesResourceLifecycle(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := &ownerlessRetirementLLMClient{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	probe := &lifecycleStepProbe{
		began: make(chan runtime.StepLifecycleSnapshot, 1),
		ended: make(chan runtime.StepLifecycleSnapshot, 1),
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		StepLifecycle:   probe,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, client)
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)

	done := make(chan error, 1)
	go func() {
		done <- authority.WithRuntime(context.Background(), attachment.Resource(), func(ctx context.Context, engine *runtime.Engine) error {
			_, err := engine.SubmitUserMessage(ctx, "ownerless step")
			return err
		})
	}()
	select {
	case <-client.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ownerless runtime step")
	}
	select {
	case snapshot := <-probe.began:
		if snapshot.Transition != runtime.StepLifecycleTransitionBegan {
			t.Fatalf("began snapshot transition = %q", snapshot.Transition)
		}
	case <-time.After(time.Second):
		t.Fatal("ownerless runtime step did not publish began lifecycle")
	}

	close(client.releaseFirst)
	if err := <-done; err != nil {
		t.Fatalf("ownerless runtime step: %v", err)
	}
	select {
	case snapshot := <-probe.ended:
		if snapshot.Transition != runtime.StepLifecycleTransitionEnded {
			t.Fatalf("ended snapshot transition = %q", snapshot.Transition)
		}
	case <-time.After(time.Second):
		t.Fatal("ownerless runtime step did not publish ended lifecycle")
	}
}

func TestAuthoritySyncExecutionTargetPersistsReminderBeforeQueuedUserDrain(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := make(lifecycleRequestCaptureClient, 1)
	observer := &lifecycleReminderQueueObserver{}
	authority := newLifecycleAuthority(t, fixture, observer, nil)
	plan := authorityTestRuntimePlan(t, fixture, &client)
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	observer.queue = func() {
		if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
			engine.QueueUserMessageForAutoDrain("queued after switch", "request-after-switch")
			return nil
		}); err != nil {
			t.Errorf("queue user work during reminder persistence: %v", err)
		}
	}
	worktreeRoot := t.TempDir()

	if err := authority.SyncExecutionTarget(
		context.Background(),
		sessionID.String(),
		lifecycleWorktreeTarget(fixture.config.WorkspaceRoot, worktreeRoot),
		lifecycleReminder(fixture.config.WorkspaceRoot, worktreeRoot),
	); err != nil {
		t.Fatalf("sync execution target: %v", err)
	}

	request := client.await(t)
	for _, item := range request.Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.Role != nil && *item.Role == llm.RoleDeveloper &&
			item.MessageType != nil && *item.MessageType == llm.MessageTypeWorktreeMode &&
			item.WorktreeContext != nil &&
			item.WorktreeContext.EffectiveCwd == worktreeRoot {
			return
		}
	}
	t.Fatalf("queued model request omitted the persisted worktree reminder: %+v", request.Items)
}

type lifecyclePersistenceObserver struct {
	failuresRemaining atomic.Int32
}

func (o *lifecyclePersistenceObserver) ObservePersistedStore(context.Context, session.PersistedStoreSnapshot) error {
	if o.failuresRemaining.Load() > 0 {
		o.failuresRemaining.Add(-1)
		return errors.New("worktree reminder persistence failed")
	}
	return nil
}

type authorityStartBarrierLifecycle struct {
	*testsetup.StartBarrier
}

type startupRepairReadyProbe struct {
	callID   string
	ready    atomic.Int32
	observed atomic.Bool
}

func (p *startupRepairReadyProbe) ResourceReady(
	_ context.Context,
	_ AgentResourceDescriptor,
	engine *runtime.Engine,
	_ AgentResourceRetainer,
) error {
	if p.callID != "" {
		if err := engine.WithTranscriptHydrationSnapshot(func(snapshot runtime.TranscriptHydrationSnapshot) error {
			for _, live := range snapshot.InFlightTools {
				if live.ToolCallID == p.callID {
					return errors.New("ResourceReady observed a stale live tool start")
				}
			}
			for _, row := range snapshot.CommittedRows {
				if row.Tool != nil &&
					row.Tool.ToolCallID == p.callID &&
					row.Tool.IsError {
					p.observed.Store(true)
					return nil
				}
			}
			return errors.New("ResourceReady preceded the committed fresh-resource repair")
		}); err != nil {
			return err
		}
	}
	p.ready.Add(1)
	return nil
}

func (*startupRepairReadyProbe) ResourceDraining(
	context.Context,
	AgentResourceDescriptor,
) error {
	return nil
}

func (l *authorityStartBarrierLifecycle) ResourceReady(ctx context.Context, _ AgentResourceDescriptor, _ *runtime.Engine, _ AgentResourceRetainer) error {
	return l.ArriveAndWait(ctx)
}

func (l *authorityStartBarrierLifecycle) ResourceDraining(context.Context, AgentResourceDescriptor) error {
	return nil
}

func newLifecycleAuthority(t *testing.T, fixture sessionRuntimeFixture, observer session.PersistenceObserver, lifecycle AgentResourceLifecycle) *Authority {
	t.Helper()
	storeOptions := append(fixture.metadata.AuthoritativeSessionStoreOptions(), session.WithPersistenceObserver(observer))
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      storeOptions,
		ResourceLifecycle: lifecycle,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close lifecycle authority: %v", err)
		}
	})
	return authority
}

func TestFreshResourceRepairFailureDoesNotPublishResourceReady(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	eventLog, err := fixture.store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	if _, _, err := eventLog.AppendRecord(nil, session.MessageRecord{
		Role: session.MessageRoleAssistant,
		ToolCalls: []session.MessageToolCallRecord{{
			CallID: "unowned-dangling",
			Name:   string(toolspec.ToolAskQuestion),
			Kind:   session.ToolCallKindFunction,
			Input:  json.RawMessage(`{}`),
		}},
	}); err != nil {
		t.Fatalf("append unowned dangling call: %v", err)
	}

	ready := &startupRepairReadyProbe{}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: ready,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close recovery authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	if _, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
		Runtime:   &plan,
	}); err == nil {
		t.Fatal("fresh resource startup accepted a failed dangling-output repair")
	}
	if count := ready.ready.Load(); count != 0 {
		t.Fatalf("ResourceReady publications = %d, want zero after startup repair failure", count)
	}
	authority.mu.Lock()
	resource := authority.resources[sessionID]
	authority.mu.Unlock()
	if resource != nil {
		t.Fatalf("failed startup repair installed a resource: %+v", resource)
	}
}

func TestFreshResourceRepairCompletesBeforeResourceReady(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	eventLog, err := fixture.store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	const callID = "startup-repair"
	if _, _, err := eventLog.AppendRecord(textutil.Value("recovery-step"), session.MessageRecord{
		Role: session.MessageRoleAssistant,
		ToolCalls: []session.MessageToolCallRecord{{
			CallID: callID,
			Name:   string(toolspec.ToolAskQuestion),
			Kind:   session.ToolCallKindFunction,
			Input:  json.RawMessage(`{}`),
		}},
	}); err != nil {
		t.Fatalf("append dangling call: %v", err)
	}

	ready := &startupRepairReadyProbe{callID: callID}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: ready,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close recovery authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	if _, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open repaired runtime: %v", err)
	}
	if count := ready.ready.Load(); count != 1 {
		t.Fatalf("ResourceReady publications = %d, want one", count)
	}
	if !ready.observed.Load() {
		t.Fatal("ResourceReady did not observe the committed neutral repair")
	}
}

func TestAuthorityTryBlockSessionStartsRejectsInFlightStart(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	lifecycle := &authorityStartBarrierLifecycle{
		StartBarrier: testsetup.NewStartBarrier(),
	}
	authority := newLifecycleAuthority(t, fixture, &lifecyclePersistenceObserver{}, lifecycle)
	t.Cleanup(lifecycle.Unblock)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	started := testsetup.Start(func() (ExecutionHandle, error) {
		return authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
			Descriptor: mustOpenSessionDescriptor(t, sessionID),
			Runtime:    &plan,
			Resource:   OpenAgentResource{},
			Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
		})
	})
	select {
	case <-lifecycle.Entered():
	case result := <-started:
		t.Fatalf("agent start completed before entering resource lifecycle: handle=%v error=%v", result.Value, result.Err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for agent start to enter resource lifecycle")
	}

	_, err := authority.TryBlockSessionStarts(
		context.Background(),
		[]runtimeids.SessionID{sessionID},
		SessionStartBlockMaintenance,
	)
	if !errors.Is(err, ErrSessionStartAdmissionBusy) {
		t.Fatalf("try block session starts while admission is in flight = %v, want ErrSessionStartAdmissionBusy", err)
	}

	lifecycle.Unblock()
	result := <-started
	if result.Err != nil {
		t.Fatalf("start agent execution: %v", result.Err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := result.Value.Wait(waitCtx); err != nil {
		t.Fatalf("wait for agent execution: %v", err)
	}
}

func TestAuthorityTryBlockSessionStartsLeavesUncontendedBatchMemberUnblocked(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	busySessionID := lifecycleSessionID(t, fixture)
	uncontendedSessionID := runtimeids.NewSessionID()
	lifecycle := &authorityStartBarrierLifecycle{
		StartBarrier: testsetup.NewStartBarrier(),
	}
	authority := newLifecycleAuthority(t, fixture, &lifecyclePersistenceObserver{}, lifecycle)
	t.Cleanup(lifecycle.Unblock)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	started := testsetup.Start(func() (ExecutionHandle, error) {
		return authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
			Descriptor: mustOpenSessionDescriptor(t, busySessionID),
			Runtime:    &plan,
			Resource:   OpenAgentResource{},
			Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
		})
	})
	select {
	case <-lifecycle.Entered():
	case result := <-started:
		t.Fatalf("agent start completed before entering resource lifecycle: handle=%v error=%v", result.Value, result.Err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for agent start to enter resource lifecycle")
	}

	_, err := authority.TryBlockSessionStarts(
		context.Background(),
		[]runtimeids.SessionID{uncontendedSessionID, busySessionID},
		SessionStartBlockMaintenance,
	)
	if !errors.Is(err, ErrSessionStartAdmissionBusy) {
		t.Fatalf("try block batch with busy member = %v, want ErrSessionStartAdmissionBusy", err)
	}
	uncontendedRelease, err := authority.TryBlockSessionStarts(
		context.Background(),
		[]runtimeids.SessionID{uncontendedSessionID},
		SessionStartBlockMaintenance,
	)
	if err != nil {
		t.Fatalf("try block uncontended batch member after rejected batch: %v", err)
	}
	if err := uncontendedRelease.Close(context.Background()); err != nil {
		t.Fatalf("release uncontended session-start block: %v", err)
	}

	lifecycle.Unblock()
	result := <-started
	if result.Err != nil {
		t.Fatalf("start agent execution: %v", result.Err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := result.Value.Wait(waitCtx); err != nil {
		t.Fatalf("wait for agent execution: %v", err)
	}
}

func TestAuthoritySyncExecutionTargetRecoversOrRetiresAfterPersistenceFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		failures int32
		retired  bool
	}{
		{name: "reminder failure rolls back runtime", failures: 1},
		{name: "rollback failure retires exact resource", failures: 2, retired: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRuntimeFixture(t)
			sessionID := lifecycleSessionID(t, fixture)
			observer := &lifecyclePersistenceObserver{}
			lifecycle := &authorityLifecycleProbe{draining: make(chan struct{}, 1)}
			authority := newLifecycleAuthority(t, fixture, observer, lifecycle)
			plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
			attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
			var resource *agentResource
			releaseCallback := make(chan struct{})
			callbackDone := make(chan error, 1)
			if test.retired {
				authority.mu.Lock()
				resource = authority.resources[sessionID]
				authority.mu.Unlock()
				entered := make(chan struct{})
				go func() {
					callbackDone <- authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
						close(entered)
						<-releaseCallback
						return nil
					})
				}()
				<-entered
			}
			observer.failuresRemaining.Store(test.failures)
			targetWorkdir := t.TempDir()
			syncDone := make(chan error, 1)
			go func() {
				syncDone <- authority.SyncExecutionTarget(
					context.Background(),
					sessionID.String(),
					lifecycleWorktreeTarget(fixture.config.WorkspaceRoot, targetWorkdir),
					lifecycleReminder(fixture.config.WorkspaceRoot, targetWorkdir),
				)
			}()
			if test.retired {
				select {
				case <-lifecycle.draining:
				case <-time.After(3 * time.Second):
					t.Fatal("retirement did not begin draining")
				}
				if state := resource.descriptor().State; state != AgentResourceDraining {
					t.Fatalf("pinned retiring resource state = %v, want draining", state)
				}
				select {
				case err := <-syncDone:
					t.Fatalf("retirement completed before runtime callback release: %v", err)
				default:
				}
				close(releaseCallback)
				if callbackErr := <-callbackDone; callbackErr != nil {
					t.Fatalf("runtime callback: %v", callbackErr)
				}
			}
			err := <-syncDone
			if err == nil {
				t.Fatal("sync execution target succeeded despite persistence failure")
			}
			accessErr := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
				if engine.TranscriptWorkingDir() != fixture.config.WorkspaceRoot || engine.WorktreeReminderState() != nil {
					t.Fatalf("runtime target after rollback = workdir %q reminder %+v", engine.TranscriptWorkingDir(), engine.WorktreeReminderState())
				}
				return nil
			})
			if !test.retired {
				if accessErr != nil {
					t.Fatalf("inspect rolled-back runtime: %v", accessErr)
				}
				return
			}
			if !errors.Is(accessErr, serverapi.ErrRuntimeUnavailable) {
				t.Fatalf("failed resource lookup error = %v, want runtime unavailable", accessErr)
			}
			if state := resource.descriptor().State; state != AgentResourceClosed {
				t.Fatalf("retired resource state = %v, want closed", state)
			}
			replacement := openLifecycleRuntime(t, authority, sessionID, "owner-b", &plan)
			if replacement.Resource() == attachment.Resource() {
				t.Fatal("replacement reused the retired resource generation")
			}
		})
	}
}

func TestAuthorityBlocksSessionStartsDuringMaintenance(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	release, err := fixture.authority.BlockSessionStarts(
		context.Background(),
		[]runtimeids.SessionID{sessionID},
		SessionStartBlockMaintenance,
	)
	if err != nil {
		t.Fatalf("block session starts: %v", err)
	}
	t.Cleanup(func() {
		if err := release.Close(context.Background()); err != nil {
			t.Errorf("cleanup session-start block: %v", err)
		}
	})

	request := AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   OpenAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	}
	if _, err := fixture.authority.StartAgentExecution(context.Background(), request); !errors.Is(err, ErrSessionStartsBlocked) {
		t.Fatalf("start while blocked error = %v, want ErrSessionStartsBlocked", err)
	}
	if _, err := fixture.authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
		Runtime:   &plan,
	}); !errors.Is(err, ErrSessionStartsBlocked) {
		t.Fatalf("open runtime while blocked error = %v, want ErrSessionStartsBlocked", err)
	}
	maintenanceCalled := false
	err = fixture.authority.RunSessionMaintenance(
		context.Background(),
		sessionID.String(),
		func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error {
			maintenanceCalled = true
			return nil
		},
	)
	if !errors.Is(err, ErrSessionStartsBlocked) {
		t.Fatalf("unauthorized maintenance error = %v, want ErrSessionStartsBlocked", err)
	}
	if maintenanceCalled {
		t.Fatal("blocked maintenance callback ran")
	}
	authorizedCtx := release.AuthorizeMaintenance(context.Background())
	if err := fixture.authority.RunSessionMaintenance(
		authorizedCtx,
		sessionID.String(),
		func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error {
			maintenanceCalled = true
			return nil
		},
	); err != nil {
		t.Fatalf("authorized maintenance: %v", err)
	}
	if !maintenanceCalled {
		t.Fatal("authorized maintenance callback did not run")
	}
	if err := release.Close(context.Background()); err != nil {
		t.Fatalf("release session-start block: %v", err)
	}
}

func TestNilAuthorityHasNoBlockingRuntimeActivity(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	var authority *Authority
	active, err := authority.HasBlockingRuntimeActivity(context.Background(), fixture.store.Meta().SessionID)
	if err != nil || active {
		t.Fatalf("nil authority blocking activity = (%t, %v), want (false, nil)", active, err)
	}
}

func TestAuthorityMaintenanceRequiresEveryActiveBlockAuthorization(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	outer, err := fixture.authority.BlockSessionStarts(
		context.Background(),
		[]runtimeids.SessionID{sessionID},
		SessionStartBlockMaintenance,
	)
	if err != nil {
		t.Fatalf("block outer session starts: %v", err)
	}
	defer func() {
		if err := outer.Close(context.Background()); err != nil {
			t.Errorf("release outer session-start block: %v", err)
		}
	}()
	inner, err := fixture.authority.BlockSessionStarts(
		context.Background(),
		[]runtimeids.SessionID{sessionID},
		SessionStartBlockMaintenance,
	)
	if err != nil {
		t.Fatalf("block inner session starts: %v", err)
	}
	defer func() {
		if err := inner.Close(context.Background()); err != nil {
			t.Errorf("release inner session-start block: %v", err)
		}
	}()

	callbackCalled := false
	err = fixture.authority.RunSessionMaintenance(
		inner.AuthorizeMaintenance(context.Background()),
		sessionID.String(),
		func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error {
			callbackCalled = true
			return nil
		},
	)
	if !errors.Is(err, ErrSessionStartsBlocked) {
		t.Fatalf("partially authorized maintenance error = %v, want ErrSessionStartsBlocked", err)
	}
	if callbackCalled {
		t.Fatal("partially authorized maintenance callback ran")
	}

	authorizedCtx := inner.AuthorizeMaintenance(outer.AuthorizeMaintenance(context.Background()))
	if err := fixture.authority.RunSessionMaintenance(
		authorizedCtx,
		sessionID.String(),
		func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error {
			callbackCalled = true
			return nil
		},
	); err != nil {
		t.Fatalf("fully authorized maintenance: %v", err)
	}
	if !callbackCalled {
		t.Fatal("fully authorized maintenance callback did not run")
	}
}

func TestAuthorityBlockingRuntimeActivityIncludesMaintenanceStep(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	openLifecycleRuntime(t, fixture.authority, sessionID, "owner-a", &plan)

	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- fixture.authority.RunSessionMaintenance(
			context.Background(),
			sessionID.String(),
			func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error {
				close(started)
				<-release
				return nil
			},
		)
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime maintenance")
	}
	active, err := fixture.authority.HasBlockingRuntimeActivity(context.Background(), sessionID.String())
	if err != nil {
		t.Fatalf("check blocking runtime activity: %v", err)
	}
	if !active {
		t.Fatal("runtime maintenance was not reported as blocking activity")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("run session maintenance: %v", err)
	}
	active, err = fixture.authority.HasBlockingRuntimeActivity(context.Background(), sessionID.String())
	if err != nil {
		t.Fatalf("check blocking runtime activity after maintenance: %v", err)
	}
	if active {
		t.Fatal("completed runtime maintenance remained blocking")
	}
}

func TestAuthorityBlockingRuntimeActivityIncludesOpenLiveRunGroup(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := &ownerlessRetirementLLMClient{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	plan := authorityTestRuntimePlan(t, fixture, client)
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "owner-a", &plan)

	submitDone := make(chan error, 1)
	go func() {
		submitDone <- fixture.authority.WithRuntime(context.Background(), attachment.Resource(), func(ctx context.Context, engine *runtime.Engine) error {
			_, err := engine.SubmitUserMessage(ctx, "first")
			return err
		})
	}()
	select {
	case <-client.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the first live step")
	}

	beforeQueueStarted := make(chan struct{})
	releaseBeforeQueue := make(chan struct{})
	defer func() {
		select {
		case <-releaseBeforeQueue:
		default:
			close(releaseBeforeQueue)
		}
	}()
	queueDone := make(chan error, 1)
	go func() {
		queueDone <- fixture.authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
			item, accepted, err := engine.QueueUserMessageForActiveRun(
				context.Background(),
				"follow-up",
				runtimeids.NewRuntimeClientRequestID(),
				func() error {
					close(beforeQueueStarted)
					<-releaseBeforeQueue
					return nil
				},
			)
			if err == nil && (!accepted || item.ID == "") {
				return errors.New("active live run rejected queued follow-up")
			}
			return err
		})
	}()
	select {
	case <-beforeQueueStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live-run queue admission")
	}

	close(client.releaseFirst)
	if err := <-submitDone; err != nil {
		t.Fatalf("submit first live step: %v", err)
	}
	active, err := fixture.authority.HasBlockingRuntimeActivity(context.Background(), sessionID.String())
	if err != nil {
		t.Fatalf("check open live-run activity: %v", err)
	}
	if !active {
		t.Fatal("open live-run group without an engine step was not reported as blocking")
	}

	close(releaseBeforeQueue)
	if err := <-queueDone; err != nil {
		t.Fatalf("queue live-run follow-up: %v", err)
	}
}

func TestAuthorityBlockingRuntimeActivityIncludesDrainingResource(t *testing.T) {
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

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	defer func() {
		select {
		case <-releaseCallback:
		default:
			close(releaseCallback)
		}
	}()
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
			close(callbackStarted)
			<-releaseCallback
			return nil
		})
	}()
	select {
	case <-callbackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime callback")
	}

	closeDone := make(chan error, 1)
	go func() {
		_, err := attachment.Release(context.Background(), RuntimeReleaseClose)
		closeDone <- err
	}()
	select {
	case <-lifecycle.draining:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime draining")
	}
	active, err := authority.HasBlockingRuntimeActivity(context.Background(), sessionID.String())
	if err != nil {
		t.Fatalf("check draining runtime activity: %v", err)
	}
	if !active {
		t.Fatal("draining resource was not reported as blocking activity")
	}

	close(releaseCallback)
	if err := <-callbackDone; err != nil {
		t.Fatalf("runtime callback: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close draining runtime: %v", err)
	}
}
