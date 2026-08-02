package registry

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/transcript"
)

func init() {
	transcriptContractViolationsPanic = true
}

func transcriptBrokerHydration(t *testing.T) clientui.TranscriptEvent {
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
	return clientui.NewTranscriptEvent(hydration)
}

func transcriptBrokerDiagnostic(code clientui.OperationalDiagnosticCode, detail string) clientui.TranscriptEvent {
	return clientui.NewTranscriptEvent(clientui.TranscriptOperationalDiagnostic{
		Code:   code,
		Detail: detail,
	})
}

func mustRegistryStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return stepID
}

func TestTranscriptSubscriptionBrokerSequencesEachSubscriberFromHydration(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	first, err := broker.Subscribe(transcriptBrokerHydration(t))
	if err != nil {
		t.Fatalf("Subscribe first: %v", err)
	}
	defer func() { _ = first.Close() }()

	firstHydration := nextTranscriptMessage(t, first)
	if firstHydration.Sequence != 1 || firstHydration.Kind() != clientui.TranscriptMessageHydration {
		t.Fatalf("first hydration = %+v, want seq=1 hydration", firstHydration)
	}

	broker.Publish([]clientui.TranscriptEvent{
		transcriptBrokerDiagnostic(clientui.OperationalDiagnosticSleepGuardFailed, "sleep guard failed"),
		transcriptBrokerDiagnostic(clientui.OperationalDiagnosticPromptHistoryPersistFailed, "prompt history failed"),
	})
	if got := nextTranscriptMessage(t, first); got.Sequence != 2 || got.Kind() != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("first live one = %+v, want seq=2 operational diagnostic", got)
	}
	if got := nextTranscriptMessage(t, first); got.Sequence != 3 || got.Kind() != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("first live two = %+v, want seq=3 operational diagnostic", got)
	}

	second, err := broker.Subscribe(transcriptBrokerHydration(t))
	if err != nil {
		t.Fatalf("Subscribe second: %v", err)
	}
	defer func() { _ = second.Close() }()
	secondHydration := nextTranscriptMessage(t, second)
	if secondHydration.Sequence != 1 || secondHydration.Kind() != clientui.TranscriptMessageHydration {
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
			broker.Publish([]clientui.TranscriptEvent{transcriptBrokerDiagnostic(clientui.OperationalDiagnosticSleepGuardFailed, "sleep guard failed")})
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
	broker.Publish([]clientui.TranscriptEvent{clientui.NewTranscriptEvent(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Kind:       clientui.TranscriptRowTool,
		Tool:       &clientui.TranscriptToolRow{StepID: mustRegistryStepID(t), ToolCallID: ""},
	})})
}

func TestSessionFeedSequencerRejectsUninitializedEventBeforeMutation(t *testing.T) {
	sequencer := newSessionFeedSequencer(newTranscriptSubscriptionBroker())
	before := sequencer.snapshot
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("uninitialized transcript event did not fail fast")
		}
		if !reflect.DeepEqual(sequencer.snapshot, before) {
			t.Fatalf("sequencer snapshot mutated on invalid event: before=%+v after=%+v", before, sequencer.snapshot)
		}
	}()
	sequencer.Publish([]clientui.TranscriptEvent{{}})
}

func TestSessionFeedSequencerRejectsInvalidBatchBeforePrefixMutation(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	sequencer := newSessionFeedSequencer(broker)
	hydration := transcriptBrokerHydration(t).Payload().(clientui.TranscriptHydration)
	sequencer.snapshot.runtimeReadModel = &hydration.RuntimeReadModelUpdate
	sub, err := sequencer.Subscribe(hydration)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)
	valid := clientui.NewTranscriptEvent(clientui.TranscriptToolStart{
		StepID:     mustRegistryStepID(t),
		ToolCallID: "tool-call-prefix",
		ToolName:   "exec_command",
	})
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("invalid batch did not fail fast")
		}
		if _, err := nextTranscriptMessageTimeout(sub, 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("valid prefix was delivered after invalid batch: %v", err)
		}
		hydratedSub, err := sequencer.Subscribe(hydration)
		if err != nil {
			t.Fatalf("subscribe after rejected batch: %v", err)
		}
		defer func() { _ = hydratedSub.Close() }()
		hydratedMessage := nextTranscriptMessage(t, hydratedSub)
		hydrated, ok := hydratedMessage.Payload().(clientui.TranscriptHydration)
		if !ok {
			t.Fatalf("post-batch hydration payload = %T, want TranscriptHydration", hydratedMessage.Payload())
		}
		for _, tool := range hydrated.InFlightTools {
			if tool.ToolCallID == "tool-call-prefix" {
				t.Fatalf("post-batch hydration contains rejected tool prefix: %+v", tool)
			}
		}
	}()
	sequencer.Publish([]clientui.TranscriptEvent{valid, {}})
}

func TestTranscriptBrokerRejectsUninitializedEventWithoutSequenceOrDelivery(t *testing.T) {
	restore := withTranscriptContractViolationPanic(false)
	defer restore()
	broker := newTranscriptSubscriptionBroker()
	sub, err := broker.Subscribe(transcriptBrokerHydration(t))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = nextTranscriptMessage(t, sub)
	broker.Publish([]clientui.TranscriptEvent{{}})
	_, err = nextTranscriptMessageTimeout(sub, time.Second)
	if err == nil {
		t.Fatal("uninitialized event was delivered")
	}
	reason, ok := serverapi.TranscriptCloseReasonOf(err)
	if !ok || reason != serverapi.TranscriptCloseReasonContractViolation {
		t.Fatalf("close reason = %q ok=%t, want contract violation", reason, ok)
	}
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

	broker.Publish([]clientui.TranscriptEvent{clientui.NewTranscriptEvent(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Kind:       clientui.TranscriptRowTool,
		Tool:       &clientui.TranscriptToolRow{StepID: mustRegistryStepID(t), ToolCallID: ""},
	})})
	_, err = sub.Next(context.Background())
	if err == nil {
		t.Fatal("contract-invalid tool completion was delivered")
	}
	reason, ok := serverapi.TranscriptCloseReasonOf(err)
	if !ok || reason != serverapi.TranscriptCloseReasonContractViolation {
		t.Fatalf("contract violation reason = %q ok=%t, want contract_violation; err=%v", reason, ok, err)
	}
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

func withTranscriptContractViolationPanic(enabled bool) func() {
	previous := transcriptContractViolationsPanic
	transcriptContractViolationsPanic = enabled
	return func() {
		transcriptContractViolationsPanic = previous
	}
}

func nextTranscriptMessage(t *testing.T, sub serverapi.TranscriptSubscription) clientui.TranscriptMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	message, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("transcript subscription next: %v", err)
	}
	return message
}

func nextTranscriptMessageTimeout(sub serverapi.TranscriptSubscription, timeout time.Duration) (clientui.TranscriptMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return sub.Next(ctx)
}
