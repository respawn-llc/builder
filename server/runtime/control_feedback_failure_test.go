package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/session"
)

func TestSetFastModeWithCommittedFeedbackDoesNotMutateOnAppendFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}}, Config{Model: "gpt-5.3-codex"})
	blocker := mustBlockTestEventLogAppends(t, store)

	changed, receipt, err := engine.SetFastModeEnabledWithCommittedFeedback(true, func(bool) string {
		return "feedback"
	})
	if err == nil || receipt.Committed || changed || engine.FastModeEnabled() {
		t.Fatalf(
			"uncommitted fast-mode feedback mutated runtime state: receipt=%+v changed=%t enabled=%t error=%v",
			receipt,
			changed,
			engine.FastModeEnabled(),
			err,
		)
	}

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log appends: %v", err)
	}
	changed, receipt, err = engine.SetFastModeEnabledWithCommittedFeedback(true, func(bool) string {
		return "feedback"
	})
	if err != nil || !receipt.Committed || !changed || !engine.FastModeEnabled() {
		t.Fatalf(
			"retry did not apply fast mode after durable feedback: receipt=%+v changed=%t enabled=%t error=%v",
			receipt,
			changed,
			engine.FastModeEnabled(),
			err,
		)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded feedback records: %v", err)
	}
	for _, record := range window.Records {
		if _, ok := mustSessionEventPayload(record).(session.LocalEntryRecord); ok {
			return
		}
	}
	t.Fatalf("bounded feedback records contain no committed local entry: %+v", window.Records)
}
