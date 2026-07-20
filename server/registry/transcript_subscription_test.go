package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/invariant"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func transcriptBrokerHydration(t *testing.T) clientui.TranscriptMessage {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	version, err := clientui.NewReadModelVersion("registry-test", 1, 1)
	if err != nil {
		t.Fatalf("NewReadModelVersion: %v", err)
	}
	hydration := clientui.TranscriptHydration{
		SessionIdentity: clientui.TranscriptSessionIdentity{
			SessionID:             sessionID,
			ConversationFreshness: clientui.ConversationFreshnessFresh,
		},
		SessionStatus: clientui.TranscriptSessionStatus{
			ReviewerFrequency: "off",
			ThinkingLevel:     "medium",
			CompactionMode:    "auto",
		},
		RuntimeReadModelUpdate: clientui.RuntimeReadModelUpdate{
			Version:             version,
			Activity:            clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		},
		CommittedRows: []clientui.TranscriptCommittedRow{},
	}
	return clientui.TranscriptMessage{
		Kind:    clientui.TranscriptMessageHydration,
		Payload: clientui.TranscriptPayload{Hydration: &hydration},
	}
}

func transcriptBrokerDiagnostic(code clientui.OperationalDiagnosticCode, detail string) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageOperationalDiagnostic,
		Payload: clientui.TranscriptPayload{OperationalDiagnostic: &clientui.TranscriptOperationalDiagnostic{
			Code:   code,
			Detail: detail,
		}},
	}
}

func mustRegistryStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID(registryTestStepID)
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return stepID
}

func mustRegistryQueueItemID(t *testing.T, raw string) runtimeids.QueueItemID {
	t.Helper()
	queueItemID, err := runtimeids.ParseQueueItemID(raw)
	if err != nil {
		t.Fatalf("ParseQueueItemID: %v", err)
	}
	return queueItemID
}

func TestTranscriptSubscriptionBrokerSequencesEachSubscriberFromHydration(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	first, err := broker.Subscribe(transcriptBrokerHydration(t))
	if err != nil {
		t.Fatalf("Subscribe first: %v", err)
	}
	defer func() { _ = first.Close() }()

	firstHydration := nextTranscriptMessage(t, first)
	if firstHydration.Sequence != 1 || firstHydration.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("first hydration = %+v, want seq=1 hydration", firstHydration)
	}

	broker.Publish([]clientui.TranscriptMessage{
		transcriptBrokerDiagnostic(clientui.OperationalDiagnosticSleepGuardFailed, "sleep guard failed"),
		transcriptBrokerDiagnostic(clientui.OperationalDiagnosticPromptHistoryPersistFailed, "prompt history failed"),
	})
	if got := nextTranscriptMessage(t, first); got.Sequence != 2 || got.Kind != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("first live one = %+v, want seq=2 operational diagnostic", got)
	}
	if got := nextTranscriptMessage(t, first); got.Sequence != 3 || got.Kind != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("first live two = %+v, want seq=3 operational diagnostic", got)
	}

	second, err := broker.Subscribe(transcriptBrokerHydration(t))
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
		sub, err := broker.Subscribe(transcriptBrokerHydration(t))
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
		sub, err := broker.Subscribe(transcriptBrokerHydration(t))
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		for i := 0; i < transcriptSubscriptionBufferSize+1; i++ {
			broker.Publish([]clientui.TranscriptMessage{transcriptBrokerDiagnostic(clientui.OperationalDiagnosticSleepGuardFailed, "sleep guard failed")})
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
	sub, err := broker.Subscribe(transcriptBrokerHydration(t))
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
		Payload: clientui.TranscriptPayload{CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{StepID: mustRegistryStepID(t), ToolCallID: ""},
		}},
	}})
}

func TestTranscriptSubscriptionBrokerClosesOnContractViolationWhenPanicDisabled(t *testing.T) {
	restore := withTranscriptContractViolationPanic(false)
	defer restore()

	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(transcriptBrokerHydration(t))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)

	broker.Publish([]clientui.TranscriptMessage{{
		Kind: clientui.TranscriptMessageCommittedRow,
		Payload: clientui.TranscriptPayload{CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{StepID: mustRegistryStepID(t), ToolCallID: ""},
		}},
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
	sub, err := broker.Subscribe(transcriptBrokerHydration(t))
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
		Payload: clientui.TranscriptPayload{CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowUser,
			Tool:       &clientui.TranscriptToolRow{StepID: mustRegistryStepID(t), ToolCallID: "mismatched"},
		}},
	}})
}

