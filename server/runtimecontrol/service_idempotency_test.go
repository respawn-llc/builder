package runtimecontrol

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/tools"
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
	publishErr   error
}

func (r *sessionStatusCountingResolver) RuntimeReadModelSnapshot(context.Context, string) (runtimeactivity.ResponseSnapshot, error) {
	return runtimeactivity.ResponseSnapshot{}, nil
}

func (r *sessionStatusCountingResolver) PublishSessionStatus(string) error {
	r.publishCount++
	return r.publishErr
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

func TestServiceCommittedRuntimeMutationReturnsAndCachesSessionStatusPublishError(t *testing.T) {
	statusErr := errors.New("session status publish failed")
	store, engine, service := newRuntimeControlTestService(t, &runtimeControlFakeClient{}, nil, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	})
	resolver := &sessionStatusCountingResolver{publishErr: statusErr}
	service.WithRuntimeActivityResolver(resolver)
	req := serverapi.RuntimeSetFastModeEnabledRequest{
		ClientRequestID: "status-publish-error",
		SessionID:       store.Meta().SessionID,
		Enabled:         true,
	}

	first, err := service.SetFastModeEnabled(context.Background(), req)
	if !errors.Is(err, statusErr) {
		t.Fatalf("first SetFastModeEnabled error = %v, want status publish error", err)
	}
	second, err := service.SetFastModeEnabled(context.Background(), req)
	if !errors.Is(err, statusErr) {
		t.Fatalf("replayed SetFastModeEnabled error = %v, want status publish error", err)
	}
	if !first.Changed || first != second || !engine.FastModeEnabled() {
		t.Fatalf("responses = (%+v, %+v), fast mode = %t", first, second, engine.FastModeEnabled())
	}
	if resolver.publishCount != 1 {
		t.Fatalf("session status publish count = %d, want 1", resolver.publishCount)
	}
}

type sequenceRuntimeActivityResolver struct {
	snapshots []runtimeactivity.ResponseSnapshot
	calls     int
}

func (r *sequenceRuntimeActivityResolver) RuntimeReadModelSnapshot(context.Context, string) (runtimeactivity.ResponseSnapshot, error) {
	if r.calls >= len(r.snapshots) {
		return r.snapshots[len(r.snapshots)-1], nil
	}
	snapshot := r.snapshots[r.calls]
	r.calls++
	return snapshot, nil
}

func TestServiceInterruptRetryRejectsReadModelLivenessWithoutRuntime(t *testing.T) {
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
		},
		{
			Version: idleVersion,
			Activity: clientui.RuntimeActivity{
				State:          clientui.RuntimeActivityRegisteredIdle,
				QueueAccepting: true,
			},
		},
	}}
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).WithRuntimeActivityResolver(resolver)
	req := serverapi.RuntimeInterruptRequest{ClientRequestID: "interrupt-retry", SessionID: "018fdd67-89ab-4cde-8123-456789abcdef"}

	first, err := service.Interrupt(context.Background(), req)
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("Interrupt first error = %v, want not accepted", err)
	}
	second, err := service.Interrupt(context.Background(), req)
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("Interrupt retry error = %v, want not accepted", err)
	}
	if first.Activity.ActiveForControl() || second.Activity.ActiveForControl() {
		t.Fatalf("rejected Interrupt activities = %+v/%+v, want zero values", first.Activity, second.Activity)
	}
	if resolver.calls != 0 {
		t.Fatalf("snapshot calls = %d, want exact authority rejection before read-model composition", resolver.calls)
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

func TestServiceRejectedCompactionHasNoVisibleStatus(t *testing.T) {
	store, engine, _, _ := newRuntimeControlCompactionFixture(t)
	if _, err := engine.CompactContextWithAcceptance(context.Background(), "", func(func() (bool, error)) (bool, error) {
		return false, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CompactContextWithAcceptance error = %v, want context canceled", err)
	}
	if got := len(localEntryEvents(t, store)); got != 0 {
		t.Fatalf("rejected compaction visible failure entries = %d, want 0", got)
	}
}

type acceptedErrorRuntimeControlClient struct {
	runtimeControlFakeClient
	err error
}

func (c *acceptedErrorRuntimeControlClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return llm.Response{}, c.err
}

func TestServiceSubmitUserTurnReplaysAcceptedError(t *testing.T) {
	acceptedErr := &llm.APIStatusError{StatusCode: 400, Body: "model failed after input acceptance"}
	client := &acceptedErrorRuntimeControlClient{err: acceptedErr}
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	request := runtimeControlUserTurnRequest(store, "accepted-error", "accepted once")

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.SubmitUserTurn(context.Background(), request); !errors.Is(err, acceptedErr) || errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
			t.Fatalf("SubmitUserTurn attempt %d error = %v, want accepted model error", attempt+1, err)
		}
	}
	if client.calls != 1 || countUserMessagesWithContent(t, store, "accepted once") != 1 {
		t.Fatalf("generate calls/user messages = %d/%d, want 1/1", client.calls, countUserMessagesWithContent(t, store, "accepted once"))
	}
}

