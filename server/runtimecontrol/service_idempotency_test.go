package runtimecontrol

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"core/internal/testharness/filemode"
	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

var runtimeControlOpenAICapabilities = llm.ProviderCapabilities{
	ProviderID:               "openai",
	SupportsResponsesAPI:     true,
	SupportsResponsesCompact: true,
	IsOpenAIFirstParty:       true,
}

type sessionStatusCountingResolver struct {
	publishCount int
}

func (r *sessionStatusCountingResolver) RuntimeReadModelSnapshot(context.Context, string, []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	return runtimeactivity.ResponseSnapshot{}, nil
}

func (r *sessionStatusCountingResolver) PublishSessionStatus(string) {
	r.publishCount++
}

func TestServiceSetThinkingLevelDedupesSuccessfulRetry(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	req := serverapi.RuntimeSetThinkingLevelRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Level: "high"}

	if err := service.SetThinkingLevel(context.Background(), req); err != nil {
		t.Fatalf("SetThinkingLevel first: %v", err)
	}
	if err := service.SetThinkingLevel(context.Background(), req); err != nil {
		t.Fatalf("SetThinkingLevel replay: %v", err)
	}
	if got := engine.ThinkingLevel(); got != "high" {
		t.Fatalf("thinking level = %q, want high", got)
	}
}

func TestServiceGoalMutationReplaysCommittedFailuresWithoutRepeatingWork(t *testing.T) {
	t.Run("live metadata observer", func(t *testing.T) {
		observerErr := errors.New("live goal metadata observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
		store, _, service := newRuntimeControlTestService(
			t,
			nil,
			nil,
			runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}},
			session.WithPersistenceObserver(gate),
		)
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.Goal != nil && snapshot.Meta.LastSequence == 0
		}, observerErr)
		assertGoalSetReplayFailure(t, service, store, "live-metadata-replay", observerErr, 0)
	})

	t.Run("live notice observer", func(t *testing.T) {
		observerErr := errors.New("live goal notice observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
		store, _, service := newRuntimeControlTestService(
			t,
			nil,
			nil,
			runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}},
			session.WithPersistenceObserver(gate),
		)
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.Goal != nil && snapshot.Meta.LastSequence == 1
		}, observerErr)
		assertGoalSetReplayFailure(t, service, store, "live-notice-replay", observerErr, 1)
	})

	t.Run("dormant metadata observer", func(t *testing.T) {
		observerErr := errors.New("dormant goal metadata observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
		store, service := newDormantGoalControlService(t, session.WithPersistenceObserver(gate))
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.Goal != nil && snapshot.Meta.LastSequence == 0
		}, observerErr)
		assertGoalSetReplayFailure(t, service, store, "dormant-metadata-replay", observerErr, 0)
	})

	t.Run("dormant notice observer", func(t *testing.T) {
		observerErr := errors.New("dormant goal notice observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
		store, service := newDormantGoalControlService(t, session.WithPersistenceObserver(gate))
		gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.Goal != nil && snapshot.Meta.LastSequence == 1
		}, observerErr)
		assertGoalSetReplayFailure(t, service, store, "dormant-notice-replay", observerErr, 1)
	})

	t.Run("dormant notice before commit", func(t *testing.T) {
		store, service := newDormantGoalControlService(t)
		blocker := filemode.MustBlockEventLogAppends(t, filepath.Join(store.Dir(), "events.jsonl"))
		req := serverapi.RuntimeGoalSetRequest{
			ClientRequestID: "dormant-uncommitted-notice-replay",
			SessionID:       store.Meta().SessionID,
			Objective:       "persist before failed notice",
			Actor:           string(session.GoalActorUser),
		}
		first, firstErr := service.SetGoal(context.Background(), req)
		if firstErr == nil || first.Goal == nil {
			t.Fatalf("first dormant notice failure response/error = %+v / %v, want committed goal and error", first, firstErr)
		}
		if err := blocker.Restore(); err != nil {
			t.Fatalf("restore event-log appends: %v", err)
		}
		second, secondErr := service.SetGoal(context.Background(), req)
		if !errors.Is(secondErr, firstErr) {
			t.Fatalf("replayed dormant notice failure = %v, want original %v", secondErr, firstErr)
		}
		assertGoalResponsesEqual(t, first, second)
		if count := len(runtimeControlGoalDeveloperMessages(t, store)); count != 0 {
			t.Fatalf("dormant notices after uncommitted append failure = %d, want 0", count)
		}
	})
}