func TestTranscriptSubscriptionBoundaryValidatesCommittedRowIntegrity(t *testing.T) {
	valid := clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowUser,
		User:       &clientui.TranscriptUserRow{StepID: mustRegistryStepID(t), Text: "valid"},
	}
	if err := validateCommittedRow(valid); err != nil {
		t.Fatalf("zero-value valid integrity was rejected: %v", err)
	}

	invalidRows := []clientui.TranscriptCommittedRow{valid, valid}
	invalidRows[0].Integrity = transcript.RowIntegrity(255)
	invalidRows[1].Visibility = clientui.EntryVisibilityAuto
	for _, invalid := range invalidRows {
		if err := invariant.ValidateTranscriptCommittedRow(invalid); err == nil {
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

func TestSessionTranscriptSubscriptionHydratesFirstAndSequencesPerSubscription(t *testing.T) {
	registry := NewRuntimeRegistry()
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), evt)
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
	if firstHydration.Payload.Hydration == nil || len(firstHydration.Payload.Hydration.CommittedRows) != 1 {
		t.Fatalf("hydration rows = %+v, want one committed row", firstHydration.Payload.Hydration)
	}

	second := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = second.Close() }()
	secondHydration := nextTranscriptMessage(t, second)
	if secondHydration.Sequence != 1 || secondHydration.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("second subscription first message = %+v, want fresh seq=1 hydration", secondHydration)
	}

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		StepID:                     registryTestStepID,
		Message:                    llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "after subscribe"},
		CommittedTranscriptChanged: true,
	})
	live := nextTranscriptMessageOfKind(t, first, clientui.TranscriptMessageCommittedRow)
	if live.Kind != clientui.TranscriptMessageCommittedRow {
		t.Fatalf("live message = %+v, want first post-hydration committed row", live)
	}
	if live.Payload.CommittedRow == nil || live.Payload.CommittedRow.Assistant == nil || live.Payload.CommittedRow.Assistant.Text != "after subscribe" {
		t.Fatalf("live committed row = %+v, want appended assistant row", live.Payload.CommittedRow)
	}
}

func TestSessionTranscriptSessionIdentityHydratesAndPublishesExecutionTarget(t *testing.T) {
	target := clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceRoot:         "/workspace",
		WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		CwdRelpath:            ".",
		EffectiveWorkdir:      "/workspace",
	}
	registry := NewRuntimeRegistry().WithExecutionTargetResolver(func(context.Context, string) (clientui.SessionExecutionTarget, error) {
		return target, nil
	})
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Payload.Hydration == nil || !clientui.SessionExecutionTargetsEqual(*hydration.Payload.Hydration.SessionIdentity.ExecutionTarget, target) {
		t.Fatalf("hydration execution target = %+v, want %+v", hydration.Payload.Hydration.SessionIdentity.ExecutionTarget, target)
	}

	nextTarget := clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceRoot:         "/workspace",
		WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		Worktree: &clientui.SessionExecutionWorktreeTarget{
			ID:           "worktree-1",
			Root:         "/workspace/wt",
			Availability: string(clientui.ProjectAvailabilityAvailable),
		},
		CwdRelpath:       ".",
		EffectiveWorkdir: "/workspace/wt",
	}
	registry.PublishSessionIdentity(engine.SessionID(), &nextTarget)
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageSessionIdentity || live.Payload.SessionIdentity == nil {
		t.Fatalf("live identity = %+v, want seq=2 session_identity", live)
	}
	if !clientui.SessionExecutionTargetsEqual(*live.Payload.SessionIdentity.ExecutionTarget, nextTarget) {
		t.Fatalf("live execution target = %+v, want %+v", live.Payload.SessionIdentity.ExecutionTarget, nextTarget)
	}
}

func TestSessionTranscriptFeedSequencerHydratesQueuedStatusAndPublishesLiveAfterHydration(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	queueItemID := runtimeids.NewQueueItemID()
	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind: runtime.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
			SessionID:       engine.SessionID(),
			QueueItemID:     queueItemID.String(),
			ClientRequestID: clientRequestID.String(),
			Status:          runtime.QueuedUserMessageAccepted,
			RestoreText:     "queued text",
		},
	})

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.QueuedMessages) != 1 {
		t.Fatalf("hydration queued state = %+v, want one queued message", hydration)
	}
	if hydration.Payload.Hydration.QueuedMessages[0].QueueItemID != queueItemID || hydration.Payload.Hydration.QueuedMessages[0].Status != clientui.QueuedUserMessageAccepted {
		t.Fatalf("hydration queued message = %+v, want accepted queue item %q", hydration.Payload.Hydration.QueuedMessages[0], queueItemID.String())
	}

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind: runtime.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
			SessionID:       engine.SessionID(),
			QueueItemID:     queueItemID.String(),
			ClientRequestID: clientRequestID.String(),
			Status:          runtime.QueuedUserMessageDiscarded,
		},
	})
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageQueuedMessageState || live.Payload.QueuedMessageState == nil || live.Payload.QueuedMessageState.Status != clientui.QueuedUserMessageDiscarded {
		t.Fatalf("live queued state = %+v, want seq=2 discarded", live)
	}
}

