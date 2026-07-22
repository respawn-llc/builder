package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
)

func TestSubmitQueuedUserMessagesPreservesCommittedFlushReceiptOnRunError(t *testing.T) {
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
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read bounded queued-input records: %v", readErr)
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
	if userMessages != 1 {
		t.Fatalf("persisted queued user messages = %d, want one committed input", userMessages)
	}
}
