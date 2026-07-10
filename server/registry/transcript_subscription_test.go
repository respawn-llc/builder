package registry

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimecontrol"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestTranscriptSubscriptionBrokerSequencesEachSubscriberFromHydration(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	first, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
	if err != nil {
		t.Fatalf("Subscribe first: %v", err)
	}
	defer func() { _ = first.Close() }()

	firstHydration := nextTranscriptMessage(t, first)
	if firstHydration.Sequence != 1 || firstHydration.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("first hydration = %+v, want seq=1 hydration", firstHydration)
	}

	broker.Publish([]clientui.TranscriptMessage{
		{Kind: clientui.TranscriptMessageRunState, RunState: &clientui.RunState{}},
		{Kind: clientui.TranscriptMessageRuntimeActivity, RuntimeActivity: &clientui.RuntimeActivity{}},
	})
	if got := nextTranscriptMessage(t, first); got.Sequence != 2 || got.Kind != clientui.TranscriptMessageRunState {
		t.Fatalf("first live one = %+v, want seq=2 run_state", got)
	}
	if got := nextTranscriptMessage(t, first); got.Sequence != 3 || got.Kind != clientui.TranscriptMessageRuntimeActivity {
		t.Fatalf("first live two = %+v, want seq=3 runtime_activity", got)
	}

	second, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
	if err != nil {
		t.Fatalf("Subscribe second: %v", err)
	}
	defer func() { _ = second.Close() }()
	secondHydration := nextTranscriptMessage(t, second)
	if secondHydration.Sequence != 1 || secondHydration.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("second hydration = %+v, want fresh seq=1 hydration", secondHydration)
	}
	if event, err := nextTranscriptMessageTimeout(second, 20*time.Millisecond); err == nil {
		t.Fatalf("second subscriber replayed old live event: %+v", event)
	}
}

func TestTranscriptSubscriptionBrokerCloseAndOverflowUseTerminalErrors(t *testing.T) {
	t.Run("broker close", func(t *testing.T) {
		broker := newTranscriptSubscriptionBroker()
		sub, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		_ = nextTranscriptMessage(t, sub)
		broker.Close(io.EOF)
		_, err = sub.Next(context.Background())
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Next after close error = %v, want EOF", err)
		}
	})

	t.Run("subscriber overflow", func(t *testing.T) {
		broker := newTranscriptSubscriptionBroker()
		sub, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		for i := 0; i < sessionActivityBufferSize+1; i++ {
			broker.Publish([]clientui.TranscriptMessage{{Kind: clientui.TranscriptMessageRunState, RunState: &clientui.RunState{}}})
		}
		for {
			_, err = sub.Next(context.Background())
			if err == nil {
				continue
			}
			if !errors.Is(err, serverapi.ErrStreamGap) {
				t.Fatalf("overflow error = %v, want stream gap", err)
			}
			reason, ok := serverapi.TranscriptCloseReasonOf(err)
			if !ok || reason != serverapi.TranscriptCloseReasonSubscriberOverflow {
				t.Fatalf("overflow close reason = %q ok=%t, want subscriber overflow", reason, ok)
			}
			return
		}
	})
}

func TestTranscriptSubscriptionBrokerPanicsOnContractViolationInTestMode(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("contract-invalid tool completion did not panic in test mode")
		}
	}()
	broker.Publish([]clientui.TranscriptMessage{{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{ToolCallID: ""},
		},
	}})
}

func TestTranscriptSubscriptionBrokerClosesOnContractViolationWhenPanicDisabled(t *testing.T) {
	restore := withTranscriptContractViolationPanic(false)
	defer restore()

	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)

	broker.Publish([]clientui.TranscriptMessage{{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{ToolCallID: ""},
		},
	}})
	_, err = sub.Next(context.Background())
	if err == nil {
		t.Fatal("contract-invalid tool completion was delivered")
	}
	reason, ok := serverapi.TranscriptCloseReasonOf(err)
	if !ok || reason != serverapi.TranscriptCloseReasonContractViolation {
		t.Fatalf("contract violation reason = %q ok=%t, want contract_violation; err=%v", reason, ok, err)
	}
}

func TestTranscriptSubscriptionBrokerRejectsCommittedRowKindPayloadMismatch(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("committed row with mismatched kind/payload did not panic in test mode")
		}
	}()
	broker.Publish([]clientui.TranscriptMessage{{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowUser,
			Tool:       &clientui.TranscriptToolRow{ToolCallID: "mismatched"},
		},
	}})
}