func TestSessionTranscriptFeedSequencerReceivesEngineQueueStatus(t *testing.T) {
	registry := NewRuntimeRegistry()
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), evt)
	})
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)

	item := engine.QueueUserMessageWithClientRequestID("queued through engine", runtimeids.NewRuntimeClientRequestID().String())
	queueItemID := mustRegistryQueueItemID(t, item.ID)
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageQueuedMessageState || live.Payload.QueuedMessageState == nil || live.Payload.QueuedMessageState.QueueItemID != queueItemID || live.Payload.QueuedMessageState.Status != clientui.QueuedUserMessageAccepted {
		t.Fatalf("engine queue live state = %+v, want accepted queue item %q", live, item.ID)
	}
	if live.Payload.QueuedMessageState.Text == nil || *live.Payload.QueuedMessageState.Text != "queued through engine" {
		t.Fatalf("accepted queue text = %v, want queued through engine", live.Payload.QueuedMessageState.Text)
	}

	if !engine.DiscardQueuedUserMessage(item.ID) {
		t.Fatalf("DiscardQueuedUserMessage(%q) returned false", item.ID)
	}
	discarded := nextTranscriptMessage(t, sub)
	if discarded.Sequence != 3 || discarded.Kind != clientui.TranscriptMessageQueuedMessageState || discarded.Payload.QueuedMessageState == nil || discarded.Payload.QueuedMessageState.Status != clientui.QueuedUserMessageDiscarded {
		t.Fatalf("engine discard live state = %+v, want seq=3 discarded", discarded)
	}
}

func TestSessionTranscriptFeedSequencerHydratesEngineQueuedTextInFIFOOrder(t *testing.T) {
	registry := NewRuntimeRegistry()
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), evt)
	})
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	items := []runtime.QueuedUserMessage{
		engine.QueueUserMessageWithClientRequestID("first queued for hydration", runtimeids.NewRuntimeClientRequestID().String()),
		engine.QueueUserMessageWithClientRequestID("second queued for hydration", runtimeids.NewRuntimeClientRequestID().String()),
		engine.QueueUserMessageWithClientRequestID("third queued for hydration", runtimeids.NewRuntimeClientRequestID().String()),
		engine.QueueUserMessageWithClientRequestID("fourth queued for hydration", runtimeids.NewRuntimeClientRequestID().String()),
	}
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()

	hydration := nextTranscriptMessage(t, sub)
	if hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.QueuedMessages) != len(items) {
		t.Fatalf("hydration = %+v, want %d queued messages", hydration, len(items))
	}
	for index, item := range items {
		queued := hydration.Payload.Hydration.QueuedMessages[index]
		if queued.QueueItemID != mustRegistryQueueItemID(t, item.ID) || queued.Text == nil || *queued.Text != item.Text {
			t.Fatalf("hydrated queued message %d = %+v, want FIFO item %+v", index, queued, item)
		}
	}
}

