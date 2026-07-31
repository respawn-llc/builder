package sessionruntime

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
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

func (o *lifecycleReminderQueueObserver) ObservePersistedStore(_ context.Context, snapshot session.PersistedStoreSnapshot) error {
	if snapshot.Meta.WorktreeReminder != nil {
		o.once.Do(o.queue)
	}
	return nil
}

type lifecycleRequestCaptureClient chan llm.Request

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

type lifecycleContinuationClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *lifecycleContinuationClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("follow-up"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil
}

func TestAuthoritySubmittedTurnReturnsBeforeSameScopeQueuedFollowUp(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	client := &lifecycleContinuationClient{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		close(client.release)
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, client)
	attachment := openLifecycleRuntime(t, authority, sessionID, "command-owner", &plan)
	t.Cleanup(func() {
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("release runtime: %v", err)
		}
	})

	callbackStarted := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- authority.RunCurrentAgentExecution(
			context.Background(),
			mustOpenSessionDescriptor(t, sessionID),
			func(_ context.Context, engine *runtime.Engine) error {
				close(callbackStarted)
				engine.QueueUserMessageForAutoDrain("same-scope follow-up", "follow-up-request")
				return nil
			},
		)
	}()

	select {
	case <-callbackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("initial submitted turn did not start")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("submitted turn result: %v", err)
		}
	case <-client.started:
		t.Fatal("same-scope follow-up provider started before submitted-turn result")
	case <-time.After(3 * time.Second):
		t.Fatal("submitted-turn result did not complete before follow-up")
	}

	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("same-scope follow-up did not start")
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

func (l *authorityStartBarrierLifecycle) ResourceReady(ctx context.Context, _ AgentResourceDescriptor, _ *runtime.Engine, _ AgentResourceRetainer) error {
	return l.ArriveAndWait(ctx)
}

func (l *authorityStartBarrierLifecycle) ResourceDraining(context.Context, AgentResourceDescriptor) error {
	return nil
}

type commandLifecycleProbe struct {
	mu     sync.Mutex
	events []string
}

func (p *commandLifecycleProbe) record(event string) {
	p.mu.Lock()
	p.events = append(p.events, event)
	p.mu.Unlock()
}

func (p *commandLifecycleProbe) AdmitResource(context.Context, runtimeids.SessionResourceRef) error {
	p.record("command-admit")
	return nil
}

func (p *commandLifecycleProbe) CloseResource(context.Context, runtimeids.SessionResourceRef) error {
	p.record("command-close")
	return nil
}

func (p *commandLifecycleProbe) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}

type commandLifecycleResourceProbe struct {
	commands *commandLifecycleProbe
}

func (p *commandLifecycleResourceProbe) ResourceReady(context.Context, AgentResourceDescriptor, *runtime.Engine, AgentResourceRetainer) error {
	p.commands.record("resource-ready")
	return nil
}

func (p *commandLifecycleResourceProbe) ResourceDraining(context.Context, AgentResourceDescriptor) error {
	p.commands.record("resource-draining")
	return nil
}

type lifecycleExecutionLeaseProbe struct {
	commitOnce  sync.Once
	abortOnce   sync.Once
	releaseOnce sync.Once
	committed   chan struct{}
	released    chan struct{}
	ordered     atomic.Int32
}

func newLifecycleExecutionLeaseProbe() *lifecycleExecutionLeaseProbe {
	return &lifecycleExecutionLeaseProbe{
		committed: make(chan struct{}),
		released:  make(chan struct{}),
	}
}

