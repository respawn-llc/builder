package runtimecontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/registry"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type liveWatchAskViewStub struct {
	asks []clientui.PendingAsk
}

func (s liveWatchAskViewStub) ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	return serverapi.AskListPendingBySessionResponse{Asks: s.asks}, nil
}

type liveWatchApprovalViewStub struct{}

func (liveWatchApprovalViewStub) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{}, nil
}

type failingLiveWatchAttention struct{ err error }

func (f failingLiveWatchAttention) SubscribeAttentionNotifications(context.Context, serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return nil, f.err
}

func (f failingLiveWatchAttention) SubscribeSessionAttentionNotifications(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return nil, f.err
}

func TestLiveWatchReturnsInitialPendingQuestionWhenNoRunIsActive(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	sessionID := store.Meta().SessionID
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(attentionnotify.NewBroker())
	service.WithLiveWatchPromptSources(
		liveWatchAskViewStub{asks: []clientui.PendingAsk{{
			AskID: "ask-1", SessionID: sessionID, Question: "Continue?", CreatedAt: time.Now().UTC(),
		}}},
		liveWatchApprovalViewStub{},
		attention,
	)

	response, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("LiveWatch: %v", err)
	}
	if response.Outcome.Kind != serverapi.RuntimeLiveWatchQuestion ||
		response.Outcome.Question == nil || response.Outcome.Question.Ask == nil ||
		response.Outcome.Question.Ask.AskID != "ask-1" {
		t.Fatalf("LiveWatch response = %+v", response)
	}
}

func TestLiveWatchSurfacesAttentionStreamFailureBeforeArbitration(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	streamErr := errors.New("attention stream failed")
	service.WithLiveWatchPromptSources(liveWatchAskViewStub{}, liveWatchApprovalViewStub{}, failingLiveWatchAttention{err: streamErr})

	_, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
	if !errors.Is(err, streamErr) {
		t.Fatalf("LiveWatch error = %v, want attention stream failure", err)
	}
}

func TestLiveWatchResultClassifiesTypedTerminalStates(t *testing.T) {
	id := runtimeids.NewSessionID()
	cases := []struct {
		name       string
		result     runtime.LiveRunResult
		err        error
		kind       string
		reason     string
		diagnostic string
	}{
		{"no final", runtime.LiveRunResult{NoFinalReason: runtime.LiveRunNoFinalAnswerReasonGoalLoop}, runtime.ErrLiveRunNoFinalAnswer, "no_final_result", "", ""},
		{"interrupted", runtime.LiveRunResult{Status: runtime.RunStatusInterrupted, Error: errors.New("stop detail")}, errors.New("terminal"), "interrupted", "terminal", "stop detail"},
		{"error", runtime.LiveRunResult{Status: runtime.RunStatusFailed, Error: errors.New("failure detail")}, errors.New("terminal"), "execution_error", "terminal", "failure detail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := liveWatchResult(id, "session", tc.result, tc.err)
			if err != nil || string(response.Outcome.Kind) != tc.kind {
				t.Fatalf("result = %+v, err = %v", response, err)
			}
			if tc.reason == "" {
				return
			}
			if response.Outcome.Failure == nil || response.Outcome.Failure.Reason != tc.reason ||
				response.Outcome.Failure.Diagnostic == nil || *response.Outcome.Failure.Diagnostic != tc.diagnostic {
				t.Fatalf("failure = %+v", response.Outcome.Failure)
			}
		})
	}
}