func TestQueuedMessageStateLedgerPreservesFIFOAcrossIdentityBridgeAndRemoval(t *testing.T) {
	firstClientID := runtimeids.NewRuntimeClientRequestID()
	firstQueueID := runtimeids.NewQueueItemID()
	secondClientID := runtimeids.NewRuntimeClientRequestID()
	secondQueueID := runtimeids.NewQueueItemID()
	thirdClientID := runtimeids.NewRuntimeClientRequestID()
	thirdQueueID := runtimeids.NewQueueItemID()
	ledger := queuedMessageStateLedger{}
	firstText := "first"
	secondText := "second"
	updatedSecondText := "second authoritative identity"
	thirdText := "third"

	ledger.apply(clientui.TranscriptQueuedMessageState{
		ClientRequestID: firstClientID,
		QueueItemID:     firstQueueID,
		Status:          clientui.QueuedUserMessageAccepted,
		Text:            &firstText,
	})
	ledger.apply(clientui.TranscriptQueuedMessageState{
		ClientRequestID: secondClientID,
		QueueItemID:     secondQueueID,
		Status:          clientui.QueuedUserMessageAccepted,
		Text:            &secondText,
	})
	ledger.apply(clientui.TranscriptQueuedMessageState{
		ClientRequestID: thirdClientID,
		QueueItemID:     thirdQueueID,
		Status:          clientui.QueuedUserMessageAccepted,
		Text:            &thirdText,
	})
	ledger.apply(clientui.TranscriptQueuedMessageState{
		QueueItemID:     secondQueueID,
		ClientRequestID: secondClientID,
		Status:          clientui.QueuedUserMessageAccepted,
		Text:            &updatedSecondText,
	})
	ledger.apply(clientui.TranscriptQueuedMessageState{
		QueueItemID:     firstQueueID,
		ClientRequestID: firstClientID,
		Status:          clientui.QueuedUserMessageDiscarded,
	})

	got := ledger.values()
	if len(got) != 2 {
		t.Fatalf("ledger values = %+v, want second and third", got)
	}
	if got[0].QueueItemID != secondQueueID || got[0].Text == nil || *got[0].Text != updatedSecondText || got[1].ClientRequestID != thirdClientID {
		t.Fatalf("ledger values = %+v, want updated second followed by third", got)
	}

	ledger.apply(clientui.TranscriptQueuedMessageState{
		ClientRequestID: secondClientID,
		QueueItemID:     secondQueueID,
		Status:          clientui.QueuedUserMessageDiscarded,
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

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:   runtime.EventToolCallStarted,
		StepID: registryTestStepID,
		ToolCall: &llm.ToolCall{
			ID:   "call-duplicate",
			Name: "shell",
		},
	})
	start := nextTranscriptMessage(t, sub)
	if start.Kind != clientui.TranscriptMessageToolStart || start.Payload.ToolStart == nil || start.Payload.ToolStart.ToolCallID != "call-duplicate" {
		t.Fatalf("first start = %+v, want tool_start", start)
	}

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:   runtime.EventToolCallStarted,
		StepID: registryTestStepID,
		ToolCall: &llm.ToolCall{
			ID:   "call-duplicate",
			Name: "exec",
		},
	})
	updated := nextTranscriptMessage(t, sub)
	if updated.Kind != clientui.TranscriptMessageToolStart || updated.Payload.ToolStart == nil || updated.Payload.ToolStart.ToolCallID != "call-duplicate" || updated.Payload.ToolStart.ToolName != "exec" {
		t.Fatalf("updated start = %+v, want normalized tool_start metadata", updated)
	}
	publishRunState(registry, engine.SessionID(), true)

	late := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = late.Close() }()
	hydration := nextTranscriptMessage(t, late)
	if hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.InFlightTools) != 1 {
		t.Fatalf("late hydration = %+v, want one in-flight tool", hydration)
	}
	if got := hydration.Payload.Hydration.InFlightTools[0].ToolName; got != "exec" {
		t.Fatalf("hydrated tool name = %q, want updated duplicate start metadata", got)
	}
}

func TestSessionTranscriptFeedHydratesToolsInFirstStartOrder(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	const toolCount = 16
	for index := 0; index < toolCount; index++ {
		registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
			Kind:   runtime.EventToolCallStarted,
			StepID: "22222222-2222-4222-8222-222222222222",
			ToolCall: &llm.ToolCall{
				ID:   fmt.Sprintf("call-%02d", index),
				Name: "shell",
			},
		})
	}
	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:   runtime.EventToolCallStarted,
		StepID: "22222222-2222-4222-8222-222222222222",
		ToolCall: &llm.ToolCall{
			ID:   "call-00",
			Name: "exec",
		},
	})
	publishRunState(registry, engine.SessionID(), true)

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.InFlightTools) != toolCount {
		t.Fatalf("hydration = %+v, want %d in-flight tools", hydration, toolCount)
	}
	for index, tool := range hydration.Payload.Hydration.InFlightTools {
		wantID := fmt.Sprintf("call-%02d", index)
		if tool.ToolCallID != clientui.ToolCallID(wantID) {
			t.Fatalf("hydrated tool %d id = %q, want first-start order id %q", index, tool.ToolCallID, wantID)
		}
	}
	if got := hydration.Payload.Hydration.InFlightTools[0].ToolName; got != "exec" {
		t.Fatalf("updated first tool name = %q, want exec", got)
	}
}

