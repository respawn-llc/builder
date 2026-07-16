package runtimecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/transcript"
)

var runtimeControlOpenAICapabilities = llm.ProviderCapabilities{
	ProviderID:               "openai",
	SupportsResponsesAPI:     true,
	SupportsResponsesCompact: true,
	IsOpenAIFirstParty:       true,
}

type sessionStatusCountingResolver struct {
	stubRuntimeResolver
	publishCount int
}

func (r *sessionStatusCountingResolver) PublishSessionStatus(string) {
	r.publishCount++
}

func TestServiceSetThinkingLevelDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
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

type sequenceRuntimeActivityResolver struct {
	snapshots []runtimeactivity.ResponseSnapshot
	calls     int
}

func (r *sequenceRuntimeActivityResolver) Snapshot(context.Context, string, []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
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
	service := NewService(stubRuntimeResolver{}).WithRuntimeActivityResolver(resolver)
	req := serverapi.RuntimeInterruptRequest{ClientRequestID: "interrupt-retry", SessionID: "session-1"}

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

func TestServiceSetFastModeEnabledDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5", ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSetFastModeEnabledRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Enabled: true}

	first, err := service.SetFastModeEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetFastModeEnabled first: %v", err)
	}
	second, err := service.SetFastModeEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetFastModeEnabled replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if !engine.FastModeEnabled() {
		t.Fatal("expected fast mode to remain enabled")
	}
}

func TestServiceSetReviewerEnabledDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5", Reviewer: runtime.ReviewerConfig{Model: "gpt-5", ClientFactory: func() (llm.Client, error) { return &runtimeControlFakeClient{}, nil }}})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSetReviewerEnabledRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Enabled: true}

	first, err := service.SetReviewerEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetReviewerEnabled first: %v", err)
	}
	second, err := service.SetReviewerEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetReviewerEnabled replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if got := engine.ReviewerFrequency(); got != "edits" {
		t.Fatalf("reviewer frequency = %q, want edits", got)
	}
}

func TestServiceCommittedControlObserverErrorIsMemoized(t *testing.T) {
	type controlResult struct {
		changed bool
		applied bool
	}
	testCases := []struct {
		name      string
		newEngine func(*testing.T, *session.Store) *runtime.Engine
		run       func(*Service, *runtime.Engine, string, string) (controlResult, error)
	}{
		{
			name: "fast mode",
			newEngine: func(t *testing.T, store *session.Store) *runtime.Engine {
				t.Helper()
				engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{
					Model:                        "gpt-5",
					ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
				})
				if err != nil {
					t.Fatalf("create runtime engine: %v", err)
				}
				return engine
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
			newEngine: func(t *testing.T, store *session.Store) *runtime.Engine {
				t.Helper()
				engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{
					Model: "gpt-5",
					Reviewer: runtime.ReviewerConfig{
						Model: "gpt-5",
						ClientFactory: func() (llm.Client, error) {
							return &runtimeControlFakeClient{}, nil
						},
					},
				})
				if err != nil {
					t.Fatalf("create runtime engine: %v", err)
				}
				return engine
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
			newEngine: func(t *testing.T, store *session.Store) *runtime.Engine {
				t.Helper()
				engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
				if err != nil {
					t.Fatalf("create runtime engine: %v", err)
				}
				return engine
			},
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
			store, err := session.Create(
				t.TempDir(),
				"workspace-x",
				"/tmp/workspace-x",
				sessioncontract.SessionCategoryMain,
				append(runtimeControlTestSessionPersistence.Options(), session.WithPersistenceObserver(gate))...,
			)
			if err != nil {
				t.Fatalf("create session store: %v", err)
			}
			engine := testCase.newEngine(t, store)
			resolver := &sessionStatusCountingResolver{stubRuntimeResolver: stubRuntimeResolver{engine: engine}}
			service := NewService(resolver)
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

func TestServiceSetAutoCompactionEnabledDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSetAutoCompactionEnabledRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Enabled: false}

	first, err := service.SetAutoCompactionEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetAutoCompactionEnabled first: %v", err)
	}
	second, err := service.SetAutoCompactionEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetAutoCompactionEnabled replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if engine.AutoCompactionEnabled() {
		t.Fatal("expected auto compaction to remain disabled")
	}
}

