package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/filemode"
	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/runtimeids"
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

func TestChatSettingsDurabilityAbortRetiresCurrentResourceGeneration(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
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
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	authority.mu.Lock()
	failedResource := authority.resources[sessionID]
	authority.mu.Unlock()
	recoveryPath := filepath.Join(failedResource.store.Dir(), "append-recovery.json")
	if err := os.Mkdir(recoveryPath, 0o755); err != nil {
		t.Fatalf("block Chat settings persistence: %v", err)
	}

	err := authority.WithCurrentRuntime(t.Context(), sessionID, func(ctx context.Context, engine *runtime.Engine) error {
		_, _, _, mutationErr := engine.SetQuestionsEnabledWithCommittedFeedback(ctx, false, func(bool, bool) string {
			return "feedback"
		})
		return mutationErr
	})
	if err == nil {
		t.Fatal("uncommitted Chat settings mutation succeeded")
	}
	select {
	case <-lifecycle.draining:
	case <-time.After(3 * time.Second):
		t.Fatal("Chat settings durability abort did not retire the resource")
	}
	deadline := time.Now().Add(3 * time.Second)
	var admitted *agentResource
	for time.Now().Before(deadline) {
		authority.mu.Lock()
		admitted = authority.resources[sessionID]
		authority.mu.Unlock()
		if failedResource.descriptor().State == AgentResourceClosed && admitted != failedResource {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if state := failedResource.descriptor().State; state != AgentResourceClosed {
		t.Fatalf("failed resource state = %v, want closed", state)
	}
	if admitted == failedResource {
		t.Fatal("failed Chat settings resource generation remained admitted")
	}
	if err := os.Remove(recoveryPath); err != nil {
		t.Fatalf("restore Chat settings persistence: %v", err)
	}
	reopened := openLifecycleRuntime(t, authority, sessionID, "owner-b", &plan)
	if reopened.Resource() == attachment.Resource() {
		t.Fatal("later open reused the failed Chat settings resource generation")
	}
}

func TestGoalLifecycleDurabilityAbortRetiresCurrentGeneration(t *testing.T) {
	previousMaxProcs := goruntime.GOMAXPROCS(1)
	defer goruntime.GOMAXPROCS(previousMaxProcs)

	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
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

	client := &lifecycleQuestionBarrierClient{}
	var failedResource *agentResource
	var blocker *filemode.EventLogAppendBlocker
	var blockErr error
	blocked := make(chan struct{})
	var blockOnce sync.Once
	plan := authorityTestRuntimePlan(t, fixture, client, func(event runtime.Event) {
		if event.Kind == runtime.EventToolCallStarted &&
			event.ToolCall != nil &&
			event.ToolCall.ID == "lifecycle-question" {
			blockOnce.Do(func() {
				blocker, blockErr = filemode.BlockEventLogAppends(
					filepath.Join(failedResource.store.Dir(), "events.jsonl"),
				)
				close(blocked)
			})
		}
	})
	plan.options.EnabledTools = []toolspec.ID{
		toolspec.ToolAskQuestion,
		toolspec.ToolWebSearch,
	}
	plan.options.Settings.WebSearch = "native"
	capabilities := llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsNativeWebSearch:       true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}
	plan.options.ProviderCapabilitiesOverride = &capabilities
	attachment := openLifecycleRuntime(t, authority, sessionID, "owner-a", &plan)
	authority.mu.Lock()
	failedResource = authority.resources[sessionID]
	authority.mu.Unlock()
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	var releaseCallbackOnce sync.Once
	releaseActiveCallback := func() {
		releaseCallbackOnce.Do(func() { close(releaseCallback) })
	}
	t.Cleanup(releaseActiveCallback)
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- authority.WithCurrentRuntime(
			context.Background(),
			sessionID,
			func(context.Context, *runtime.Engine) error {
				close(callbackEntered)
				<-releaseCallback
				return nil
			},
		)
	}()
	select {
	case <-callbackEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("current runtime callback did not become active")
	}

	if err := authority.RunCurrentAgentExecution(
		context.Background(),
		mustOpenSessionDescriptor(t, sessionID),
		func(
			_ context.Context,
			engine *runtime.Engine,
		) error {
			if _, err := engine.SetGoal(t.Context(), "continue autonomously", session.GoalActorUser); err != nil {
				return err
			}
			return engine.StartGoalLoop()
		}); err != nil {
		t.Fatalf("start Goal lifecycle turn: %v", err)
	}

	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("Goal lifecycle turn did not reach Result Group append")
	}
	if blockErr != nil {
		t.Fatalf("block Goal lifecycle event-log append: %v", blockErr)
	}
	select {
	case <-lifecycle.draining:
	case <-time.After(3 * time.Second):
		t.Fatal("Goal lifecycle runtime abort did not retire the resource")
	}
	if state := failedResource.descriptor().State; state != AgentResourceDraining {
		t.Fatalf("Goal lifecycle resource state = %v, want draining while callback is active", state)
	}
	releaseActiveCallback()
	select {
	case callbackErr := <-callbackDone:
		if callbackErr != nil {
			t.Fatalf("release current runtime callback: %v", callbackErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("current runtime callback did not release")
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore Goal lifecycle event log: %v", err)
	}
	var admitted *agentResource
	deadline := time.Now().Add(3 * time.Second)
	for {
		authority.mu.Lock()
		admitted = authority.resources[sessionID]
		authority.mu.Unlock()
		if admitted != failedResource || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if admitted == failedResource {
		t.Fatal("Goal lifecycle aborted resource remained admitted")
	}
	if state := failedResource.descriptor().State; state != AgentResourceClosed {
		t.Fatalf("Goal lifecycle aborted resource state = %v, want closed", state)
	}
	reopened := openLifecycleRuntime(t, authority, sessionID, "owner-b", &plan)
	if reopened.Resource() == attachment.Resource() {
		t.Fatal("Goal lifecycle abort reused the failed resource generation")
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

func TestAuthorityBlockingRuntimeActivityDoesNotInventMaintenanceStep(t *testing.T) {
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
	if active {
		t.Fatal("runtime maintenance fabricated live Runtime activity")
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