func (p *lifecycleExecutionLeaseProbe) Wait(ctx context.Context) error {
	select {
	case <-p.committed:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (p *lifecycleExecutionLeaseProbe) Commit() error {
	p.commitOnce.Do(func() { close(p.committed) })
	return nil
}

func (p *lifecycleExecutionLeaseProbe) Abort(error) error {
	p.abortOnce.Do(func() {})
	return nil
}

func (p *lifecycleExecutionLeaseProbe) Release() error {
	p.releaseOnce.Do(func() { close(p.released) })
	return nil
}

func (p *lifecycleExecutionLeaseProbe) OrderedMutation(_ context.Context, apply func(runtime.OrderedMutationTurn) error) error {
	p.ordered.Add(1)
	return apply(directOrderedMutationTurn{})
}

type lifecycleExecutionCommandProbe struct {
	commands *commandLifecycleProbe
	lease    *lifecycleExecutionLeaseProbe
}

func (p *lifecycleExecutionCommandProbe) AdmitResource(ctx context.Context, ref runtimeids.SessionResourceRef) error {
	return p.commands.AdmitResource(ctx, ref)
}

func (p *lifecycleExecutionCommandProbe) CloseResource(ctx context.Context, ref runtimeids.SessionResourceRef) error {
	return p.commands.CloseResource(ctx, ref)
}

func (p *lifecycleExecutionCommandProbe) AcquireExecution(context.Context, runtimeids.SessionResourceRef) (ResourceExecutionLease, error) {
	p.commands.record("execution-acquire")
	return p.lease, nil
}

func TestAuthorityBindsCommandQueueAroundResourceReadinessAndClosure(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	commands := &commandLifecycleProbe{}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		CommandLifecycle:  commands,
		ResourceLifecycle: &commandLifecycleResourceProbe{commands: commands},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, authority, sessionID, "command-owner", &plan)
	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release runtime: %v", err)
	}

	events := commands.snapshot()
	admitIndex := slices.Index(events, "command-admit")
	readyIndex := slices.Index(events, "resource-ready")
	drainingIndex := slices.Index(events, "resource-draining")
	closeIndex := slices.Index(events, "command-close")
	if admitIndex < 0 || readyIndex < 0 || drainingIndex < 0 || closeIndex < 0 {
		t.Fatalf("command lifecycle events = %v, want all lifecycle boundaries", events)
	}
	if admitIndex > readyIndex {
		t.Fatalf("command admission happened after resource ready: %v", events)
	}
	if drainingIndex > closeIndex {
		t.Fatalf("command queue closed before resource draining callback: %v", events)
	}
}

func TestAuthorityExecutionWaitsOnCommandLeaseAndReleasesAtTerminalCleanup(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	commands := &commandLifecycleProbe{}
	lease := newLifecycleExecutionLeaseProbe()
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		CommandLifecycle: &lifecycleExecutionCommandProbe{
			commands: commands,
			lease:    lease,
		},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, authority, sessionID, "command-owner", &plan)
	runnerStarted := make(chan struct{})
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			close(runnerStarted)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	select {
	case <-runnerStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("command lease did not commit the execution start gate")
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait execution: %v", err)
	}
	select {
	case <-lease.released:
	case <-time.After(3 * time.Second):
		t.Fatal("execution lease was not released at terminal cleanup")
	}
	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release runtime: %v", err)
	}
}

func TestAuthorityExecutionReusesCommandPermitForOrderedEngineMutation(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	commands := &commandLifecycleProbe{}
	lease := newLifecycleExecutionLeaseProbe()
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		CommandLifecycle: &lifecycleExecutionCommandProbe{
			commands: commands,
			lease:    lease,
		},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, authority, sessionID, "command-owner", &plan)
	t.Cleanup(func() {
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("release runtime: %v", err)
		}
	})

	started := make(chan struct{})
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runner: func(_ context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			close(started)
			return bridge.WithEngine(context.Background(), func(_ context.Context, engine *runtime.Engine) error {
				return engine.AppendCommittedEntry("system", "ordered from execution")
			})
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("execution runner did not start")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := handle.Wait(waitCtx); err != nil {
		t.Fatalf("execution ordered mutation deadlocked or failed: %v", err)
	}
	if lease.ordered.Load() == 0 {
		t.Fatal("execution mutation did not reuse its command-owned ordering continuation")
	}
}

func TestAuthorityOrderedExecutionCapturesExactAgentScope(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	var captured ExecutionScope
	var calls atomic.Int32
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		AgentOrderedMutation: func(_ context.Context, scope ExecutionScope, apply func(runtime.OrderedMutationTurn) error) error {
			captured = scope
			calls.Add(1)
			return apply(directOrderedMutationTurn{})
		},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, authority, sessionID, "command-owner", &plan)
	runnerRelease := make(chan struct{})
	var releaseRunner sync.Once
	release := func() { releaseRunner.Do(func() { close(runnerRelease) }) }
	t.Cleanup(release)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			<-runnerRelease
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	if err := authority.WithOrderedExecution(context.Background(), handle.Scope().ID(), func() error {
		return nil
	}); err != nil {
		t.Fatalf("ordered execution: %v", err)
	}
	if calls.Load() != 1 || captured.ID() != handle.Scope().ID() || captured.Kind() != ExecutionScopeAgent {
		t.Fatalf("captured ordered scope = %#v calls=%d, want exact agent scope %s", captured, calls.Load(), handle.Scope().ID())
	}
	release()
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait execution: %v", err)
	}
	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release runtime: %v", err)
	}
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