func TestServiceGoalMutationRetriesAfterUncommittedFailure(t *testing.T) {
	store, service := newDormantGoalControlService(t)
	pause := serverapi.RuntimeGoalStatusRequest{
		ClientRequestID: "retryable-dormant-pause",
		SessionID:       store.Meta().SessionID,
		Actor:           string(session.GoalActorUser),
	}
	if _, err := service.PauseGoal(context.Background(), pause); err == nil {
		t.Fatal("pause without a goal succeeded")
	}
	if _, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "retryable-dormant-set",
		SessionID:       store.Meta().SessionID,
		Objective:       "retry after no-goal failure",
		Actor:           string(session.GoalActorUser),
	}); err != nil {
		t.Fatalf("set goal after retryable failure: %v", err)
	}
	shown, showErr := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID})
	if showErr != nil || shown.Goal == nil {
		t.Fatalf("show goal after retryable failure = %+v / %v, want persisted goal", shown.Goal, showErr)
	}
	paused, err := service.PauseGoal(context.Background(), pause)
	if err != nil {
		t.Fatalf("replayed pause after goal creation: %v", err)
	}
	if paused.Goal == nil || paused.Goal.Status != string(session.GoalStatusPaused) {
		t.Fatalf("replayed pause response = %+v, want paused goal", paused.Goal)
	}
	if count := len(runtimeControlGoalDeveloperMessages(t, store)); count != 2 {
		t.Fatalf("notices after retryable pause = %d, want set and pause", count)
	}
}

func newDormantGoalControlService(t *testing.T, options ...session.StoreOption) (*session.Store, *Service) {
	t.Helper()
	store, _ := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{}, options...)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    append(runtimeControlTestSessionPersistence.Options(), options...),
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close dormant authority: %v", closeErr)
		}
	})
	return store, NewService(authority).WithPersistedSessionResolver(runtimeControlTestSessionPersistence)
}

func assertGoalSetReplayFailure(
	t *testing.T,
	service *Service,
	store *session.Store,
	requestID string,
	wantErr error,
	wantNotices int,
) {
	t.Helper()
	req := serverapi.RuntimeGoalSetRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		Objective:       "memoized goal mutation",
		Actor:           string(session.GoalActorUser),
	}
	first, firstErr := service.SetGoal(context.Background(), req)
	if !errors.Is(firstErr, wantErr) {
		t.Fatalf("first goal error = %v, want %v", firstErr, wantErr)
	}
	second, secondErr := service.SetGoal(context.Background(), req)
	if !errors.Is(secondErr, wantErr) {
		t.Fatalf("replayed goal error = %v, want %v", secondErr, wantErr)
	}
	assertGoalResponsesEqual(t, first, second)
	if count := len(runtimeControlGoalDeveloperMessages(t, store)); count != wantNotices {
		t.Fatalf("goal notices after replay = %d, want %d", count, wantNotices)
	}
}

func assertGoalResponsesEqual(t *testing.T, first, second serverapi.RuntimeGoalShowResponse) {
	t.Helper()
	if first.Goal == nil || second.Goal == nil {
		t.Fatalf("goal responses = (%+v, %+v), want projected goals", first.Goal, second.Goal)
	}
	if first.Goal.ID != second.Goal.ID ||
		first.Goal.Objective != second.Goal.Objective ||
		first.Goal.Status != second.Goal.Status ||
		!first.Goal.CreatedAt.Equal(second.Goal.CreatedAt) ||
		!first.Goal.UpdatedAt.Equal(second.Goal.UpdatedAt) {
		t.Fatalf("goal responses = (%+v, %+v), want stable identity and timestamps", first.Goal, second.Goal)
	}
}

