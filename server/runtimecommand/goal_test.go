package runtimecommand

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type goalAuthorityPersistenceObserver struct {
	persistence *sessiontest.Persistence

	mu        sync.Mutex
	snapshots []session.PersistedStoreSnapshot
}

func (o *goalAuthorityPersistenceObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	o.mu.Lock()
	o.snapshots = append(o.snapshots, snapshot)
	o.mu.Unlock()
	return o.persistence.ObservePersistedStore(ctx, snapshot)
}

func (o *goalAuthorityPersistenceObserver) ObserveEventLogReconciliation(ctx context.Context, reconciliation session.PersistedEventLogReconciliation) error {
	return o.persistence.ObserveEventLogReconciliation(ctx, reconciliation)
}

func (o *goalAuthorityPersistenceObserver) resetSnapshots() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.snapshots = nil
}

func (o *goalAuthorityPersistenceObserver) recordedSnapshots() []session.PersistedStoreSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]session.PersistedStoreSnapshot(nil), o.snapshots...)
}

type goalAuthorityClient struct{}

func (goalAuthorityClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func TestGoalAuthorityDormantSetPersistsMetadataBeforeOneNoticeWithoutRuntime(t *testing.T) {
	var liveEvents int
	store, authority, goalAuthority, observer := newGoalAuthorityFixture(t, func(sessionruntime.AgentResourceDescriptor, runtime.Event) {
		liveEvents++
	})
	observer.resetSnapshots()
	sessionID := mustGoalAuthoritySessionID(t, store)

	result, err := goalAuthority.Set(context.Background(), GoalSetCommand{
		SessionID: sessionID,
		Objective: "persist dormant goal",
		Actor:     session.GoalActorUser,
	})
	if err != nil {
		t.Fatalf("Set dormant goal: %v", err)
	}
	if result.Goal == nil || result.Goal.Objective != "persist dormant goal" ||
		result.Disposition != runtime.GoalCommandApplied ||
		!result.MetadataReceipt.Committed || !result.NoticeReceipt.Committed {
		t.Fatalf("dormant set result = %+v, want applied committed goal", result)
	}
	reopened := reopenGoalAuthorityStore(t, store, observer)
	if goal := reopened.Meta().Goal; goal == nil || goal.ID != result.Goal.ID || goal.Objective != result.Goal.Objective {
		t.Fatalf("persisted dormant goal = %+v, want %+v", goal, result.Goal)
	}
	if count := goalAuthorityNoticeCount(t, reopened); count != 1 {
		t.Fatalf("dormant goal notice count = %d, want 1", count)
	}
	snapshots := observer.recordedSnapshots()
	if len(snapshots) < 2 ||
		snapshots[0].Meta.Goal == nil || snapshots[0].Meta.LastSequence != 0 ||
		snapshots[1].Meta.Goal == nil || snapshots[1].Meta.LastSequence != 1 {
		t.Fatalf("dormant persistence snapshots = %+v, want goal metadata before one notice append", snapshots)
	}
	if _, ok := authority.SessionExecution(sessionID); ok {
		t.Fatal("dormant goal set created an agent execution")
	}
	if runtimeErr := authority.WithCurrentRuntime(context.Background(), sessionID, func(context.Context, *runtime.Engine) error {
		return nil
	}); !errors.Is(runtimeErr, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("dormant goal set runtime access error = %v, want runtime unavailable", runtimeErr)
	}
	if liveEvents != 0 {
		t.Fatalf("dormant goal set emitted %d live events, want none", liveEvents)
	}
}

func TestGoalAuthorityDormantSetPreservesSessionNotFound(t *testing.T) {
	_, _, goalAuthority, _ := newGoalAuthorityFixture(t, nil)
	sessionID, err := runtimeids.ParseSessionID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("parse unknown session id: %v", err)
	}

	_, err = goalAuthority.Set(context.Background(), GoalSetCommand{
		SessionID: sessionID,
		Objective: "missing session goal",
		Actor:     session.GoalActorUser,
	})

	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("dormant set error = %v, want session not found", err)
	}
	if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("dormant set error = %v, must not report runtime unavailable", err)
	}
}

func TestGoalAuthorityDormantSetRespectsAdmissionFence(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*testing.T, *sessionruntime.Authority, runtimeids.SessionID)
		wantError error
	}{
		{
			name: "blocked",
			prepare: func(t *testing.T, authority *sessionruntime.Authority, sessionID runtimeids.SessionID) {
				t.Helper()
				release, err := authority.BlockSessionStarts(
					context.Background(),
					[]runtimeids.SessionID{sessionID},
					sessionruntime.SessionStartBlockMaintenance,
				)
				if err != nil {
					t.Fatalf("block session starts: %v", err)
				}
				t.Cleanup(func() {
					if closeErr := release.Close(context.Background()); closeErr != nil {
						t.Errorf("release session-start block: %v", closeErr)
					}
				})
			},
			wantError: sessionruntime.ErrSessionStartsBlocked,
		},
		{
			name: "closed",
			prepare: func(t *testing.T, authority *sessionruntime.Authority, _ runtimeids.SessionID) {
				t.Helper()
				if err := authority.Close(context.Background()); err != nil {
					t.Fatalf("close authority: %v", err)
				}
			},
			wantError: sessionruntime.ErrAuthorityClosed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, authority, goalAuthority, observer := newGoalAuthorityFixture(t, nil)
			sessionID := mustGoalAuthoritySessionID(t, store)
			test.prepare(t, authority, sessionID)

			_, err := goalAuthority.Set(context.Background(), GoalSetCommand{
				SessionID: sessionID,
				Objective: "blocked dormant goal",
				Actor:     session.GoalActorUser,
			})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("dormant set error = %v, want %v", err, test.wantError)
			}
			if goal := store.Meta().Goal; goal != nil {
				t.Fatalf("goal after rejected dormant set = %+v, want nil", goal)
			}
			reopened := reopenGoalAuthorityStore(t, store, observer)
			if goal := reopened.Meta().Goal; goal != nil {
				t.Fatalf("persisted goal after rejected dormant set = %+v, want nil", goal)
			}
			if count := goalAuthorityNoticeCount(t, reopened); count != 0 {
				t.Fatalf("goal notices after rejected dormant set = %d, want 0", count)
			}
		})
	}
}