func TestTranscriptSubscriptionBoundaryValidatesCommittedRowIntegrity(t *testing.T) {
	valid := clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowUser,
		User:       &clientui.TranscriptUserRow{Text: "valid"},
	}
	if err := validateCommittedRow(valid); err != nil {
		t.Fatalf("zero-value valid integrity was rejected: %v", err)
	}

	invalidRows := []clientui.TranscriptCommittedRow{valid, valid}
	invalidRows[0].Integrity = transcript.RowIntegrity(255)
	invalidRows[1].Visibility = clientui.EntryVisibilityAuto
	for _, invalid := range invalidRows {
		if err := clientui.ValidateTranscriptCommittedRow(invalid); err == nil {
			t.Fatalf("shared committed-row validator accepted invalid row: %#v", invalid)
		}
		err := validateCommittedRow(invalid)
		if err == nil {
			t.Fatalf("invalid committed row was accepted: %#v", invalid)
		}
		var violation transcriptContractViolation
		if !errors.As(err, &violation) {
			t.Fatalf("broker validation error = %T, want transcript contract violation", err)
		}
	}
}

func TestTranscriptSubscriptionBrokerDeliversMalformedToolRowsWithoutCallID(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)

	for _, integrity := range []transcript.RowIntegrity{
		transcript.RowIntegrityRecoverableMalformed,
		transcript.RowIntegrityUnrecoverableMalformed,
	} {
		broker.Publish([]clientui.TranscriptMessage{{
			Kind: clientui.TranscriptMessageCommittedRow,
			CommittedRow: &clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityDetail,
				Integrity:  integrity,
				Kind:       clientui.TranscriptRowTool,
				Tool:       &clientui.TranscriptToolRow{},
			},
		}})
		message := nextTranscriptMessage(t, sub)
		if message.CommittedRow == nil || message.CommittedRow.Integrity != integrity || message.CommittedRow.Tool == nil {
			t.Fatalf("malformed tool row delivery = %#v", message)
		}
	}
}

func TestSessionTranscriptSubscriptionHydratesFirstAndSequencesPerSubscription(t *testing.T) {
	registry := NewRuntimeRegistry()
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEvent(engine.SessionID(), evt)
	})
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	if err := engine.AppendCommittedEntry("system", "before subscribe"); err != nil {
		t.Fatalf("AppendCommittedEntry before subscribe: %v", err)
	}

	first := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = first.Close() }()
	firstHydration := nextTranscriptMessage(t, first)
	if firstHydration.Sequence != 1 || firstHydration.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("first message = %+v, want seq=1 hydration", firstHydration)
	}
	if firstHydration.Hydration == nil || len(firstHydration.Hydration.CommittedRows) != 1 {
		t.Fatalf("hydration rows = %+v, want one committed row", firstHydration.Hydration)
	}

	second := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = second.Close() }()
	secondHydration := nextTranscriptMessage(t, second)
	if secondHydration.Sequence != 1 || secondHydration.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("second subscription first message = %+v, want fresh seq=1 hydration", secondHydration)
	}

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		Message:                    llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "after subscribe"},
		CommittedTranscriptChanged: true,
	})
	live := nextTranscriptMessageOfKind(t, first, clientui.TranscriptMessageCommittedRow)
	if live.Kind != clientui.TranscriptMessageCommittedRow {
		t.Fatalf("live message = %+v, want first post-hydration committed row", live)
	}
	if live.CommittedRow == nil || live.CommittedRow.Assistant == nil || live.CommittedRow.Assistant.Text != "after subscribe" {
		t.Fatalf("live committed row = %+v, want appended assistant row", live.CommittedRow)
	}
}

