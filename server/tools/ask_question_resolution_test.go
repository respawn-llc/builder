package tools

import (
	"context"
	"testing"
)

func TestAskQuestionBrokerPreservesLosslessLegacyQuestionResolution(t *testing.T) {
	answer := "  legacy answer  "
	freeform := "  legacy freeform  "
	broker := NewAskQuestionBroker()
	broker.SetAskHandler(func(context.Context, AskQuestionRequest) (AskQuestionResolution, error) {
		return AskQuestionLegacyAnswer{
			Answer:         &answer,
			FreeformAnswer: &freeform,
		}, nil
	})

	resolution, err := broker.Ask(
		context.Background(),
		AskQuestionRequest{ID: "question-1", Question: "Proceed?"},
	)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	legacy, ok := resolution.(AskQuestionLegacyAnswer)
	if !ok {
		t.Fatalf("resolution type = %T, want AskQuestionLegacyAnswer", resolution)
	}
	if legacy.Answer == nil || *legacy.Answer != answer {
		t.Fatalf("legacy answer = %v, want exact submitted value", legacy.Answer)
	}
	if legacy.FreeformAnswer == nil || *legacy.FreeformAnswer != freeform {
		t.Fatalf("legacy freeform = %v, want exact submitted value", legacy.FreeformAnswer)
	}
}

func TestValidateAskQuestionResolutionUsesTypedOptionalText(t *testing.T) {
	selected := 1
	req := AskQuestionRequest{
		ID:          "question-1",
		Question:    "Proceed?",
		Suggestions: []string{"yes"},
	}
	if err := ValidateAskQuestionResolution(req, AskQuestionAnswer{
		SelectedOptionNumber: &selected,
	}); err != nil {
		t.Fatalf("selected-only Question resolution: %v", err)
	}
	if err := ValidateAskQuestionResolution(req, AskQuestionAnswer{}); err == nil {
		t.Fatal("empty typed Question resolution unexpectedly validated")
	}
}