func TestServiceSubmitUserShellCommandReplaysCommittedObserverError(t *testing.T) {
	observerErr := errors.New("shell acceptance observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
	registry := tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}})
	store, _, service := newRuntimeControlTestService(t, nil, registry, runtime.Config{}, session.WithPersistenceObserver(gate))
	if err := service.SubmitUserShellCommand(context.Background(), runtimeControlShellCommandRequest(store, "seed-shell", "true")); err != nil {
		t.Fatalf("seed shell metadata: %v", err)
	}
	request := runtimeControlShellCommandRequest(store, "accepted-shell-error", "false")
	gate.FailNext(observerErr)

	for attempt := 0; attempt < 2; attempt++ {
		if err := service.SubmitUserShellCommand(context.Background(), request); !errors.Is(err, observerErr) || errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
			t.Fatalf("SubmitUserShellCommand attempt %d error = %v, want committed observer error", attempt+1, err)
		}
	}
	if got := countDirectShellCommandMessages(t, store, "false"); got != 1 {
		t.Fatalf("direct shell message count = %d, want 1", got)
	}
}

func TestServiceCompactionConsumesCommittedObserverError(t *testing.T) {
	observerErr := errors.New("history replacement observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
	store, _, client, service := newRuntimeControlCompactionFixture(t, session.WithPersistenceObserver(gate))
	request := serverapi.RuntimeCompactContextRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       store.Meta().SessionID,
		Args:            "compact now",
	}
	gate.FailNext(observerErr)
	if err := service.CompactContext(context.Background(), request); !errors.Is(err, observerErr) {
		t.Fatalf("first compaction error = %v, want observer error", err)
	}
	if err := service.CompactContext(context.Background(), request); !errors.Is(err, observerErr) {
		t.Fatalf("replayed compaction error = %v, want cached observer error", err)
	}
	if client.compactionCalls != 1 {
		t.Fatalf("compaction call count = %d, want 1", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history_replaced event count = %d, want 1", got)
	}
}

func TestServiceSubmitUserTurnContinuesAfterAcceptedPreSubmitCompactionError(t *testing.T) {
	observerErr := errors.New("pre-submit history replacement observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeControlTestSessionPersistence)
	store, engine, client, service := newRuntimeControlCompactionFixture(t, session.WithPersistenceObserver(gate))
	if shouldCompact, compactErr := engine.ShouldCompactBeforeUserMessage(context.Background(), "after compaction"); !shouldCompact || compactErr != nil {
		t.Fatalf("pre-submit compaction precondition = (%t, %v), usage=%+v", shouldCompact, compactErr, engine.ContextUsage())
	}
	request := runtimeControlUserTurnRequest(store, "parent-compaction", "after compaction")
	gate.FailNext(observerErr)
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := service.SubmitUserTurn(context.Background(), request)
		if !errors.Is(err, observerErr) || errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) ||
			!resp.Compacted || resp.Message == nil || *resp.Message != "done" {
			t.Fatalf("SubmitUserTurn attempt %d = (%+v, %v), want accepted compacted response and observer error", attempt+1, resp, err)
		}
	}
	if client.compactionCalls != 1 ||
		countEventsByKind(t, store, "history_replaced") != 1 ||
		countUserMessagesWithContent(t, store, "after compaction") != 1 {
		t.Fatalf("pre-submit compaction calls/events/submits = %d/%d/%d, want 1/1/1",
			client.compactionCalls,
			countEventsByKind(t, store, "history_replaced"),
			countUserMessagesWithContent(t, store, "after compaction"))
	}
}

func newRuntimeControlCompactionFixture(t *testing.T, options ...session.StoreOption) (*session.Store, *runtime.Engine, *runtimeControlFakeClient, *Service) {
	t.Helper()
	trimmed := 1
	client := &runtimeControlFakeClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{InputTokens: 330000, WindowTokens: 372000}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 1000}},
		},
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
		text, _ := textutil.OptionalValue(entryRecord.Text)
		entries = append(entries, runtime.ChatEntry{
			Role: entryRecord.Role,
			Text: text,
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

func TestServiceDiscardQueuedUserMessageDedupesSuccessfulRetry(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	firstQueued := mustQueueRuntimeControlMessage(t, engine, "same")
	otherQueued := mustQueueRuntimeControlMessage(t, engine, "other")
	duplicateQueued := mustQueueRuntimeControlMessage(t, engine, "same")
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
