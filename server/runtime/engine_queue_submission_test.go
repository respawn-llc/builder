package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

func TestIdleHumanBoundaryAppliesBeforeInitialProviderOriginIsExposed(t *testing.T) {
	probe := &agentStepOriginProbe{}
	var originExposedDuringApply atomic.Bool
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("done"),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}}},
		tools.NewRegistry(),
		Config{
			Model:         "gpt-5",
			StepLifecycle: probe,
			OnEvent: func(event Event) {
				if event.QueuedUserMessageStatus == nil ||
					event.QueuedUserMessageStatus.Status != QueuedUserMessageSubmitted {
					return
				}
				began, _ := probe.snapshot()
				originExposedDuringApply.Store(len(began) != 0)
			},
		},
	)
	if _, err := engine.QueueUserMessage("idle input"); err != nil {
		t.Fatalf("QueueUserMessage: %v", err)
	}
	if _, err := engine.SubmitQueuedUserMessages(context.Background()); err != nil {
		t.Fatalf("SubmitQueuedUserMessages: %v", err)
	}
	if originExposedDuringApply.Load() {
		t.Fatal("initial provider origin was exposed while the idle human Boundary was applying")
	}
	began, boundaries := probe.snapshot()
	if len(began) != 1 || len(boundaries) != 1 || began[0] != boundaries[0] {
		t.Fatalf("idle Agent Step lifecycle = began:%+v boundaries:%+v, want one exact provider origin", began, boundaries)
	}
}

func TestSubmitQueuedUserMessagesPreservesCommittedFlushReceiptOnRunError(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	providerErr := &llm.ProviderAPIError{
		ProviderID: "test",
		Code:       llm.UnifiedErrorCodeProviderContract,
	}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{errors: []error{providerErr}},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	engine.QueueUserMessage("queued input")

	_, receipt, err := engine.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
	if !receipt.Committed || !errors.Is(err, providerErr) {
		t.Fatalf("queued submission receipt=%+v error=%v, want committed provider failure", receipt, err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("committed queued input retained retry ownership")
	}
	if userMessages := boundedPersistedUserMessageCount(t, store); userMessages != 1 {
		t.Fatalf("persisted queued user messages = %d, want one committed input", userMessages)
	}
}

func TestSubmitQueuedUserMessagesPreservesCommittedFlushReceiptOnTerminalResult(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("completed"),
			},
		}}},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	engine.QueueUserMessage("queued input")

	assistant, receipt, err := engine.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
	if !receipt.Committed || err != nil || messageContent(assistant) != "completed" {
		t.Fatalf("queued submission receipt=%+v error=%v", receipt, err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("committed queued input retained retry ownership")
	}
	if userMessages := boundedPersistedUserMessageCount(t, store); userMessages != 1 {
		t.Fatalf("persisted queued user messages = %d, want one committed input", userMessages)
	}
}

func boundedPersistedUserMessageCount(t *testing.T, store *session.Store) int {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded queued-input records: %v", err)
	}
	userMessages := 0
	for _, record := range window.Records {
		payload, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if !ok {
			continue
		}
		message, restoreErr := llmMessageFromSessionRecord(payload)
		if restoreErr != nil {
			t.Fatalf("restore queued-input message: %v", restoreErr)
		}
		if message.Role == llm.RoleUser {
			userMessages++
		}
	}
	return userMessages
}
