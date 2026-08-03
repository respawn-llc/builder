package runtimecontrol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type liveWatchAskView struct {
	response serverapi.AskListPendingBySessionResponse
	mu       sync.Mutex
	sequence []serverapi.AskListPendingBySessionResponse
}

func (v *liveWatchAskView) ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.sequence) > 0 {
		response := v.sequence[0]
		v.sequence = v.sequence[1:]
		return response, nil
	}
	return v.response, nil
}

type liveWatchApprovalView struct {
	response serverapi.ApprovalListPendingBySessionResponse
}

func (v *liveWatchApprovalView) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return v.response, nil
}

type liveWatchAttentionService struct {
	sub serverapi.AttentionNotificationSubscription
}

func (s liveWatchAttentionService) SubscribeSessionAttentionNotifications(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return s.sub, nil
}

func (s liveWatchAttentionService) SubscribeAttentionNotifications(context.Context, serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return s.sub, nil
}

type liveWatchAttentionSubscription struct {
	events chan clientui.AttentionNotificationEvent
	err    error
	once   sync.Once
}

func (s *liveWatchAttentionSubscription) Next(ctx context.Context) (clientui.AttentionNotificationEvent, error) {
	select {
	case event := <-s.events:
		return event, nil
	case <-ctx.Done():
		return clientui.AttentionNotificationEvent{}, ctx.Err()
	}
}

func (s *liveWatchAttentionSubscription) Close() error {
	s.once.Do(func() {})
	return nil
}

var _ apicontract.AskViewService = (*liveWatchAskView)(nil)
var _ apicontract.ApprovalViewService = (*liveWatchApprovalView)(nil)
var _ apicontract.AttentionNotificationService = liveWatchAttentionService{}
var _ serverapi.AttentionNotificationSubscription = (*liveWatchAttentionSubscription)(nil)

func TestServiceLiveWatchReturnsCurrentAccessRequestWithAuthoritativeLabels(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	service.WithLiveWatchPromptSources(
		&liveWatchAskView{},
		&liveWatchApprovalView{response: serverapi.ApprovalListPendingBySessionResponse{Approvals: []clientui.PendingApproval{{
			ApprovalID: "approval-1",
			SessionID:  store.Meta().SessionID,
			Question:   "Allow the operation?",
			Options: []clientui.ApprovalOption{
				{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Use the unusual label"},
				{Decision: clientui.ApprovalDecisionDeny, Label: "Keep it blocked"},
			},
			CreatedAt: time.Unix(1, 0).UTC(),
		}}}},
		liveWatchAttentionService{sub: &liveWatchAttentionSubscription{events: make(chan clientui.AttentionNotificationEvent)}},
	)

	response, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("LiveWatch: %v", err)
	}
	if len(response.Outcomes) != 1 || response.Outcomes[0].Question == nil {
		t.Fatalf("response = %+v, want one question outcome", response)
	}
	options := response.Outcomes[0].Question.AccessOptions
	if len(options) != 2 || options[0].Label != "Use the unusual label" || options[1].Label != "Keep it blocked" {
		t.Fatalf("access options = %+v", options)
	}
}

func TestServiceLiveWatchIdleWithoutPromptReturnsNoActiveRun(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	service.WithLiveWatchPromptSources(
		&liveWatchAskView{},
		&liveWatchApprovalView{},
		liveWatchAttentionService{sub: &liveWatchAttentionSubscription{events: make(chan clientui.AttentionNotificationEvent)}},
	)
	_, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
	if !errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("LiveWatch error = %v, want no active run", err)
	}
}

func TestObservationLiveRunResponseProjectsNoFinalAnswerAsObservedExecutionError(t *testing.T) {
	response, err := observationLiveRunResponse(
		"9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d",
		"session",
		runtime.LiveRunResult{
			NoFinalReason: runtime.LiveRunNoFinalAnswerReasonWorkflow,
		},
		runtime.ErrLiveRunNoFinalAnswer,
	)
	if err != nil {
		t.Fatalf("observationLiveRunResponse: %v", err)
	}
	if len(response.Outcomes) != 1 {
		t.Fatalf("outcomes = %+v", response.Outcomes)
	}
	outcome := response.Outcomes[0]
	if outcome.Kind != serverapi.RuntimeObservationOutcomeExecutionError ||
		outcome.ExecutionError == nil ||
		outcome.ExecutionError.Reason != string(runtime.LiveRunNoFinalAnswerReasonWorkflow) {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestServiceLiveWatchReturnsLaterQuestionAndCancelsLosingWait(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	askView := &liveWatchAskView{sequence: []serverapi.AskListPendingBySessionResponse{
		{},
		{Asks: []clientui.PendingAsk{{
			AskID:       "ask-1",
			SessionID:   store.Meta().SessionID,
			Question:    "What should happen next?",
			Suggestions: []string{"continue"},
		}}},
	}}
	attention := &liveWatchAttentionSubscription{events: make(chan clientui.AttentionNotificationEvent, 1)}
	service.WithLiveWatchPromptSources(askView, &liveWatchApprovalView{}, liveWatchAttentionService{sub: attention})

	submitDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "watch", "keep running"))
		submitDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime")
	}

	watchDone := make(chan serverapi.RuntimeLiveWatchResponse, 1)
	watchErr := make(chan error, 1)
	go func() {
		response, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
		watchDone <- response
		watchErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	attention.events <- clientui.AttentionNotificationEvent{Sequence: 1, Type: clientui.AttentionNotificationEventPending}

	select {
	case response := <-watchDone:
		if err := <-watchErr; err != nil {
			t.Fatalf("LiveWatch: %v", err)
		}
		if len(response.Outcomes) != 1 || response.Outcomes[0].Question == nil {
			t.Fatalf("response = %+v, want question", response)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for later question")
	}
	_, _ = service.LiveStop(context.Background(), serverapi.RuntimeLiveStopRequest{
		ClientRequestID: "6859fdfa-6808-4109-a031-de3d432e88dd",
		SessionID:       store.Meta().SessionID,
	})
	close(client.release)
	<-submitDone
}

func TestObservationLiveRunResponseProjectsTerminalFacts(t *testing.T) {
	const sessionID = "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	final := "final"
	response, err := observationLiveRunResponse(sessionID, "session", runtime.LiveRunResult{
		AssistantMessage: llm.Message{Content: &final},
		StartedAt:        time.Unix(1, 0),
		FinishedAt:       time.Unix(2, 0),
	}, nil)
	if err != nil || response.Outcomes[0].Kind != serverapi.RuntimeObservationOutcomeFinalAnswer {
		t.Fatalf("final response=%+v err=%v", response, err)
	}
	response, err = observationLiveRunResponse(sessionID, "session", runtime.LiveRunResult{
		Status: runtime.RunStatusInterrupted,
	}, context.Canceled)
	if err != nil || response.Outcomes[0].Kind != serverapi.RuntimeObservationOutcomeInterrupted {
		t.Fatalf("interrupted response=%+v err=%v", response, err)
	}
	response, err = observationLiveRunResponse(sessionID, "session", runtime.LiveRunResult{}, errors.New("failed"))
	if err != nil || response.Outcomes[0].Kind != serverapi.RuntimeObservationOutcomeExecutionError {
		t.Fatalf("error response=%+v err=%v", response, err)
	}
}