func TestSessionTranscriptSessionIdentityHydratesAndPublishesExecutionTarget(t *testing.T) {
	target := clientui.SessionExecutionTarget{WorkspaceID: "workspace-1", WorkspaceRoot: "/workspace", EffectiveWorkdir: "/workspace"}
	registry := NewRuntimeRegistry().WithExecutionTargetResolver(func(context.Context, string) (clientui.SessionExecutionTarget, error) {
		return target, nil
	})
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Hydration == nil || !clientui.SessionExecutionTargetsEqual(hydration.Hydration.SessionIdentity.ExecutionTarget, target) {
		t.Fatalf("hydration execution target = %+v, want %+v", hydration.Hydration.SessionIdentity.ExecutionTarget, target)
	}

	nextTarget := clientui.SessionExecutionTarget{WorkspaceID: "workspace-1", WorkspaceRoot: "/workspace", Worktree: &clientui.SessionExecutionWorktreeTarget{ID: "worktree-1", Root: "/workspace/wt"}, EffectiveWorkdir: "/workspace/wt"}
	registry.PublishSessionIdentity(engine.SessionID(), &nextTarget)
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageSessionIdentity || live.SessionIdentity == nil {
		t.Fatalf("live identity = %+v, want seq=2 session_identity", live)
	}
	if !clientui.SessionExecutionTargetsEqual(live.SessionIdentity.ExecutionTarget, nextTarget) {
		t.Fatalf("live execution target = %+v, want %+v", live.SessionIdentity.ExecutionTarget, nextTarget)
	}
}

func TestSessionTranscriptFeedSequencerHydratesRuntimeActivityAndPublishesLiveAfterHydration(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1}
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	registry.PublishRuntimeActivitySnapshot(engine.SessionID(), runtimeactivity.ResponseSnapshot{
		Version:             version,
		Activity:            activity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
	})

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Hydration == nil || hydration.Hydration.RuntimeActivity == nil || *hydration.Hydration.RuntimeActivity != activity {
		t.Fatalf("hydration runtime activity = %+v, want %+v", hydration, activity)
	}
	if hydration.Hydration.InputReconciliation == nil || hydration.Hydration.InputReconciliation.Version != version {
		t.Fatalf("hydration reconciliation = %+v, want version %+v", hydration.Hydration.InputReconciliation, version)
	}

	nextVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2}
	nextActivity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{ActiveKind: clientui.RuntimeActivityActiveKindUserTurn, RunID: "run-1", StepID: "step-1"})
	registry.PublishRuntimeActivitySnapshot(engine.SessionID(), runtimeactivity.ResponseSnapshot{
		Version:             nextVersion,
		Activity:            nextActivity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(nextVersion),
	})

	liveActivity := nextTranscriptMessage(t, sub)
	if liveActivity.Sequence != 2 || liveActivity.Kind != clientui.TranscriptMessageRuntimeActivity || liveActivity.RuntimeActivity == nil || *liveActivity.RuntimeActivity != nextActivity {
		t.Fatalf("live runtime activity = %+v, want seq=2 %+v", liveActivity, nextActivity)
	}
	liveReconciliation := nextTranscriptMessage(t, sub)
	if liveReconciliation.Sequence != 3 || liveReconciliation.Kind != clientui.TranscriptMessageInputReconciliation || liveReconciliation.InputReconciliation == nil || liveReconciliation.InputReconciliation.Version != nextVersion {
		t.Fatalf("live reconciliation = %+v, want seq=3 version %+v", liveReconciliation, nextVersion)
	}
}

func TestSessionTranscriptFeedSequencerHydratesQueuedStatusAndPublishesLiveAfterHydration(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind: runtime.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
			SessionID:       engine.SessionID(),
			QueueItemID:     "queue-1",
			ClientRequestID: "client-1",
			Status:          runtime.QueuedUserMessageAccepted,
			RestoreText:     "queued text",
		},
	})

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Hydration == nil || len(hydration.Hydration.QueuedOrSteeredMessages) != 1 {
		t.Fatalf("hydration queued state = %+v, want one queued message", hydration)
	}
	if hydration.Hydration.QueuedOrSteeredMessages[0].QueueItemID != "queue-1" || hydration.Hydration.QueuedOrSteeredMessages[0].Status != clientui.QueuedUserMessageAccepted {
		t.Fatalf("hydration queued message = %+v, want queue-1 accepted", hydration.Hydration.QueuedOrSteeredMessages[0])
	}

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind: runtime.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
			SessionID:       engine.SessionID(),
			QueueItemID:     "queue-1",
			ClientRequestID: "client-1",
			Status:          runtime.QueuedUserMessageDiscarded,
		},
	})
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageQueuedOrSteeredMessageState || live.QueuedOrSteeredMessageState == nil || live.QueuedOrSteeredMessageState.Status != clientui.QueuedUserMessageDiscarded {
		t.Fatalf("live queued state = %+v, want seq=2 discarded", live)
	}
}

