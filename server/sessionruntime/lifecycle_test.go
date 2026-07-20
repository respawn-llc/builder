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
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
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
			item.Role == llm.RoleDeveloper &&
			item.MessageType == llm.MessageTypeWorktreeMode &&
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

	request := AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   OpenAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	}
	if _, err := fixture.authority.StartAgentExecution(context.Background(), request); !errors.Is(err, ErrSessionStartsBlocked) {
		t.Fatalf("start while blocked error = %v, want ErrSessionStartsBlocked", err)
	}
	if err := release.Close(context.Background()); err != nil {
		t.Fatalf("release session-start block: %v", err)
	}
}