type sequenceRuntimeActivityResolver struct {
	snapshots []runtimeactivity.ResponseSnapshot
	calls     int
}

func (r *sequenceRuntimeActivityResolver) RuntimeReadModelSnapshot(context.Context, string, []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	if r.calls >= len(r.snapshots) {
		return r.snapshots[len(r.snapshots)-1], nil
	}
	snapshot := r.snapshots[r.calls]
	r.calls++
	return snapshot, nil
}

func TestServiceInterruptRetryReturnsFreshActivitySnapshot(t *testing.T) {
	runningVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1}
	idleVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2}
	runID := mustRuntimeControlRunID(t)
	stepID := mustRuntimeControlStepID(t)
	resolver := &sequenceRuntimeActivityResolver{snapshots: []runtimeactivity.ResponseSnapshot{
		{
			Version: runningVersion,
			Activity: clientui.RuntimeActivity{
				State: clientui.RuntimeActivityRunning,
				ActiveStep: &clientui.RuntimeActiveStep{
					ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
					RunID:      runID,
					StepID:     stepID,
				},
			},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		},
		{
			Version: idleVersion,
			Activity: clientui.RuntimeActivity{
				State:          clientui.RuntimeActivityRegisteredIdle,
				QueueAccepting: true,
			},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		},
	}}
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).WithRuntimeActivityResolver(resolver)
	req := serverapi.RuntimeInterruptRequest{ClientRequestID: "interrupt-retry", SessionID: "018fdd67-89ab-4cde-8123-456789abcdef"}

	first, err := service.Interrupt(context.Background(), req)
	if err != nil {
		t.Fatalf("Interrupt first: %v", err)
	}
	second, err := service.Interrupt(context.Background(), req)
	if err != nil {
		t.Fatalf("Interrupt retry: %v", err)
	}
	if !first.Activity.ActiveForControl() {
		t.Fatalf("first activity = %+v, want active", first.Activity)
	}
	if second.Activity.ActiveForControl() || second.Version != idleVersion {
		t.Fatalf("retry activity/version = %+v/%+v, want fresh idle %+v", second.Activity, second.Version, idleVersion)
	}
	if resolver.calls != 2 {
		t.Fatalf("snapshot calls = %d, want fresh composition on retry", resolver.calls)
	}
}