func TestSessionTranscriptFeedSequencerReceivesEngineQueueStatus(t *testing.T) {
	registry := NewRuntimeRegistry()
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEvent(engine.SessionID(), evt)
	})
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	item := engine.QueueUserMessage("queued through engine")
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageQueuedOrSteeredMessageState || live.QueuedOrSteeredMessageState == nil || live.QueuedOrSteeredMessageState.QueueItemID != item.ID || live.QueuedOrSteeredMessageState.Status != clientui.QueuedUserMessageAccepted {
		t.Fatalf("engine queue live state = %+v, want accepted queue item %q", live, item.ID)
	}
	if live.QueuedOrSteeredMessageState.UserText != "queued through engine" {
		t.Fatalf("accepted queue text = %q, want queued through engine", live.QueuedOrSteeredMessageState.UserText)
	}

	if !engine.DiscardQueuedUserMessage(item.ID) {
		t.Fatalf("DiscardQueuedUserMessage(%q) returned false", item.ID)
	}
	discarded := nextTranscriptMessage(t, sub)
	if discarded.Sequence != 3 || discarded.Kind != clientui.TranscriptMessageQueuedOrSteeredMessageState || discarded.QueuedOrSteeredMessageState == nil || discarded.QueuedOrSteeredMessageState.Status != clientui.QueuedUserMessageDiscarded {
		t.Fatalf("engine discard live state = %+v, want seq=3 discarded", discarded)
	}
}

func TestSessionTranscriptFeedSequencerHydratesEngineQueuedTextInFIFOOrder(t *testing.T) {
	registry := NewRuntimeRegistry()
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEvent(engine.SessionID(), evt)
	})
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	items := []runtime.QueuedUserMessage{
		engine.QueueUserMessage("first queued for hydration"),
		engine.QueueUserMessage("second queued for hydration"),
		engine.QueueUserMessage("third queued for hydration"),
		engine.QueueUserMessage("fourth queued for hydration"),
	}
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()

	hydration := nextTranscriptMessage(t, sub)
	if hydration.Hydration == nil || len(hydration.Hydration.QueuedOrSteeredMessages) != len(items) {
		t.Fatalf("hydration = %+v, want %d queued messages", hydration, len(items))
	}
	for index, item := range items {
		queued := hydration.Hydration.QueuedOrSteeredMessages[index]
		if queued.QueueItemID != item.ID || queued.UserText != item.Text {
			t.Fatalf("hydrated queued message %d = %+v, want FIFO item %+v", index, queued, item)
		}
	}
}

func TestQueuedMessageStateLedgerPreservesFIFOAcrossIdentityBridgeAndRemoval(t *testing.T) {
	firstClientID := uuid.NewString()
	secondClientID := uuid.NewString()
	secondQueueID := uuid.NewString()
	thirdClientID := uuid.NewString()
	ledger := queuedMessageStateLedger{}

	ledger.apply(clientui.TranscriptQueuedOrSteeredMessageState{
		ClientRequestID: firstClientID,
		Status:          clientui.QueuedUserMessageAccepted,
		UserText:        "first",
	})
	ledger.apply(clientui.TranscriptQueuedOrSteeredMessageState{
		ClientRequestID: secondClientID,
		Status:          clientui.QueuedUserMessageAccepted,
		UserText:        "second pending identity",
	})
	ledger.apply(clientui.TranscriptQueuedOrSteeredMessageState{
		ClientRequestID: thirdClientID,
		Status:          clientui.QueuedUserMessageAccepted,
		UserText:        "third",
	})
	ledger.apply(clientui.TranscriptQueuedOrSteeredMessageState{
		QueueItemID:     secondQueueID,
		ClientRequestID: secondClientID,
		Status:          clientui.QueuedUserMessageAccepted,
		UserText:        "second authoritative identity",
	})
	ledger.apply(clientui.TranscriptQueuedOrSteeredMessageState{
		ClientRequestID: firstClientID,
		Status:          clientui.QueuedUserMessageDiscarded,
	})

	got := ledger.values()
	if len(got) != 2 {
		t.Fatalf("ledger values = %+v, want second and third", got)
	}
	if got[0].QueueItemID != secondQueueID || got[0].UserText != "second authoritative identity" || got[1].ClientRequestID != thirdClientID {
		t.Fatalf("ledger values = %+v, want updated second followed by third", got)
	}

	ledger.apply(clientui.TranscriptQueuedOrSteeredMessageState{
		QueueItemID: secondQueueID,
		Status:      clientui.QueuedUserMessageDiscarded,
	})
	got = ledger.values()
	if len(got) != 1 || got[0].ClientRequestID != thirdClientID {
		t.Fatalf("ledger after authoritative removal = %+v, want third only", got)
	}
}