func TestSessionTranscriptFeedHydratesBackgroundsInFirstSeenOrder(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	const backgroundCount = 16
	activityIDs := make([]uuid.UUID, 0, backgroundCount)
	for index := 0; index < backgroundCount; index++ {
		activityID := uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1))
		activityIDs = append(activityIDs, activityID)
		registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
			Kind:   runtime.EventBackgroundUpdated,
			StepID: registryTestStepID,
			Background: &runtime.BackgroundShellEvent{
				Type:        runtime.BackgroundShellEventBackgrounded,
				ID:          fmt.Sprintf("process-%02d", index),
				ActivityID:  activityID,
				OwnerRunID:  registryTestStepID,
				OwnerStepID: registryTestStepID,
				State:       "running",
				Command:     "go test ./...",
				Workdir:     "/repo",
			},
		})
	}
	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:   runtime.EventBackgroundUpdated,
		StepID: registryTestStepID,
		Background: &runtime.BackgroundShellEvent{
			Type:        runtime.BackgroundShellEventBackgrounded,
			ID:          "process-00",
			ActivityID:  activityIDs[0],
			OwnerRunID:  registryTestStepID,
			OwnerStepID: registryTestStepID,
			State:       "running",
			Command:     "go test ./...",
			Workdir:     "/repo",
			Preview:     "updated",
		},
	})

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.BackgroundActivities) != backgroundCount {
		t.Fatalf("hydration = %+v, want %d backgrounds", hydration, backgroundCount)
	}
	for index, background := range hydration.Payload.Hydration.BackgroundActivities {
		if background.ActivityID.String() != activityIDs[index].String() {
			t.Fatalf("hydrated background %d id = %q, want first-seen order id %q", index, background.ActivityID.String(), activityIDs[index])
		}
	}
	preview := hydration.Payload.Hydration.BackgroundActivities[0].Preview
	if preview == nil || *preview != "updated" {
		t.Fatalf("updated first background preview = %v, want updated", preview)
	}
}

func TestSessionTranscriptFeedHydratesPromptsInCreationOrder(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	const promptCount = 16
	for index := 0; index < promptCount; index++ {
		projectPendingPromptForTest(registry, engine.SessionID(), tools.AskQuestionRequest{
			ID:       fmt.Sprintf("ask-%02d", index),
			Question: "Choose",
			StepID:   registryTestStepID,
		})
	}
	publishRunState(registry, engine.SessionID(), true)

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.PendingPrompts) != promptCount {
		t.Fatalf("hydration = %+v, want %d prompts", hydration, promptCount)
	}
	for index, prompt := range hydration.Payload.Hydration.PendingPrompts {
		wantID := fmt.Sprintf("ask-%02d", index)
		if prompt.PromptID != clientui.PromptID(wantID) {
			t.Fatalf("hydrated prompt %d id = %q, want creation order id %q", index, prompt.PromptID, wantID)
		}
	}
}

func TestSessionTranscriptFeedSequencerHydratesActiveStepAndPublishesFinishedStep(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:   runtime.EventRunStateChanged,
		StepID: registryTestStepID,
		RunState: &runtime.RunState{
			Lifecycle:  runtime.RunLifecycle{Phase: runtime.RunLifecycleRunning, Mode: runtime.RunModeTurn},
			RunID:      registryTestRunID,
			ActiveKind: runtime.ActiveKindUserTurn,
			Status:     runtime.RunStatusRunning,
		},
	})
	publishRunState(registry, engine.SessionID(), true)
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Payload.Hydration == nil || hydration.Payload.Hydration.ActiveStep == nil || hydration.Payload.Hydration.ActiveStep.Status != clientui.RunStatusRunning {
		t.Fatalf("hydration active step = %+v, want running", hydration)
	}

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:   runtime.EventRunStateChanged,
		StepID: registryTestStepID,
		RunState: &runtime.RunState{
			Lifecycle:  runtime.RunLifecycle{Phase: runtime.RunLifecycleFinished, Mode: runtime.RunModeTurn},
			RunID:      registryTestRunID,
			ActiveKind: runtime.ActiveKindUserTurn,
			Status:     runtime.RunStatusCompleted,
		},
	})
	publishRunState(registry, engine.SessionID(), false)
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessageStepState || live.Payload.StepState == nil || live.Payload.StepState.Status != clientui.RunStatusCompleted {
		t.Fatalf("live step state = %+v, want seq=2 completed", live)
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
	if hydration.Sequence != 1 || hydration.Payload.Hydration == nil || hydration.Payload.Hydration.SessionStatus.ReviewerFrequency != engine.ReviewerFrequency() {
		t.Fatalf("hydration session status = %+v, want runtimeview-projected status", hydration)
	}

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:         runtime.EventConversationUpdated,
		ContextUsage: &runtime.ContextUsage{UsedTokens: 10, WindowTokens: 100},
	})
	live := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageSessionStatus)
	if live.Kind != clientui.TranscriptMessageSessionStatus || live.Payload.SessionStatus == nil {
		t.Fatalf("live session status = %+v, want session_status", live)
	}
}

