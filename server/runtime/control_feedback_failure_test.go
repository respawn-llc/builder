package runtime

import (
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
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

func TestCommittedImmediateSettingRemainsAppliedAfterPublicationFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}}, Config{Model: "gpt-5.3-codex"})
	publicationErr := errors.New("setting publication failed")

	changed, err := engine.SetFastModeEnabledWithPublication(t.Context(), true, func(clientui.TranscriptSessionSettingFeedback) error {
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

func requireSessionFastModeOverride(t *testing.T, store *session.Store, want bool) {
	t.Helper()
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Fast == nil || *meta.ChatSettings.Fast != want {
		t.Fatalf("Session Fast Mode override = %+v, want %t", meta.ChatSettings, want)
	}
}