func TestSessionTranscriptFeedPublishesUpdatedToolStartMetadataByCallID(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind: runtime.EventToolCallStarted,
		ToolCall: &llm.ToolCall{
			ID:   "call-duplicate",
			Name: "shell",
		},
	})
	start := nextTranscriptMessage(t, sub)
	if start.Kind != clientui.TranscriptMessageToolStart || start.ToolStart == nil || start.ToolStart.ToolCallID != "call-duplicate" {
		t.Fatalf("first start = %+v, want tool_start", start)
	}

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind: runtime.EventToolCallStarted,
		ToolCall: &llm.ToolCall{
			ID:   "call-duplicate",
			Name: "exec",
		},
	})
	updated := nextTranscriptMessage(t, sub)
	if updated.Kind != clientui.TranscriptMessageToolStart || updated.ToolStart == nil || updated.ToolStart.ToolCallID != "call-duplicate" || updated.ToolStart.ToolName != "exec" {
		t.Fatalf("updated start = %+v, want normalized tool_start metadata", updated)
	}

	late := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = late.Close() }()
	hydration := nextTranscriptMessage(t, late)
	if hydration.Hydration == nil || len(hydration.Hydration.InFlightTools) != 1 {
		t.Fatalf("late hydration = %+v, want one in-flight tool", hydration)
	}
	if got := hydration.Hydration.InFlightTools[0].ToolName; got != "exec" {
		t.Fatalf("hydrated tool name = %q, want updated duplicate start metadata", got)
	}
}

func TestSessionTranscriptSubscriberCountsAsRuntimeInterest(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	if registry.HasRuntimeSubscribers(engine.SessionID()) {
		t.Fatal("runtime subscribers present before transcript subscribe")
	}
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	if !registry.HasRuntimeSubscribers(engine.SessionID()) {
		t.Fatal("transcript subscriber did not count as runtime interest")
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close transcript subscription: %v", err)
	}
	if registry.HasRuntimeSubscribers(engine.SessionID()) {
		t.Fatal("runtime subscribers present after transcript close")
	}
}

func TestSessionTranscriptFeedSequencerHydratesRunStateAndPublishesLiveAfterHydration(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind: runtime.EventRunStateChanged,
		RunState: &runtime.RunState{
			Lifecycle: runtime.IdleRunLifecycle(),
			Status:    runtime.RunStatusCompleted,
		},
	})
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Hydration == nil || hydration.Hydration.RunState == nil || hydration.Hydration.RunState.Status != clientui.RunStatusCompleted {
		t.Fatalf("hydration run state = %+v, want completed", hydration)
	}

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind: runtime.EventRunStateChanged,
		RunState: &runtime.RunState{
			Lifecycle: runtime.RunLifecycle{Phase: runtime.RunLifecycleRunning, Mode: runtime.RunModeTurn},
			Status:    runtime.RunStatusRunning,
		},
	})
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageRunState || live.RunState == nil || live.RunState.Status != clientui.RunStatusRunning {
		t.Fatalf("live run state = %+v, want seq=2 running", live)
	}
}

func TestSessionTranscriptFeedSequencerUsesRuntimeViewStatusProducer(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Hydration == nil || hydration.Hydration.SessionStatus.ReviewerFrequency != engine.ReviewerFrequency() {
		t.Fatalf("hydration session status = %+v, want runtimeview-projected status", hydration)
	}

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind:         runtime.EventConversationUpdated,
		ContextUsage: &runtime.ContextUsage{UsedTokens: 10, WindowTokens: 100},
	})
	live := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageSessionStatus)
	if live.Kind != clientui.TranscriptMessageSessionStatus || live.SessionStatus == nil {
		t.Fatalf("live session status = %+v, want session_status", live)
	}
}

