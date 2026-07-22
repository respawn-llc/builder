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

	assertOneBoundedControlFeedback(t, store)
}

func TestSetQuestionsWithCommittedFeedbackDoesNotMutateOnAppendFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	blocker := mustBlockTestEventLogAppends(t, store)

	changed, enabled, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(false, func(bool, bool) string {
		return "feedback"
	})
	if err == nil || receipt.Committed || changed || !enabled || !engine.QuestionsEnabled() {
		t.Fatalf(
			"uncommitted questions feedback mutated runtime state: receipt=%+v changed=%t enabled=%t current=%t error=%v",
			receipt,
			changed,
			enabled,
			engine.QuestionsEnabled(),
			err,
		)
	}

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log appends: %v", err)
	}
	changed, enabled, receipt, err = engine.SetQuestionsEnabledWithCommittedFeedback(false, func(bool, bool) string {
		return "feedback"
	})
	if err != nil || !receipt.Committed || !changed || enabled || engine.QuestionsEnabled() {
		t.Fatalf(
			"retry did not apply questions setting after durable feedback: receipt=%+v changed=%t enabled=%t current=%t error=%v",
			receipt,
			changed,
			enabled,
			engine.QuestionsEnabled(),
			err,
		)
	}

	assertOneBoundedControlFeedback(t, store)
}

func assertOneBoundedControlFeedback(t *testing.T, store *session.Store) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded feedback records: %v", err)
	}
	entries := 0
	for _, record := range window.Records {
		if _, ok := mustSessionEventPayload(record).(session.LocalEntryRecord); ok {
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("bounded control-feedback entries = %d, want one", entries)
	}
}
