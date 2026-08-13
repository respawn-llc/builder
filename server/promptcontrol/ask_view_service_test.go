package promptcontrol

import (
	"context"
	"testing"
	"time"

	"core/server/registry"
	askquestion "core/server/tools"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const promptViewStepID = "11111111-1111-4111-8111-111111111111"

type stubAskPendingPromptSource struct {
	items []registry.PendingPromptSnapshot
}

func (s *stubAskPendingPromptSource) ListPendingPrompts(string) []registry.PendingPromptSnapshot {
	return append([]registry.PendingPromptSnapshot(nil), s.items...)
}

func TestServiceListsPendingAsksBySession(t *testing.T) {
	now := time.Now().UTC()
	stepID, err := runtimeids.ParseStepID(promptViewStepID)
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	svc := NewAskViewService(&stubAskPendingPromptSource{items: []registry.PendingPromptSnapshot{
		{Request: askquestion.AskQuestionRequest{ID: "ask-1", StepID: promptViewStepID, Question: "one?", Suggestions: []string{"a", "b"}, RecommendedOptionIndex: 2}, PromptID: "ask-1", StepID: stepID, CreatedAt: now},
		{Request: askquestion.AskQuestionRequest{ID: "approval-1", StepID: promptViewStepID, Question: "allow?", Approval: true}, PromptID: "approval-1", StepID: stepID, CreatedAt: now.Add(time.Second)},
	}})

	resp, err := svc.ListPendingAsksBySession(context.Background(), serverapi.AskListPendingBySessionRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListPendingAsksBySession: %v", err)
	}
	if len(resp.Asks) != 1 {
		t.Fatalf("expected one pending ask, got %+v", resp)
	}
	if resp.Asks[0].PromptID != clientui.PromptID("ask-1") ||
		resp.Asks[0].SessionID.String() != "session-1" ||
		resp.Asks[0].StepID.String() != promptViewStepID ||
		resp.Asks[0].RecommendedOptionIndex == nil ||
		*resp.Asks[0].RecommendedOptionIndex != 2 {
		t.Fatalf("unexpected pending ask: %+v", resp.Asks[0])
	}
}

func TestServiceEncodesAbsentPendingAskRecommendationAsNil(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(promptViewStepID)
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	svc := NewAskViewService(&stubAskPendingPromptSource{items: []registry.PendingPromptSnapshot{{
		Request: askquestion.AskQuestionRequest{
			ID:          "ask-1",
			StepID:      promptViewStepID,
			Question:    "one?",
			Suggestions: []string{"a"},
		},
		PromptID: "ask-1",
		StepID:   stepID,
	}}})

	resp, err := svc.ListPendingAsksBySession(
		context.Background(),
		serverapi.AskListPendingBySessionRequest{SessionID: "session-1"},
	)
	if err != nil {
		t.Fatalf("ListPendingAsksBySession: %v", err)
	}
	if len(resp.Asks) != 1 || resp.Asks[0].RecommendedOptionIndex != nil {
		t.Fatalf("pending asks = %+v, want absent recommendation", resp.Asks)
	}
}

func TestServiceRejectsInvalidPendingAskRecommendation(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(promptViewStepID)
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	svc := NewAskViewService(&stubAskPendingPromptSource{items: []registry.PendingPromptSnapshot{{
		Request: askquestion.AskQuestionRequest{
			ID:                     "ask-1",
			StepID:                 promptViewStepID,
			Question:               "one?",
			Suggestions:            []string{"a"},
			RecommendedOptionIndex: 2,
		},
		PromptID: "ask-1",
		StepID:   stepID,
	}}})

	if _, err := svc.ListPendingAsksBySession(
		context.Background(),
		serverapi.AskListPendingBySessionRequest{SessionID: "session-1"},
	); err == nil {
		t.Fatal("accepted pending ask recommendation outside suggestions")
	}
}

func TestAskViewServiceRequiresSessionID(t *testing.T) {
	if _, err := NewAskViewService(&stubAskPendingPromptSource{}).ListPendingAsksBySession(context.Background(), serverapi.AskListPendingBySessionRequest{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestAskViewServiceConsumesValidatedRequest(t *testing.T) {
	service := NewAskViewService(&stubAskPendingPromptSource{})
	request := serverapi.AskListPendingBySessionRequest{SessionID: "session-1"}
	_, err := apicontract.WithValidated(
		request,
		apicontract.SemanticValidationRequired,
		func(validated apicontract.Validated[serverapi.AskListPendingBySessionRequest]) (struct{}, error) {
			_, err := service.ListPendingAsksBySessionValidated(context.Background(), validated)
			return struct{}{}, err
		},
	)
	if err != nil {
		t.Fatalf("ListPendingAsksBySessionValidated: %v", err)
	}
}