func TestServiceCompactContextDedupesSuccessfulRetry(t *testing.T) {
	store, engine, client := newRuntimeControlCompactionFixture(t)
	service := NewService(stubRuntimeResolver{engine: engine})
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindCompact)
	req := serverapi.RuntimeCompactContextRequest{ClientRequestID: ref.ClientRequestID.String(), SessionID: store.Meta().SessionID, Args: "compact now", OperationRef: ref}

	if err := service.CompactContext(context.Background(), req); err != nil {
		t.Fatalf("CompactContext first: %v", err)
	}
	if err := service.CompactContext(context.Background(), req); err != nil {
		t.Fatalf("CompactContext replay: %v", err)
	}
	if client.compactionCalls != 1 {
		t.Fatalf("compaction call count = %d, want 1", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history_replaced event count = %d, want 1", got)
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
			store, engine, client := newRuntimeControlCompactionFixture(t, session.WithPersistenceObserver(gate))
			operations := runtimeops.NewCoordinator()
			service := NewService(stubRuntimeResolver{engine: engine}).WithOperationCoordinator(operations)
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

func TestServiceCompactContextForPreSubmitDedupesSuccessfulRetry(t *testing.T) {
	store, engine, client := newRuntimeControlCompactionFixture(t)
	service := NewService(stubRuntimeResolver{engine: engine})
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindPreSubmitCompact)
	req := serverapi.RuntimeCompactContextForPreSubmitRequest{ClientRequestID: ref.ClientRequestID.String(), SessionID: store.Meta().SessionID, OperationRef: ref}

	if err := service.CompactContextForPreSubmit(context.Background(), req); err != nil {
		t.Fatalf("CompactContextForPreSubmit first: %v", err)
	}
	if err := service.CompactContextForPreSubmit(context.Background(), req); err != nil {
		t.Fatalf("CompactContextForPreSubmit replay: %v", err)
	}
	if client.compactionCalls != 1 {
		t.Fatalf("compaction call count = %d, want 1", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history_replaced event count = %d, want 1", got)
	}
}

func TestServiceInterruptDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeInterruptRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID}

	if _, err := service.Interrupt(context.Background(), req); err != nil {
		t.Fatalf("Interrupt first: %v", err)
	}
	if _, err := service.Interrupt(context.Background(), req); err != nil {
		t.Fatalf("Interrupt replay: %v", err)
	}
}

func newRuntimeControlCompactionFixture(t *testing.T, options ...session.StoreOption) (*session.Store, *runtime.Engine, *runtimeControlFakeClient) {
	t.Helper()
	trimmed := 1
	store, err := session.Create(
		t.TempDir(),
		"workspace-x",
		"/tmp/workspace-x",
		sessioncontract.SessionCategoryMain,
		append(runtimeControlTestSessionPersistence.Options(), options...)...,
	)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	client := &runtimeControlFakeClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		}},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"},
				{Type: llm.ResponseItemTypeCompaction, EncryptedContent: "checkpoint"},
			},
			Usage:             llm.Usage{WindowTokens: 200000},
			TrimmedItemsCount: &trimmed,
		}},
	}
	engine, err := runtime.New(store, client, tools.NewRegistry(), runtime.Config{Model: "gpt-5", ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	return store, engine, client
}

func countEventsByKind(t *testing.T, store *session.Store, kind string) int {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	count := 0
	for _, evt := range events {
		if evt.Kind == kind {
			count++
		}
	}
	return count
}

func localEntryEvents(t *testing.T, store *session.Store) []runtime.ChatEntry {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	entries := make([]runtime.ChatEntry, 0)
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry runtime.ChatEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local_entry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestServiceAppendCommittedEntryDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeAppendCommittedEntryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Role: "warning", Text: "be careful"}

	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry first: %v", err)
	}
	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry replay: %v", err)
	}
	count := 0
	for _, entry := range localEntryEvents(t, store) {
		if entry.Role == "warning" && entry.Text == "be careful" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("local entry count = %d, want 1", count)
	}
}

func TestServiceAppendCommittedEntryReplaysVisibility(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
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

func TestServiceSubmitQueuedUserMessagesDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	client := &runtimeControlFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	engine, err := runtime.New(store, client, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	engine.QueueUserMessage("hello")
	service := NewService(stubRuntimeResolver{engine: engine})
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmitQueued)
	req := serverapi.RuntimeSubmitQueuedUserMessagesRequest{ClientRequestID: ref.ClientRequestID.String(), SessionID: store.Meta().SessionID, OperationRef: ref}

	first, err := service.SubmitQueuedUserMessages(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitQueuedUserMessages first: %v", err)
	}
	second, err := service.SubmitQueuedUserMessages(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitQueuedUserMessages replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if client.calls != 1 {
		t.Fatalf("generate call count = %d, want 1", client.calls)
	}
	if got := countUserMessagesWithContent(t, store, "hello"); got != 1 {
		t.Fatalf("queued user flush count = %d, want 1", got)
	}
}

func TestServiceSubmitQueuedUserMessagesConsumesCommittedObserverError(t *testing.T) {
	observerErr := errors.New("queued flush observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
	store, err := session.Create(
		t.TempDir(),
		"workspace-x",
		"/tmp/workspace-x",
		sessioncontract.SessionCategoryMain,
		append(runtimeControlTestSessionPersistence.Options(), session.WithPersistenceObserver(gate))...,
	)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	client := &runtimeControlFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "seeded", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	engine, err := runtime.New(store, client, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "seed"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	modelCallsBeforeSubmit := client.calls
	entriesBeforeSubmit := engine.CommittedTranscriptEntryCount()
	engine.QueueUserMessage("hello")
	operations := runtimeops.NewCoordinator()
	service := NewService(stubRuntimeResolver{engine: engine}).WithOperationCoordinator(operations)
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
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	firstQueued := engine.QueueUserMessage("same")
	otherQueued := engine.QueueUserMessage("other")
	duplicateQueued := engine.QueueUserMessage("same")
	service := NewService(stubRuntimeResolver{engine: engine})
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
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	history := newRuntimeControlPromptHistoryStore(store.Meta().SessionID)
	service := NewService(stubRuntimeResolver{engine: engine}).WithPromptHistoryStore(history)
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
