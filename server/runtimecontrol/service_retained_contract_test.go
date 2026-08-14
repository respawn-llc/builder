package runtimecontrol_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/internal/testharness/corefixture"
	"core/server/attentionnotify"
	"core/server/registry"
	"core/server/runtimecontrol"
	"core/server/session"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func TestServicePersistsEveryChatControlWithoutCreatingRuntime(t *testing.T) {
	fixture := corefixture.New(t)
	store := fixture.CreateSession(t)
	client := fixture.Core.RuntimeControlClient()
	sessionID := store.Meta().SessionID

	reviewer, err := client.SetReviewerEnabled(t.Context(), serverapi.RuntimeSetReviewerEnabledRequest{
		ClientRequestID: "retained-reviewer",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil || reviewer.Mode != "off" || !reviewer.Changed {
		t.Fatalf("SetReviewerEnabled = %+v, %v", reviewer, err)
	}
	if err := client.SetThinkingLevel(t.Context(), serverapi.RuntimeSetThinkingLevelRequest{
		ClientRequestID: "retained-thinking",
		SessionID:       sessionID,
		Level:           "low",
	}); err != nil {
		t.Fatalf("SetThinkingLevel: %v", err)
	}
	fast, err := client.SetFastModeEnabled(t.Context(), serverapi.RuntimeSetFastModeEnabledRequest{
		ClientRequestID: "retained-fast",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetFastModeEnabled: %v", err)
	}
	questions, err := client.SetQuestionsEnabled(t.Context(), serverapi.RuntimeSetQuestionsEnabledRequest{
		ClientRequestID: "retained-questions",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetQuestionsEnabled: %v", err)
	}
	autoCompaction, err := client.SetAutoCompactionEnabled(t.Context(), serverapi.RuntimeSetAutoCompactionEnabledRequest{
		ClientRequestID: "retained-auto-compaction",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetAutoCompactionEnabled: %v", err)
	}
	if !fast.Changed || !questions.Changed || questions.Enabled || !autoCompaction.Changed || autoCompaction.Enabled {
		t.Fatalf("control responses = fast %+v, questions %+v, auto-compaction %+v", fast, questions, autoCompaction)
	}

	reopened, err := session.OpenByID(
		fixture.Config.PersistenceRoot,
		sessionID,
		fixture.Core.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("reopen Session: %v", err)
	}
	state, err := session.ChatSettingsStateFromMeta(reopened.Meta())
	if err != nil {
		t.Fatalf("ChatSettingsStateFromMeta: %v", err)
	}
	if state.Settings == nil ||
		state.Settings.Supervisor == nil || *state.Settings.Supervisor != "off" ||
		state.Settings.Thinking == nil || *state.Settings.Thinking != "low" ||
		state.Settings.Fast == nil || *state.Settings.Fast ||
		state.Settings.Questions == nil || *state.Settings.Questions ||
		state.Settings.AutoCompaction == nil || *state.Settings.AutoCompaction {
		t.Fatalf("persisted chat settings = %+v", state.Settings)
	}

	_, err = fixture.Core.RuntimeLiveControlClient().LiveWait(t.Context(), serverapi.RuntimeLiveWaitRequest{SessionID: sessionID})
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("LiveWait error = %v, want runtime unavailable", err)
	}
}

func TestServiceSetFastModeDedupesSuccessfulRetry(t *testing.T) {
	fixture := corefixture.New(t)
	store := fixture.CreateSession(t)
	request := serverapi.RuntimeSetFastModeEnabledRequest{
		ClientRequestID: "retained-fast-retry",
		SessionID:       store.Meta().SessionID,
		Enabled:         false,
	}

	first, err := fixture.Core.RuntimeControlClient().SetFastModeEnabled(t.Context(), request)
	if err != nil {
		t.Fatalf("first SetFastModeEnabled: %v", err)
	}
	second, err := fixture.Core.RuntimeControlClient().SetFastModeEnabled(t.Context(), request)
	if err != nil {
		t.Fatalf("retry SetFastModeEnabled: %v", err)
	}
	if first != second {
		t.Fatalf("retry response = %+v, want memoized %+v", second, first)
	}
}

func TestServiceSetThinkingLevelDedupesSuccessfulRetry(t *testing.T) {
	fixture := corefixture.New(t)
	store := fixture.CreateSession(t)
	request := serverapi.RuntimeSetThinkingLevelRequest{
		ClientRequestID: "retained-thinking-retry",
		SessionID:       store.Meta().SessionID,
		Level:           "high",
	}

	if err := fixture.Core.RuntimeControlClient().SetThinkingLevel(t.Context(), request); err != nil {
		t.Fatalf("first SetThinkingLevel: %v", err)
	}
	if err := fixture.Core.RuntimeControlClient().SetThinkingLevel(t.Context(), request); err != nil {
		t.Fatalf("retry SetThinkingLevel: %v", err)
	}
	reopened, err := session.OpenByID(
		fixture.Config.PersistenceRoot,
		store.Meta().SessionID,
		fixture.Core.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("reopen Session: %v", err)
	}
	state, err := session.ChatSettingsStateFromMeta(reopened.Meta())
	if err != nil {
		t.Fatalf("ChatSettingsStateFromMeta: %v", err)
	}
	if state.Settings == nil || state.Settings.Thinking == nil || *state.Settings.Thinking != "high" {
		t.Fatalf("persisted Thinking = %+v, want high", state.Settings)
	}
}

type retainedAskView struct {
	ask clientui.PendingAsk
}

func (v retainedAskView) ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	return serverapi.AskListPendingBySessionResponse{Asks: []clientui.PendingAsk{v.ask}}, nil
}

type retainedApprovalView struct{}

func (retainedApprovalView) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{}, nil
}

func TestLiveWatchReturnsInitialPendingQuestionWhenNoRunIsActive(t *testing.T) {
	fixture := corefixture.New(t)
	store := fixture.CreateSession(t)
	settings := fixture.Config.Settings
	if settings.Model == "" {
		settings.Model = "gpt-5"
	}
	if settings.ProviderOverride == "" {
		settings.ProviderOverride = "openai"
	}
	activation, err := fixture.Core.SessionRuntimeClient().ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "activate-retained-live-watch",
		SessionID:             store.Meta().SessionID,
		OwnerID:               "retained-live-watch",
		ActiveSettings:        settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Source:                fixture.Config.Source,
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.Core.SessionRuntimeClient().ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-retained-live-watch",
			Attachment:      activation.Attachment,
			OwnerID:         "retained-live-watch",
			DropOwner:       true,
		})
	})
	service, ok := fixture.Core.RuntimeLiveControlClient().(*runtimecontrol.Service)
	if !ok {
		t.Fatalf("runtime live client = %T, want *runtimecontrol.Service", fixture.Core.RuntimeLiveControlClient())
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(attentionnotify.NewBroker())
	service.WithLiveWatchPromptSources(
		retainedAskView{ask: clientui.PendingAsk{
			PromptID:  "retained-ask",
			SessionID: sessionID,
			StepID:    stepID,
			Question:  "Continue?",
			CreatedAt: time.Now().UTC(),
		}},
		retainedApprovalView{},
		attention,
	)

	response, err := service.LiveWatch(t.Context(), serverapi.RuntimeLiveWatchRequest{SessionID: sessionID.String()})
	if err != nil {
		t.Fatalf("LiveWatch: %v", err)
	}
	if response.Outcome.Kind != serverapi.RuntimeLiveWatchQuestion ||
		response.Outcome.Question == nil ||
		response.Outcome.Question.Ask == nil ||
		response.Outcome.Question.Ask.PromptID != "retained-ask" {
		t.Fatalf("LiveWatch response = %+v", response)
	}
}

var _ apicontract.AskViewService = retainedAskView{}
var _ apicontract.ApprovalViewService = retainedApprovalView{}