func TestServiceCommittedControlObserverErrorIsMemoized(t *testing.T) {
	type controlResult struct {
		changed bool
		applied bool
	}
	testCases := []struct {
		name string
		cfg  runtime.Config
		run  func(*Service, *runtime.Engine, string, string) (controlResult, error)
	}{
		{
			name: "fast mode",
			cfg: runtime.Config{
				Model:                        "gpt-5",
				ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
			},
			run: func(service *Service, engine *runtime.Engine, sessionID string, requestID string) (controlResult, error) {
				resp, err := service.SetFastModeEnabled(context.Background(), serverapi.RuntimeSetFastModeEnabledRequest{
					ClientRequestID: requestID,
					SessionID:       sessionID,
					Enabled:         true,
				})
				return controlResult{changed: resp.Changed, applied: engine.FastModeEnabled()}, err
			},
		},
		{
			name: "reviewer",
			cfg: runtime.Config{
				Model: "gpt-5",
				Reviewer: runtime.ReviewerConfig{
					Model: "gpt-5",
					ClientFactory: func() (llm.Client, error) {
						return &runtimeControlFakeClient{}, nil
					},
				},
			},
			run: func(service *Service, engine *runtime.Engine, sessionID string, requestID string) (controlResult, error) {
				resp, err := service.SetReviewerEnabled(context.Background(), serverapi.RuntimeSetReviewerEnabledRequest{
					ClientRequestID: requestID,
					SessionID:       sessionID,
					Enabled:         true,
				})
				return controlResult{changed: resp.Changed, applied: resp.Mode == "edits" && engine.ReviewerFrequency() == "edits"}, err
			},
		},
		{
			name: "questions",
			cfg:  runtime.Config{Model: "gpt-5"},
			run: func(service *Service, engine *runtime.Engine, sessionID string, requestID string) (controlResult, error) {
				resp, err := service.SetQuestionsEnabled(context.Background(), serverapi.RuntimeSetQuestionsEnabledRequest{
					ClientRequestID: requestID,
					SessionID:       sessionID,
					Enabled:         false,
				})
				return controlResult{changed: resp.Changed, applied: !resp.Enabled && !engine.QuestionsEnabled()}, err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			observerErr := errors.New("control feedback observer failed")
			gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
			store, engine, service := newRuntimeControlTestService(
				t,
				&runtimeControlFakeClient{},
				nil,
				testCase.cfg,
				session.WithPersistenceObserver(gate),
			)
			resolver := &sessionStatusCountingResolver{}
			service.WithRuntimeActivityResolver(resolver)
			gate.FailNext(observerErr)

			first, err := testCase.run(service, engine, store.Meta().SessionID, "req-committed-control")
			if !errors.Is(err, observerErr) {
				t.Fatalf("first control error = %v, want observer error", err)
			}
			second, err := testCase.run(service, engine, store.Meta().SessionID, "req-committed-control")
			if !errors.Is(err, observerErr) {
				t.Fatalf("replayed control error = %v, want cached observer error", err)
			}
			if first != second || !first.changed || !first.applied {
				t.Fatalf("control results = (%+v, %+v), want identical committed result", first, second)
			}
			if got := len(localEntryEvents(t, store)); got != 1 {
				t.Fatalf("durable control feedback count = %d, want 1", got)
			}
			if resolver.publishCount != 1 {
				t.Fatalf("session status publish count = %d, want 1", resolver.publishCount)
			}
		})
	}
}

func TestServiceCompactionConsumesCommittedObserverError(t *testing.T) {
	testCases := []struct {
		name string
		kind clientui.RuntimeOperationKind
		run  func(*Service, string, clientui.RuntimeOperationRef) error
	}{
		{
			name: "manual",
			kind: clientui.RuntimeOperationKindCompact,
			run: func(service *Service, sessionID string, ref clientui.RuntimeOperationRef) error {
				return service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
					ClientRequestID: ref.ClientRequestID.String(),
					SessionID:       sessionID,
					Args:            "compact now",
					OperationRef:    ref,
				})
			},
		},
		{
			name: "pre-submit",
			kind: clientui.RuntimeOperationKindPreSubmitCompact,
			run: func(service *Service, sessionID string, ref clientui.RuntimeOperationRef) error {
				return service.CompactContextForPreSubmit(context.Background(), serverapi.RuntimeCompactContextForPreSubmitRequest{
					ClientRequestID: ref.ClientRequestID.String(),
					SessionID:       sessionID,
					OperationRef:    ref,
				})
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			observerErr := errors.New("history replacement observer failed")
			gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
			store, _, client, service := newRuntimeControlCompactionFixture(t, session.WithPersistenceObserver(gate))
			operations := runtimeops.NewCoordinator()
			service.WithOperationCoordinator(operations)
			ref := runtimeControlOperationRef(testCase.kind)
			gate.FailNext(observerErr)

			if err := testCase.run(service, store.Meta().SessionID, ref); !errors.Is(err, observerErr) {
				t.Fatalf("first compaction error = %v, want observer error", err)
			}
			if err := testCase.run(service, store.Meta().SessionID, ref); !errors.Is(err, observerErr) {
				t.Fatalf("replayed compaction error = %v, want cached observer error", err)
			}
			if client.compactionCalls != 1 {
				t.Fatalf("compaction call count = %d, want 1", client.compactionCalls)
			}
			if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
				t.Fatalf("history_replaced event count = %d, want 1", got)
			}
			snapshot := runtimeControlFeedSnapshot(t, operations, store.Meta().SessionID, []clientui.RuntimeOperationRef{ref})
			if len(snapshot.Operations) != 1 || snapshot.Operations[0].State != clientui.RuntimeInputReconciliationCommitted {
				t.Fatalf("compaction reconciliation = %+v, want committed", snapshot)
			}
		})
	}
}