func TestSessionTranscriptFeedSequencerHydratesPendingPromptAndPublishesResolution(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	projectPendingPromptForTest(registry, engine.SessionID(), tools.AskQuestionRequest{
		ID:       "ask-1",
		Question: "approve?",
		Approval: true,
		ApprovalOptions: []tools.AskQuestionApprovalOption{{
			Decision: tools.AskQuestionApprovalDecisionAllowOnce,
			Label:    "Allow once",
		}},
		StepID: registryTestStepID,
	})
	publishRunState(registry, engine.SessionID(), true)

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.PendingPrompts) != 1 {
		t.Fatalf("hydration pending prompts = %+v, want one prompt", hydration)
	}
	prompt := hydration.Payload.Hydration.PendingPrompts[0]
	if prompt.PromptID != "ask-1" || prompt.Kind != clientui.TranscriptPromptKindApproval || prompt.State != clientui.TranscriptPromptStatePending {
		t.Fatalf("hydration prompt = %+v, want pending approval ask-1", prompt)
	}

	resolvePendingPromptForTest(registry, engine.SessionID(), "ask-1")
	live := nextTranscriptMessage(t, sub)
	if live.Sequence != 2 || live.Kind != clientui.TranscriptMessagePromptResolved || live.Payload.PromptResolved == nil || live.Payload.PromptResolved.State != clientui.TranscriptPromptStateResolved {
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

	streamUUID := uuid.MustParse("f84c7d21-4c94-4a54-87fd-b41f5bd01d38")
	streamID, err := runtimeids.ParseAssistantStreamID(streamUUID.String())
	if err != nil {
		t.Fatalf("ParseAssistantStreamID: %v", err)
	}
	metadata := &runtime.AssistantStreamMetadata{StepID: registryTestStepID}
	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:                        runtime.EventAssistantDelta,
		StepID:                      registryTestStepID,
		AssistantDelta:              "hello",
		AssistantDeltaPhase:         llm.MessagePhaseFinal,
		AssistantStreamMetadata:     metadata,
		AssistantTranscriptStreamID: &streamUUID,
	})
	delta := nextTranscriptMessage(t, sub)
	if delta.Sequence != 2 || delta.Kind != clientui.TranscriptMessageAssistantDelta {
		t.Fatalf("delta message = %+v, want seq=2 assistant delta", delta)
	}
	if delta.Payload.AssistantDelta == nil || delta.Payload.AssistantDelta.StreamID != streamID {
		t.Fatalf("delta identity = %+v, want stream %q", delta.Payload.AssistantDelta, streamID.String())
	}

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:                        runtime.EventAssistantMessage,
		StepID:                      registryTestStepID,
		Message:                     llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "hello"},
		AssistantStreamMetadata:     metadata,
		AssistantTranscriptStreamID: &streamUUID,
		CommittedTranscriptChanged:  true,
	})
	committed := nextTranscriptMessage(t, sub)
	if committed.Sequence != 3 || committed.Kind != clientui.TranscriptMessageCommittedRow {
		t.Fatalf("committed message = %+v, want seq=3 committed row", committed)
	}
	if committed.Payload.CommittedRow == nil || committed.Payload.CommittedRow.Assistant == nil || committed.Payload.CommittedRow.Assistant.StreamID == nil || *committed.Payload.CommittedRow.Assistant.StreamID != streamID {
		t.Fatalf("committed assistant identity = %+v, want stream %q", committed.Payload.CommittedRow, streamID.String())
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

	resource := registryTestResourceRef(engine.SessionID())
	publishToolStartForTest(registry, resource, "call-b")
	publishToolStartForTest(registry, resource, "call-a")
	publishToolCompletionForTest(registry, resource, "call-b")
	publishToolCompletionForTest(registry, resource, "call-a")

	first := nextTranscriptMessage(t, sub)
	second := nextTranscriptMessage(t, sub)
	third := nextTranscriptMessage(t, sub)
	fourth := nextTranscriptMessage(t, sub)
	if first.Sequence != 2 || second.Sequence != 3 || third.Sequence != 4 || fourth.Sequence != 5 {
		t.Fatalf("tool message sequences = %d,%d,%d,%d; want 2..5", first.Sequence, second.Sequence, third.Sequence, fourth.Sequence)
	}
	if first.Kind != clientui.TranscriptMessageToolStart || first.Payload.ToolStart == nil || first.Payload.ToolStart.ToolCallID != "call-b" {
		t.Fatalf("first tool message = %+v, want call-b start", first)
	}
	if second.Kind != clientui.TranscriptMessageToolStart || second.Payload.ToolStart == nil || second.Payload.ToolStart.ToolCallID != "call-a" {
		t.Fatalf("second tool message = %+v, want call-a start", second)
	}
	if third.Payload.CommittedRow == nil || third.Payload.CommittedRow.Kind != clientui.TranscriptRowTool || third.Payload.CommittedRow.Tool == nil || third.Payload.CommittedRow.Tool.ToolCallID != "call-b" {
		t.Fatalf("third message = %+v, want call-b tool row", third)
	}
	if fourth.Payload.CommittedRow == nil || fourth.Payload.CommittedRow.Kind != clientui.TranscriptRowTool || fourth.Payload.CommittedRow.Tool == nil || fourth.Payload.CommittedRow.Tool.ToolCallID != "call-a" {
		t.Fatalf("fourth message = %+v, want call-a tool row", fourth)
	}
}

