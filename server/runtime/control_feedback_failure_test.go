package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestSetFastModeWithCommittedFeedbackDoesNotMutateOnAppendFailure(t *testing.T) {
	t.Parallel()
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
	assertBoundedControlFeedbackCount(t, store, 0)
}

func TestSetQuestionsWithCommittedFeedbackDoesNotMutateOnAppendFailure(t *testing.T) {
	t.Parallel()
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
	assertBoundedControlFeedbackCount(t, store, 0)
}

func TestSetReviewerWithCommittedFeedbackDoesNotMutateOnAppendFailure(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: fakeTool{name: toolspec.ToolExecCommand},
	}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "off",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        &fakeClient{},
		},
	})
	blocker := mustBlockTestEventLogAppends(t, store)

	changed, mode, receipt, err := engine.SetReviewerEnabledWithCommittedFeedback(true, func(bool, string, bool) string {
		return "feedback"
	})
	if err == nil || receipt.Committed || changed || mode != "edits" || engine.ReviewerFrequency() != "off" {
		t.Fatalf(
			"uncommitted reviewer feedback mutated runtime state: receipt=%+v changed=%t mode=%q frequency=%q error=%v",
			receipt,
			changed,
			mode,
			engine.ReviewerFrequency(),
			err,
		)
	}

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log appends: %v", err)
	}
	assertBoundedControlFeedbackCount(t, store, 0)
}

func assertBoundedControlFeedbackCount(t *testing.T, store *session.Store, want int) {
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
	if entries != want {
		t.Fatalf("bounded control-feedback entries = %d, want %d", entries, want)
	}
}