func newRuntimeControlCompactionFixture(t *testing.T, options ...session.StoreOption) (*session.Store, *runtime.Engine, *runtimeControlFakeClient, *Service) {
	t.Helper()
	trimmed := 1
	client := &runtimeControlFakeClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		}},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
				{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("checkpoint")},
			},
			Usage:             llm.Usage{WindowTokens: 200000},
			TrimmedItemsCount: &trimmed,
		}},
	}
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	}, options...)
	if _, err := engine.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	return store, engine, client, service
}

func countEventsByKind(t *testing.T, store *session.Store, kind string) int {
	t.Helper()
	events, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	count := 0
	for _, evt := range events {
		eventKind, kindErr := evt.Kind()
		if kindErr != nil {
			t.Fatalf("event kind: %v", kindErr)
		}
		if string(eventKind) == kind {
			count++
		}
	}
	return count
}

func localEntryEvents(t *testing.T, store *session.Store) []runtime.ChatEntry {
	t.Helper()
	events, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	entries := make([]runtime.ChatEntry, 0)
	for _, evt := range events {
		kind, kindErr := evt.Kind()
		if kindErr != nil {
			t.Fatalf("event kind: %v", kindErr)
		}
		if kind != session.EventKindLocalEntry {
			continue
		}
		payload, payloadErr := evt.Payload()
		if payloadErr != nil {
			t.Fatalf("local_entry payload: %v", payloadErr)
		}
		entryRecord, ok := payload.(session.LocalEntryRecord)
		if !ok {
			t.Fatalf("local_entry payload = %T, want session.LocalEntryRecord", payload)
		}
		entries = append(entries, runtime.ChatEntry{
			Role: entryRecord.Role,
			Text: entryRecord.Text,
			Visibility: transcript.NormalizeEntryVisibility(
				transcript.EntryVisibility(entryRecord.Visibility),
			),
		})
	}
	return entries
}

func TestServiceAppendCommittedEntryReplaysVisibility(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	req := serverapi.RuntimeAppendCommittedEntryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Role: "warning", Text: "visible warning", Visibility: string(transcript.EntryVisibilityOngoing)}

	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry first: %v", err)
	}
	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry replay: %v", err)
	}
	count := 0
	for _, entry := range localEntryEvents(t, store) {
		if entry.Role == "warning" && entry.Text == "visible warning" {
			count++
			if entry.Visibility != transcript.EntryVisibilityOngoing {
				t.Fatalf("entry visibility = %q, want ongoing", entry.Visibility)
			}
		}
	}
	if count != 1 {
		t.Fatalf("visible warning entry count = %d, want 1", count)
	}
}