func TestSessionTranscriptHydratesRuntimeLedgerInFlightToolAndCompletionTerminalsIt(t *testing.T) {
	registry := NewRuntimeRegistry()
	handler := newBlockingToolHandler()
	engine := newRegistryRuntime(t, registryRuntimeFakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: handler}), runtime.Config{
		Model:         "gpt-5",
		ThinkingLevel: "medium",
	}, func(engine *runtime.Engine, evt runtime.Event) {
		registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), evt)
	})
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserShellCommand(context.Background(), "pwd")
		done <- err
	}()
	handler.waitFallbackEntered(t)
	publishRegistryRuntimeReadModel(t, registry, engine.SessionID())

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Payload.Hydration == nil || len(hydration.Payload.Hydration.InFlightTools) != 1 {
		t.Fatalf("hydration in-flight tools = %+v, want one ledger tool", hydration)
	}
	inFlight := hydration.Payload.Hydration.InFlightTools[0]
	if inFlight.ToolCallID == "" || inFlight.ToolName != string(toolspec.ToolExecCommand) {
		t.Fatalf("hydrated in-flight tool = %+v, want provider call id and exec_command", inFlight)
	}

	handler.releaseFallback()
	if err := <-done; err != nil {
		t.Fatalf("SubmitUserShellCommand: %v", err)
	}
	completion := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageCommittedRow)
	if completion.Kind != clientui.TranscriptMessageCommittedRow || completion.Payload.CommittedRow == nil || completion.Payload.CommittedRow.Tool == nil {
		t.Fatalf("completion message = %+v, want committed tool row", completion)
	}
	if completion.Payload.CommittedRow.Tool.ToolCallID != inFlight.ToolCallID {
		t.Fatalf("completion call id = %q, want hydrated call id %q", completion.Payload.CommittedRow.Tool.ToolCallID, inFlight.ToolCallID)
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

	publishToolStartForTest(registry, registryTestResourceRef(engine.SessionID()), "call-abort")
	start := nextTranscriptMessage(t, sub)
	if start.Sequence != 2 || start.Kind != clientui.TranscriptMessageToolStart || start.Payload.ToolStart == nil || start.Payload.ToolStart.ToolCallID != "call-abort" {
		t.Fatalf("tool start = %+v, want visible start", start)
	}

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:            runtime.EventToolCallAborted,
		StepID:          registryTestStepID,
		ToolCall:        &llm.ToolCall{ID: "call-abort", Name: string(toolspec.ToolExecCommand)},
		ToolAbortReason: string(clientui.ToolAbortCanceled),
	})
	abort := nextTranscriptMessage(t, sub)
	if abort.Sequence != 3 || abort.Kind != clientui.TranscriptMessageToolAbort || abort.Payload.ToolAbort == nil || abort.Payload.ToolAbort.ToolCallID != "call-abort" {
		t.Fatalf("tool abort = %+v, want terminal abort", abort)
	}
}

func subscribeTranscriptForTest(t *testing.T, registry *RuntimeRegistry, sessionID string) serverapi.TranscriptSubscription {
	t.Helper()
	sub, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	return sub
}

