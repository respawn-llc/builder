package registry

import (
	"context"
	"errors"
	"io"
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

func TestSessionFeedSequencerRejectsInvalidBatchBeforePrefixMutation(t *testing.T) {
	broker := newTranscriptSubscriptionBroker()
	sequencer := newSessionFeedSequencer(broker)
	hydration := transcriptBrokerHydration(t).Payload().(clientui.TranscriptHydration)
	sub, err := sequencer.Subscribe(func() (clientui.TranscriptHydration, error) {
		return hydration, nil
	})
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
		hydratedSub, err := sequencer.Subscribe(func() (clientui.TranscriptHydration, error) {
			return hydration, nil
		})
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

func TestSessionFeedSequencerBuildsHydrationWithoutPriorRuntimeReadModel(t *testing.T) {
	h := transcriptBrokerHydration(t).Payload().(clientui.TranscriptHydration)
	sub, err := newSessionFeedSequencer(newTranscriptSubscriptionBroker()).Subscribe(func() (clientui.TranscriptHydration, error) { return h, nil })
	if err != nil {
		t.Fatalf("subscribe without prior read-model: %v", err)
	}
	defer func() { _ = sub.Close() }()
	if message := nextTranscriptMessage(t, sub); message.Sequence != 1 || message.Kind() != clientui.TranscriptMessageHydration {
		t.Fatalf("first message = %+v, want sequence 1 hydration", message)
	}
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
		Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
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

func TestTranscriptSubscriptionContractValidatesLiveLocatorProgression(t *testing.T) {
	restore := withTranscriptContractViolationPanic(false)
	defer restore()

	tests := []struct {
		name        string
		hydrated    []transcript.CommittedRowLocator
		live        []transcript.CommittedRowLocator
		wantFailure bool
	}{
		{
			name: "empty hydration starts at first live event",
			live: []transcript.CommittedRowLocator{{EventSequence: 5, RowOrdinal: 1}},
		},
		{
			name:     "hydrated watermark accepts newer event",
			hydrated: []transcript.CommittedRowLocator{{EventSequence: 10, RowOrdinal: 1}},
			live:     []transcript.CommittedRowLocator{{EventSequence: 11, RowOrdinal: 1}},
		},
		{
			name:     "same event continues contiguously",
			hydrated: []transcript.CommittedRowLocator{{EventSequence: 10, RowOrdinal: 1}},
			live: []transcript.CommittedRowLocator{
				{EventSequence: 11, RowOrdinal: 1},
				{EventSequence: 11, RowOrdinal: 2},
				{EventSequence: 12, RowOrdinal: 1},
			},
		},
		{
			name:        "duplicate ordinal fails",
			hydrated:    []transcript.CommittedRowLocator{{EventSequence: 10, RowOrdinal: 1}},
			live:        []transcript.CommittedRowLocator{{EventSequence: 11, RowOrdinal: 1}, {EventSequence: 11, RowOrdinal: 1}},
			wantFailure: true,
		},
		{
			name:        "regressed event fails",
			hydrated:    []transcript.CommittedRowLocator{{EventSequence: 10, RowOrdinal: 1}},
			live:        []transcript.CommittedRowLocator{{EventSequence: 11, RowOrdinal: 1}, {EventSequence: 10, RowOrdinal: 1}},
			wantFailure: true,
		},
		{
			name:        "skipped ordinal fails",
			hydrated:    []transcript.CommittedRowLocator{{EventSequence: 10, RowOrdinal: 1}},
			live:        []transcript.CommittedRowLocator{{EventSequence: 11, RowOrdinal: 1}, {EventSequence: 11, RowOrdinal: 3}},
			wantFailure: true,
		},
		{
			name:        "first live row must be newer than hydration",
			hydrated:    []transcript.CommittedRowLocator{{EventSequence: 10, RowOrdinal: 1}},
			live:        []transcript.CommittedRowLocator{{EventSequence: 10, RowOrdinal: 1}},
			wantFailure: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hydration := transcriptBrokerHydrationWithLocators(t, test.hydrated)
			broker := newTranscriptSubscriptionBroker()
			sub, err := broker.Subscribe(hydration)
			if err != nil {
				t.Fatalf("subscribe with test broker: %v", err)
			}
			defer func() { _ = sub.Close() }()
			_ = nextTranscriptMessage(t, sub)
			for _, locator := range test.live {
				broker.Publish([]clientui.TranscriptEvent{clientui.NewTranscriptEvent(transcriptBrokerCommittedRow(t, locator))})
			}
			if !test.wantFailure {
				for range test.live {
					if _, err := nextTranscriptMessageTimeout(sub, time.Second); err != nil {
						t.Fatalf("valid live locator was rejected: %v", err)
					}
				}
				return
			}
			for range test.live[:len(test.live)-1] {
				if _, err := nextTranscriptMessageTimeout(sub, time.Second); err != nil {
					t.Fatalf("valid prefix before invalid locator was rejected: %v", err)
				}
			}
			if _, err := sub.Next(context.Background()); err == nil {
				t.Fatal("invalid live locator progression was delivered")
			}
		})
	}
}

func transcriptBrokerHydrationWithLocators(t *testing.T, locators []transcript.CommittedRowLocator) clientui.TranscriptEvent {
	t.Helper()
	hydration := transcriptBrokerHydration(t).Payload().(clientui.TranscriptHydration)
	for _, locator := range locators {
		hydration.CommittedRows = append(hydration.CommittedRows, transcriptBrokerCommittedRow(t, locator))
	}
	return clientui.NewTranscriptEvent(hydration)
}

func transcriptBrokerCommittedRow(t *testing.T, locator transcript.CommittedRowLocator) clientui.TranscriptCommittedRow {
	t.Helper()
	return clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowUser,
		Locator:    locator,
		User:       &clientui.TranscriptUserRow{StepID: mustRegistryStepID(t), Text: "row"},
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
