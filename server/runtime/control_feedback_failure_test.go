package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestDefinitelyUncommittedControlSettingStopsRuntimeBeforeFeedbackOrLiveProjection(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	blockTestSessionMetadataMutations(t, store)

	changed, enabled, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(t.Context(), false, func(bool, bool) string {
		return "feedback"
	})
	if err == nil || receipt.Committed || changed || enabled || !engine.QuestionsEnabled() {
		t.Fatalf("uncommitted Questions mutation = changed %t enabled %t receipt %+v current %t error %v", changed, enabled, receipt, engine.QuestionsEnabled(), err)
	}
	assertBoundedControlFeedbackCount(t, store, 0)
	if _, _, _, closeErr := engine.SetQuestionsEnabledWithCommittedFeedback(t.Context(), false, func(bool, bool) string {
		return "feedback"
	}); !errors.Is(closeErr, ErrEngineClosed) {
		t.Fatalf("mutation after uncommitted settings failure = %v, want Engine closed", closeErr)
	}
}

func TestSetFastModeWithCommittedFeedbackKeepsCommittedSettingOnAppendFailure(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}}, Config{Model: "gpt-5.3-codex"})
	blocker := mustBlockTestEventLogAppends(t, store)

	changed, receipt, err := engine.SetFastModeEnabledWithCommittedFeedback(context.Background(), true, func(bool) string {
		return "feedback"
	})
	if err == nil || !receipt.Committed || !changed || !engine.FastModeEnabled() {
		t.Fatalf(
			"committed fast-mode setting was not retained after feedback failure: receipt=%+v changed=%t enabled=%t error=%v",
			receipt,
			changed,
			engine.FastModeEnabled(),
			err,
		)
	}
	requireSessionFastModeOverride(t, store, true)

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log appends: %v", err)
	}
	assertBoundedControlFeedbackCount(t, store, 0)
}

func requireSessionFastModeOverride(t *testing.T, store *session.Store, want bool) {
	t.Helper()
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Fast == nil || *meta.ChatSettings.Fast != want {
		t.Fatalf("Session Fast Mode override = %+v, want %t", meta.ChatSettings, want)
	}
}

func TestSetQuestionsWithCommittedFeedbackKeepsCommittedSettingOnAppendFailure(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	blocker := mustBlockTestEventLogAppends(t, store)

	changed, enabled, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(context.Background(), false, func(bool, bool) string {
		return "feedback"
	})
	if err == nil || !receipt.Committed || !changed || enabled || engine.QuestionsEnabled() {
		t.Fatalf(
			"committed Questions setting was not retained after feedback failure: receipt=%+v changed=%t enabled=%t current=%t error=%v",
			receipt,
			changed,
			enabled,
			engine.QuestionsEnabled(),
			err,
		)
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Questions == nil || *meta.ChatSettings.Questions {
		t.Fatalf("Session Questions override = %+v, want false", meta.ChatSettings)
	}

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log appends: %v", err)
	}
	assertBoundedControlFeedbackCount(t, store, 0)
}

func TestSetReviewerWithCommittedFeedbackKeepsCommittedSettingOnAppendFailure(t *testing.T) {
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

	changed, mode, receipt, err := engine.SetReviewerEnabledWithCommittedFeedback(context.Background(), true, func(bool, string, bool) string {
		return "feedback"
	})
	if err == nil || !receipt.Committed || !changed || mode != "edits" || engine.ReviewerFrequency() != "edits" {
		t.Fatalf(
			"committed Reviewer setting was not retained after feedback failure: receipt=%+v changed=%t mode=%q frequency=%q error=%v",
			receipt,
			changed,
			mode,
			engine.ReviewerFrequency(),
			err,
		)
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Supervisor == nil || *meta.ChatSettings.Supervisor != "edits" {
		t.Fatalf("Session Supervisor override = %+v, want edits", meta.ChatSettings)
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