func publishRegistryRuntimeReadModel(t *testing.T, registry *RuntimeRegistry, sessionID string) {
	t.Helper()
	update, err := registry.RuntimeReadModelFeedSnapshot(context.Background(), sessionID, nil)
	if err != nil {
		t.Fatalf("RuntimeReadModelFeedSnapshot: %v", err)
	}
	registry.PublishRuntimeReadModelUpdate(sessionID, update)
}

func nextTranscriptMessage(t *testing.T, sub serverapi.TranscriptSubscription) clientui.TranscriptMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("transcript subscription next: %v", err)
	}
	return message
}

func nextTranscriptMessageOfKind(t *testing.T, sub serverapi.TranscriptSubscription, kind clientui.TranscriptMessageKind) clientui.TranscriptMessage {
	t.Helper()
	for i := 0; i < 8; i++ {
		message := nextTranscriptMessage(t, sub)
		if message.Kind == kind {
			return message
		}
	}
	t.Fatalf("did not receive transcript message kind %q", kind)
	return clientui.TranscriptMessage{}
}

func nextTranscriptMessageTimeout(sub serverapi.TranscriptSubscription, timeout time.Duration) (clientui.TranscriptMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return sub.Next(ctx)
}

func publishToolCompletionForTest(registry *RuntimeRegistry, resource runtimeids.SessionResourceRef, callID string) {
	registry.PublishAuthorityRuntimeEvent(resource, runtime.Event{
		Kind:   runtime.EventToolCallCompleted,
		StepID: registryTestStepID,
		ToolResult: &tools.Result{
			CallID: callID,
			Name:   toolspec.ToolExecCommand,
			Output: []byte(`{"output":"done"}`),
		},
		CommittedTranscriptChanged: true,
	})
}

func publishToolStartForTest(registry *RuntimeRegistry, resource runtimeids.SessionResourceRef, callID string) {
	registry.PublishAuthorityRuntimeEvent(resource, runtime.Event{
		Kind:   runtime.EventToolCallStarted,
		StepID: registryTestStepID,
		ToolCall: &llm.ToolCall{
			ID:   callID,
			Name: string(toolspec.ToolExecCommand),
		},
		CommittedTranscriptChanged: true,
	})
}

func TestTranscriptSubscriptionBrokerDeliversToolTerminalsWithoutStart(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(transcriptBrokerHydration(t))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)

	broker.Publish([]clientui.TranscriptMessage{{
		Kind: clientui.TranscriptMessageCommittedRow,
		Payload: clientui.TranscriptPayload{CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityOngoingCollapsed,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{StepID: mustRegistryStepID(t), ToolCallID: "hosted-call", ToolName: "web_search"},
		}},
	}})
	row := nextTranscriptMessage(t, sub)
	if row.Kind != clientui.TranscriptMessageCommittedRow || row.Payload.CommittedRow == nil || row.Payload.CommittedRow.Tool == nil || row.Payload.CommittedRow.Tool.ToolCallID != "hosted-call" {
		t.Fatalf("message = %+v, want committed tool row without preceding start", row)
	}

	broker.Publish([]clientui.TranscriptMessage{{
		Kind: clientui.TranscriptMessageToolAbort,
		Payload: clientui.TranscriptPayload{ToolAbort: &clientui.TranscriptToolAbort{
			StepID:     mustRegistryStepID(t),
			ToolCallID: "hosted-call-2",
			Reason:     clientui.ToolAbortCanceled,
		}},
	}})
	abort := nextTranscriptMessage(t, sub)
	if abort.Kind != clientui.TranscriptMessageToolAbort || abort.Payload.ToolAbort == nil || abort.Payload.ToolAbort.ToolCallID != "hosted-call-2" {
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

	registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), runtime.Event{
		Kind:         runtime.EventUserMessageFlushed,
		StepID:       registryTestStepID,
		UserMessage:  "steer text",
		ContextUsage: &runtime.ContextUsage{UsedTokens: 10, WindowTokens: 100},
	})
	row := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageCommittedRow)
	if row.Payload.CommittedRow == nil || row.Payload.CommittedRow.User == nil || row.Payload.CommittedRow.User.Text != "steer text" {
		t.Fatalf("committed row = %+v, want flushed user text", row.Payload.CommittedRow)
	}
	status := nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageSessionStatus)
	if status.Payload.SessionStatus == nil {
		t.Fatalf("status = %+v, want session status after user flush", status)
	}
}