func TestSessionTranscriptFeedPublishesStatusAfterRuntimeSettingChange(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	service := runtimecontrol.NewService(registry)
	_, err := service.SetQuestionsEnabled(context.Background(), serverapi.RuntimeSetQuestionsEnabledRequest{
		SessionID:       engine.SessionID(),
		ClientRequestID: "questions-disable-1",
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetQuestionsEnabled: %v", err)
	}

	live := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageSessionStatus)
	if live.SessionStatus == nil || live.SessionStatus.QuestionsEnabled {
		t.Fatalf("live session status = %+v, want questions disabled", live)
	}
}

func TestSessionTranscriptFeedSequencerHydratesPendingPromptAndPublishesResolution(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	registry.BeginPendingPrompt(engine.SessionID(), tools.AskQuestionRequest{
		ID:       "ask-1",
		Question: "approve?",
		Approval: true,
	})

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Hydration == nil || len(hydration.Hydration.PendingSessionPrompts) != 1 {
		t.Fatalf("hydration pending prompts = %+v, want one prompt", hydration)
	}
	prompt := hydration.Hydration.PendingSessionPrompts[0]
	if prompt.ID != "ask-1" || prompt.Kind != clientui.TranscriptPromptApproval || prompt.State != clientui.TranscriptPromptPending {
		t.Fatalf("hydration prompt = %+v, want pending approval ask-1", prompt)
	}

	registry.CompletePendingPrompt(engine.SessionID(), "ask-1")
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessagePendingSessionPrompt || live.PendingSessionPrompt == nil || live.PendingSessionPrompt.State != clientui.TranscriptPromptResolved {
		t.Fatalf("live prompt = %+v, want seq=2 resolved prompt", live)
	}
}

func TestSessionTranscriptSubscriptionCarriesAssistantStreamIdentity(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	streamID := uuid.MustParse("f84c7d21-4c94-4a54-87fd-b41f5bd01d38")
	metadata := &runtime.AssistantStreamMetadata{StepID: "step-1"}
	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind:                        runtime.EventAssistantDelta,
		StepID:                      "step-1",
		AssistantDelta:              "hello",
		AssistantDeltaPhase:         llm.MessagePhaseFinal,
		AssistantStreamMetadata:     metadata,
		AssistantTranscriptStreamID: &streamID,
	})
	delta := nextTranscriptMessage(t, sub)
	if delta.Sequence != 2 || delta.Kind != clientui.TranscriptMessageAssistantDelta {
		t.Fatalf("delta message = %+v, want seq=2 assistant delta", delta)
	}
	if delta.AssistantDelta == nil || delta.AssistantDelta.StreamID != streamID {
		t.Fatalf("delta identity = %+v, want stream %q", delta.AssistantDelta, streamID)
	}

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind:                        runtime.EventAssistantMessage,
		StepID:                      "step-1",
		Message:                     llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "hello"},
		AssistantStreamMetadata:     metadata,
		AssistantTranscriptStreamID: &streamID,
		CommittedTranscriptChanged:  true,
	})
	committed := nextTranscriptMessage(t, sub)
	if committed.Sequence != 3 || committed.Kind != clientui.TranscriptMessageCommittedRow {
		t.Fatalf("committed message = %+v, want seq=3 committed row", committed)
	}
	if committed.CommittedRow == nil || committed.CommittedRow.Assistant == nil || committed.CommittedRow.Assistant.StreamID == nil || *committed.CommittedRow.Assistant.StreamID != streamID {
		t.Fatalf("committed assistant identity = %+v, want stream %q", committed.CommittedRow, streamID)
	}
}

