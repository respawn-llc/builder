package promptcontrol

import (
	"context"
	"testing"
	"time"

	"core/server/registry"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type stubAskPendingPromptSource struct {
	items []registry.PendingPromptSnapshot
}

func (s *stubAskPendingPromptSource) ListPendingPrompts(string) []registry.PendingPromptSnapshot {
	return append([]registry.PendingPromptSnapshot(nil), s.items...)
}

func TestServiceListsPendingAsksBySession(t *testing.T) {
	now := time.Now().UTC()
	stepID := promptViewStepID(t)
	svc := NewAskViewService(&stubAskPendingPromptSource{items: []registry.PendingPromptSnapshot{
		{Request: askquestion.AskQuestionRequest{ID: "ask-1", StepID: stepID.String(), Question: "one?", Suggestions: []string{"a", "b"}, RecommendedOptionIndex: 2}, CreatedAt: now},
		{Request: askquestion.AskQuestionRequest{ID: "approval-1", StepID: stepID.String(), Question: "allow?", Approval: true}, CreatedAt: now.Add(time.Second)},
	}})

	resp, err := svc.ListPendingAsksBySession(context.Background(), serverapi.AskListPendingBySessionRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListPendingAsksBySession: %v", err)
	}
	if len(resp.Asks) != 1 {
		t.Fatalf("expected one pending ask, got %+v", resp)
	}
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if resp.Asks[0].PromptID != clientui.PromptID("ask-1") ||
		resp.Asks[0].SessionID != sessionID ||
		resp.Asks[0].StepID != stepID ||
		resp.Asks[0].RecommendedOptionIndex == nil ||
		*resp.Asks[0].RecommendedOptionIndex != 2 {
		t.Fatalf("unexpected pending ask: %+v", resp.Asks[0])
	}
}

func TestAskViewServiceRejectsMalformedPendingPromptIdentity(t *testing.T) {
	for name, request := range map[string]askquestion.AskQuestionRequest{
		"prompt": {ID: " ask-1", StepID: promptViewStepID(t).String(), Question: "one?"},
		"step":   {ID: "ask-1", StepID: "step-1", Question: "one?"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewAskViewService(&stubAskPendingPromptSource{items: []registry.PendingPromptSnapshot{{Request: request}}})
			if _, err := svc.ListPendingAsksBySession(
				context.Background(),
				serverapi.AskListPendingBySessionRequest{SessionID: "session-1"},
			); err == nil {
				t.Fatal("accepted malformed pending prompt identity")
			}
		})
	}
}

func TestServiceEncodesAbsentPendingAskRecommendationAsNil(t *testing.T) {
	svc := NewAskViewService(&stubAskPendingPromptSource{items: []registry.PendingPromptSnapshot{{
		Request: askquestion.AskQuestionRequest{
			ID:          "ask-1",
			StepID:      promptViewStepID(t).String(),
			Question:    "one?",
			Suggestions: []string{"a"},
		},
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
	svc := NewAskViewService(&stubAskPendingPromptSource{items: []registry.PendingPromptSnapshot{{
		Request: askquestion.AskQuestionRequest{
			ID:                     "ask-1",
			StepID:                 promptViewStepID(t).String(),
			Question:               "one?",
			Suggestions:            []string{"a"},
			RecommendedOptionIndex: 2,
		},
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

func promptViewStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return id
}
