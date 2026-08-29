package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/toolspec"
)

func TestDefinitelyUncommittedImmediateSettingStopsBeforePublicationOrLiveProjection(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	blockTestSessionMetadataMutations(t, store)
	publications := 0

	changed, enabled, err := engine.SetQuestionsEnabledWithPublication(t.Context(), false, func(clientui.TranscriptSessionSettingFeedback) error {
		publications++
		return nil
	})
	if err == nil || changed || !enabled || !engine.QuestionsEnabled() {
		t.Fatalf("uncommitted Questions mutation = changed %t enabled %t current %t error %v", changed, enabled, engine.QuestionsEnabled(), err)
	}
	if publications != 0 {
		t.Fatalf("uncommitted Questions publications = %d, want 0", publications)
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("uncommitted Questions projected rows: %+v", rows)
	}
	if _, _, closeErr := engine.SetQuestionsEnabledWithPublication(t.Context(), false, nil); !errors.Is(closeErr, ErrEngineClosed) {
		t.Fatalf("mutation after uncommitted settings failure = %v, want Engine closed", closeErr)
	}
}

func TestSetFastModeKeepsCommittedSettingOnPublicationFailure(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}}, Config{Model: "gpt-5.3-codex"})
	publicationErr := errors.New("setting publication failed")

	changed, err := engine.SetFastModeEnabledWithPublication(context.Background(), true, func(clientui.TranscriptSessionSettingFeedback) error {
		return publicationErr
	})
	if !errors.Is(err, publicationErr) || !changed || !engine.FastModeEnabled() {
		t.Fatalf("committed Fast Mode = changed %t enabled %t error %v", changed, engine.FastModeEnabled(), err)
	}
	requireSessionFastModeOverride(t, store, true)
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("committed Fast Mode projected rows after publication failure: %+v", rows)
	}
}

func TestSetQuestionsKeepsCommittedSettingOnPublicationFailure(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	publicationErr := errors.New("setting publication failed")

	changed, enabled, err := engine.SetQuestionsEnabledWithPublication(context.Background(), false, func(clientui.TranscriptSessionSettingFeedback) error {
		return publicationErr
	})
	if !errors.Is(err, publicationErr) || !changed || enabled || engine.QuestionsEnabled() {
		t.Fatalf("committed Questions setting was not retained: changed=%t enabled=%t current=%t error=%v", changed, enabled, engine.QuestionsEnabled(), err)
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Questions == nil || *meta.ChatSettings.Questions {
		t.Fatalf("Session Questions override = %+v, want false", meta.ChatSettings)
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("committed Questions projected rows after publication failure: %+v", rows)
	}
}

func TestSetReviewerKeepsCommittedSettingOnPublicationFailure(t *testing.T) {
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
	publicationErr := errors.New("setting publication failed")

	changed, mode, err := engine.SetReviewerEnabledWithPublication(context.Background(), true, func(clientui.TranscriptSessionSettingFeedback) error {
		return publicationErr
	})
	if !errors.Is(err, publicationErr) || !changed || mode != "edits" || engine.ReviewerFrequency() != "edits" {
		t.Fatalf("committed Reviewer setting was not retained: changed=%t mode=%q frequency=%q error=%v", changed, mode, engine.ReviewerFrequency(), err)
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Supervisor == nil || *meta.ChatSettings.Supervisor != "edits" {
		t.Fatalf("Session Supervisor override = %+v, want edits", meta.ChatSettings)
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("committed Reviewer projected rows after publication failure: %+v", rows)
	}
}

func requireSessionFastModeOverride(t *testing.T, store *session.Store, want bool) {
	t.Helper()
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Fast == nil || *meta.ChatSettings.Fast != want {
		t.Fatalf("Session Fast Mode override = %+v, want %t", meta.ChatSettings, want)
	}
}