func TestSessionTranscriptSubscriptionEmitsToolCompletionsInServerOrder(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	publishToolStartForTest(registry, engine.SessionID(), "call-b")
	publishToolStartForTest(registry, engine.SessionID(), "call-a")
	publishToolCompletionForTest(registry, engine.SessionID(), "call-b")
	publishToolCompletionForTest(registry, engine.SessionID(), "call-a")

	first := nextTranscriptMessage(t, sub)
	second := nextTranscriptMessage(t, sub)
	third := nextTranscriptMessage(t, sub)
	fourth := nextTranscriptMessage(t, sub)
	if first.Sequence != 2 || second.Sequence != 3 || third.Sequence != 4 || fourth.Sequence != 5 {
		t.Fatalf("tool message sequences = %d,%d,%d,%d; want 2..5", first.Sequence, second.Sequence, third.Sequence, fourth.Sequence)
	}
	if first.Kind != clientui.TranscriptMessageToolStart || first.ToolStart == nil || first.ToolStart.ToolCallID != "call-b" {
		t.Fatalf("first tool message = %+v, want call-b start", first)
	}
	if second.Kind != clientui.TranscriptMessageToolStart || second.ToolStart == nil || second.ToolStart.ToolCallID != "call-a" {
		t.Fatalf("second tool message = %+v, want call-a start", second)
	}
	if third.CommittedRow == nil || third.CommittedRow.Kind != clientui.TranscriptRowTool || third.CommittedRow.Tool == nil || third.CommittedRow.Tool.ToolCallID != "call-b" {
		t.Fatalf("third message = %+v, want call-b tool row", third)
	}
	if fourth.CommittedRow == nil || fourth.CommittedRow.Kind != clientui.TranscriptRowTool || fourth.CommittedRow.Tool == nil || fourth.CommittedRow.Tool.ToolCallID != "call-a" {
		t.Fatalf("fourth message = %+v, want call-a tool row", fourth)
	}
}

func TestSessionTranscriptHydratesRuntimeLedgerInFlightToolAndCompletionTerminalsIt(t *testing.T) {
	registry := NewRuntimeRegistry()
	handler := &blockingToolHandler{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	store, err := session.Create(t.TempDir(), "workspace", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	var engine *runtime.Engine
	engine, err = runtime.New(store, registryRuntimeFakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: handler}), runtime.Config{
		Model: "gpt-5",
		OnEvent: func(evt runtime.Event) {
			registry.PublishRuntimeEvent(engine.SessionID(), evt)
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserShellCommand(context.Background(), "pwd")
		done <- err
	}()
	select {
	case <-handler.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool handler to start")
	}

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Hydration == nil || len(hydration.Hydration.InFlightTools) != 1 {
		t.Fatalf("hydration in-flight tools = %+v, want one ledger tool", hydration)
	}
	inFlight := hydration.Hydration.InFlightTools[0]
	if inFlight.ToolCallID == "" || inFlight.ToolName != string(toolspec.ToolExecCommand) {
		t.Fatalf("hydrated in-flight tool = %+v, want provider call id and exec_command", inFlight)
	}

	close(handler.release)
	if err := <-done; err != nil {
		t.Fatalf("SubmitUserShellCommand: %v", err)
	}
	completion := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageCommittedRow)
	if completion.Kind != clientui.TranscriptMessageCommittedRow || completion.CommittedRow == nil || completion.CommittedRow.Tool == nil {
		t.Fatalf("completion message = %+v, want committed tool row", completion)
	}
	if completion.CommittedRow.Tool.ToolCallID != inFlight.ToolCallID {
		t.Fatalf("completion call id = %q, want hydrated call id %q", completion.CommittedRow.Tool.ToolCallID, inFlight.ToolCallID)
	}
}

func TestSessionTranscriptToolAbortTerminalsVisibleStart(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	publishToolStartForTest(registry, engine.SessionID(), "call-abort")
	start := nextTranscriptMessage(t, sub)
	if start.Sequence != 2 || start.Kind != clientui.TranscriptMessageToolStart || start.ToolStart == nil || start.ToolStart.ToolCallID != "call-abort" {
		t.Fatalf("tool start = %+v, want visible start", start)
	}

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind:            runtime.EventToolCallAborted,
		StepID:          "step-1",
		ToolCall:        &llm.ToolCall{ID: "call-abort", Name: string(toolspec.ToolExecCommand)},
		ToolAbortReason: string(clientui.TranscriptToolAbortCanceled),
	})
	abort := nextTranscriptMessage(t, sub)
	if abort.Sequence != 3 || abort.Kind != clientui.TranscriptMessageToolAbort || abort.ToolAbort == nil || abort.ToolAbort.ToolCallID != "call-abort" {
		t.Fatalf("tool abort = %+v, want terminal abort", abort)
	}
}

type blockingToolHandler struct {
	entered chan struct{}
	release chan struct{}
}