func TestGoalAuthorityLiveSetUsesRuntimeCommand(t *testing.T) {
	var eventMu sync.Mutex
	var goalFeedbackEvents int
	var goalStatusEvents int
	store, authority, goalAuthority, observer := newGoalAuthorityFixture(t, func(_ sessionruntime.AgentResourceDescriptor, event runtime.Event) {
		eventMu.Lock()
		defer eventMu.Unlock()
		if event.Kind == runtime.EventGoalStatusUpdated && event.GoalStatus != nil {
			goalStatusEvents++
		}
		for _, entry := range runtime.TranscriptEntriesFromEvent(event) {
			if entry.Role == string(transcript.EntryRoleGoalFeedback) {
				goalFeedbackEvents++
			}
		}
	})
	sessionID := mustGoalAuthoritySessionID(t, store)
	plan := workflowGoalAuthorityPlan(t, store.Meta().WorkspaceRoot)
	attachment, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "goal-authority-test",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open live runtime: %v", err)
	}
	t.Cleanup(func() {
		if _, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); releaseErr != nil {
			t.Errorf("release live runtime: %v", releaseErr)
		}
	})

	result, err := goalAuthority.Set(context.Background(), GoalSetCommand{
		SessionID: sessionID,
		Objective: "persist live goal",
		Actor:     session.GoalActorUser,
	})
	if err != nil {
		t.Fatalf("Set live goal: %v", err)
	}
	if result.Goal == nil || result.Goal.Objective != "persist live goal" ||
		result.Disposition != runtime.GoalCommandApplied ||
		!result.MetadataReceipt.Committed || !result.NoticeReceipt.Committed {
		t.Fatalf("live set result = %+v, want applied committed goal", result)
	}
	reopened := reopenGoalAuthorityStore(t, store, observer)
	if goal := reopened.Meta().Goal; goal == nil || goal.ID != result.Goal.ID {
		t.Fatalf("persisted live goal = %+v, want %+v", goal, result.Goal)
	}
	if count := goalAuthorityNoticeCount(t, reopened); count != 1 {
		t.Fatalf("live goal notice count = %d, want 1", count)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	if goalFeedbackEvents != 1 || goalStatusEvents != 1 {
		t.Fatalf(
			"live goal events = feedback:%d status:%d, want one of each",
			goalFeedbackEvents,
			goalStatusEvents,
		)
	}
}

func newGoalAuthorityFixture(
	t *testing.T,
	eventFeed sessionruntime.AgentResourceEventFeed,
) (*session.Store, *sessionruntime.Authority, *GoalAuthority, *goalAuthorityPersistenceObserver) {
	t.Helper()
	persistence := sessiontest.NewPersistence()
	observer := &goalAuthorityPersistenceObserver{persistence: persistence}
	options := []session.StoreOption{
		session.WithPersistenceObserver(observer),
		session.WithPersistedSessionResolver(persistence),
	}
	store, err := session.Create(
		t.TempDir(),
		"workspace-x",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
		options...,
	)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure session durable: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    options,
		EventFeed:       eventFeed,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	return store, authority, NewGoalAuthority(authority, NewExecutionAdapter(authority)), observer
}

func workflowGoalAuthorityPlan(t *testing.T, workdir string) sessionruntime.AgentRuntimePlan {
	t.Helper()
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Model = "gpt-5"
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:     settings,
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		Workdir:      workdir,
		Client:       goalAuthorityClient{},
		CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
			ScopeID: runtimeids.NewExecutionScopeID(),
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode: workflow.CurrentNodeReference{TaskID: "task-1", NodeID: "node-1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("new runtime plan: %v", err)
	}
	return plan
}

func mustGoalAuthoritySessionID(t *testing.T, store *session.Store) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	return sessionID
}

func reopenGoalAuthorityStore(t *testing.T, store *session.Store, observer *goalAuthorityPersistenceObserver) *session.Store {
	t.Helper()
	reopened, err := session.Open(
		store.Dir(),
		session.WithPersistenceObserver(observer),
		session.WithPersistedSessionResolver(observer.persistence),
	)
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	return reopened
}

func goalAuthorityNoticeCount(t *testing.T, store *session.Store) int {
	t.Helper()
	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("collect records: %v", err)
	}
	count := 0
	for _, record := range records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("read payload: %v", payloadErr)
		}
		message, ok := payload.(session.MessageRecord)
		if ok && message.MessageType != nil && *message.MessageType == session.MessageTypeGoal {
			count++
		}
	}
	return count
}