func TestServiceSubmitQueuedUserMessagesConsumesCommittedObserverError(t *testing.T) {
	observerErr := errors.New("queued flush observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
	client := &runtimeControlFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{}, session.WithPersistenceObserver(gate))
	if _, err := engine.SubmitUserMessage(context.Background(), "seed"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	modelCallsBeforeSubmit := client.calls
	entriesBeforeSubmit := engine.CommittedTranscriptEntryCount()
	engine.QueueUserMessage("hello")
	operations := runtimeops.NewCoordinator()
	service.WithOperationCoordinator(operations)
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmitQueued)
	req := serverapi.RuntimeSubmitQueuedUserMessagesRequest{
		ClientRequestID: ref.ClientRequestID.String(),
		SessionID:       store.Meta().SessionID,
		OperationRef:    ref,
	}
	gate.FailNext(observerErr)

	first, err := service.SubmitQueuedUserMessages(context.Background(), req)
	if !errors.Is(err, observerErr) {
		t.Fatalf("first queued submission error = %v, want observer error", err)
	}
	second, err := service.SubmitQueuedUserMessages(context.Background(), req)
	if !errors.Is(err, observerErr) {
		t.Fatalf("replayed queued submission error = %v, want cached observer error", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if client.calls != modelCallsBeforeSubmit {
		t.Fatalf("generate call count changed from %d to %d", modelCallsBeforeSubmit, client.calls)
	}
	if got := countUserMessagesWithContent(t, store, "hello"); got != 1 {
		t.Fatalf("queued user flush count = %d, want 1", got)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("committed queued flush retained retry ownership")
	}
	if got := engine.CommittedTranscriptEntryCount(); got != entriesBeforeSubmit+1 {
		t.Fatalf("projected transcript entries = %d, want %d", got, entriesBeforeSubmit+1)
	}
	snapshot := runtimeControlFeedSnapshot(t, operations, store.Meta().SessionID, []clientui.RuntimeOperationRef{ref})
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].State != clientui.RuntimeInputReconciliationSubmitted {
		t.Fatalf("queued submission reconciliation = %+v, want submitted", snapshot)
	}
}

func TestServiceDiscardQueuedUserMessageDedupesSuccessfulRetry(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	firstQueued := engine.QueueUserMessage("same")
	otherQueued := engine.QueueUserMessage("other")
	duplicateQueued := engine.QueueUserMessage("same")
	req := serverapi.RuntimeDiscardQueuedUserMessageRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, QueueItemID: duplicateQueued.ID}

	first, err := service.DiscardQueuedUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("DiscardQueuedUserMessage first: %v", err)
	}
	second, err := service.DiscardQueuedUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("DiscardQueuedUserMessage replay: %v", err)
	}
	if !first.Discarded || !second.Discarded {
		t.Fatalf("discard results = (%t, %t), want both true", first.Discarded, second.Discarded)
	}
	if !engine.DiscardQueuedUserMessage(firstQueued.ID) {
		t.Fatal("expected first duplicate text item to remain")
	}
	if !engine.DiscardQueuedUserMessage(otherQueued.ID) {
		t.Fatal("expected other queued item to remain")
	}
	if engine.DiscardQueuedUserMessage(duplicateQueued.ID) {
		t.Fatal("did not expect discarded queue item to remain")
	}
}

func TestServiceRecordPromptHistoryDedupesSuccessfulRetry(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	history := newRuntimeControlPromptHistoryStore(store.Meta().SessionID)
	service.WithPromptHistoryStore(history)
	req := serverapi.RuntimeRecordPromptHistoryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Text: "/resume"}

	if err := service.RecordPromptHistory(context.Background(), req); err != nil {
		t.Fatalf("RecordPromptHistory first: %v", err)
	}
	if err := service.RecordPromptHistory(context.Background(), req); err != nil {
		t.Fatalf("RecordPromptHistory replay: %v", err)
	}
	if got := countPromptHistoryEvents(t, store, "/resume"); got != 1 {
		t.Fatalf("prompt history count = %d, want 1", got)
	}
}

func runtimeControlOperationRef(kind clientui.RuntimeOperationKind) clientui.RuntimeOperationRef {
	return clientui.RuntimeOperationRef{
		Kind:            kind,
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
	}
}

func runtimeControlFeedSnapshot(t *testing.T, operations *runtimeops.Coordinator, sessionID string, refs []clientui.RuntimeOperationRef) clientui.RuntimeInputReconciliationSnapshot {
	t.Helper()
	snapshot, err := operations.FeedSnapshot(sessionID, refs)
	if err != nil {
		t.Fatalf("FeedSnapshot: %v", err)
	}
	return snapshot
}

func mustRuntimeControlRunID(t *testing.T) runtimeids.RunID {
	t.Helper()
	id, err := runtimeids.ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseRunID: %v", err)
	}
	return id
}

func mustRuntimeControlStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return id
}

func mustRuntimeControlQueueItemID(t *testing.T, raw string) runtimeids.QueueItemID {
	t.Helper()
	id, err := runtimeids.ParseQueueItemID(raw)
	if err != nil {
		t.Fatalf("ParseQueueItemID: %v", err)
	}
	return id
}