func (h *blockingToolHandler) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	close(h.entered)
	select {
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	case <-h.release:
		return tools.Result{
			CallID: call.ID,
			Name:   call.Name,
			Output: []byte(`{"output":"done"}`),
		}, nil
	}
}

func subscribeTranscriptForTest(t *testing.T, registry *RuntimeRegistry, sessionID string) serverapi.SessionTranscriptSubscription {
	t.Helper()
	sub, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.SessionTranscriptSubscribeRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	return sub
}

func nextTranscriptMessage(t *testing.T, sub serverapi.SessionTranscriptSubscription) clientui.TranscriptSubscriptionMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("transcript subscription next: %v", err)
	}
	return message
}

func nextTranscriptMessageOfKind(t *testing.T, sub serverapi.SessionTranscriptSubscription, kind clientui.TranscriptMessageKind) clientui.TranscriptSubscriptionMessage {
	t.Helper()
	for i := 0; i < 8; i++ {
		message := nextTranscriptMessage(t, sub)
		if message.Kind == kind {
			return message
		}
	}
	t.Fatalf("did not receive transcript message kind %q", kind)
	return clientui.TranscriptSubscriptionMessage{}
}

func nextTranscriptMessageTimeout(sub serverapi.SessionTranscriptSubscription, timeout time.Duration) (clientui.TranscriptSubscriptionMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return sub.Next(ctx)
}

func publishToolCompletionForTest(registry *RuntimeRegistry, sessionID string, callID string) {
	registry.PublishRuntimeEvent(sessionID, runtime.Event{
		Kind:   runtime.EventToolCallCompleted,
		StepID: "step-1",
		ToolResult: &tools.Result{
			CallID: callID,
			Name:   toolspec.ToolExecCommand,
			Output: []byte(`{"output":"done"}`),
		},
		CommittedTranscriptChanged: true,
	})
}

func publishToolStartForTest(registry *RuntimeRegistry, sessionID string, callID string) {
	registry.PublishRuntimeEvent(sessionID, runtime.Event{
		Kind:   runtime.EventToolCallStarted,
		StepID: "step-1",
		ToolCall: &llm.ToolCall{
			ID:   callID,
			Name: string(toolspec.ToolExecCommand),
		},
		CommittedTranscriptChanged: true,
	})
}

func TestTranscriptSubscriptionBrokerDeliversToolTerminalsWithoutStart(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(clientui.TranscriptMessage{Kind: clientui.TranscriptMessageHydration, Hydration: &clientui.TranscriptHydration{}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)

	broker.Publish([]clientui.TranscriptMessage{{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityOngoingCollapsed,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{ToolCallID: "hosted-call", ToolName: "web_search"},
		},
	}})
	row := nextTranscriptMessage(t, sub)
	if row.Kind != clientui.TranscriptMessageCommittedRow || row.CommittedRow == nil || row.CommittedRow.Tool == nil || row.CommittedRow.Tool.ToolCallID != "hosted-call" {
		t.Fatalf("message = %+v, want committed tool row without preceding start", row)
	}

	broker.Publish([]clientui.TranscriptMessage{{
		Kind:      clientui.TranscriptMessageToolAbort,
		ToolAbort: &clientui.TranscriptToolAbort{ToolCallID: "hosted-call-2", Reason: clientui.TranscriptToolAbortCanceled},
	}})
	abort := nextTranscriptMessage(t, sub)
	if abort.Kind != clientui.TranscriptMessageToolAbort || abort.ToolAbort == nil || abort.ToolAbort.ToolCallID != "hosted-call-2" {
		t.Fatalf("message = %+v, want tool abort without preceding start", abort)
	}
}

func TestSessionTranscriptFeedPublishesStatusAfterUserMessageFlush(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	registry.PublishRuntimeEvent(engine.SessionID(), runtime.Event{
		Kind:         runtime.EventUserMessageFlushed,
		UserMessage:  "steer text",
		ContextUsage: &runtime.ContextUsage{UsedTokens: 10, WindowTokens: 100},
	})
	row := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageCommittedRow)
	if row.CommittedRow == nil || row.CommittedRow.User == nil || row.CommittedRow.User.Text != "steer text" {
		t.Fatalf("committed row = %+v, want flushed user text", row.CommittedRow)
	}
	status := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageSessionStatus)
	if status.SessionStatus == nil {
		t.Fatalf("status = %+v, want session status after user flush", status)
	}
}
