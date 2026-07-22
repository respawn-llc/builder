package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
