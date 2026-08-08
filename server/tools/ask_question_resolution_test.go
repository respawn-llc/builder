package tools

import (
	"context"
	"testing"
)

func TestAskQuestionBrokerPreservesCanonicalQuestionResolution(t *testing.T) {
	freeform := "  exact freeform  "
	broker := NewAskQuestionBroker()
	broker.SetAskHandler(func(context.Context, AskQuestionRequest) (AskQuestionResolution, error) {
		return AskQuestionAnswer{Freeform: &freeform}, nil
	})

	resolution, err := broker.Ask(
		context.Background(),
		AskQuestionRequest{ID: "question-1", Question: "Proceed?"},
	)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer, ok := resolution.(AskQuestionAnswer)
	if !ok {
		t.Fatalf("resolution type = %T, want AskQuestionAnswer", resolution)
	}
	if answer.Freeform == nil || *answer.Freeform != freeform {
		t.Fatalf("freeform = %v, want exact submitted value", answer.Freeform)
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

func TestQuestionResolutionFormattingPreservesAbsentOptionalText(t *testing.T) {
	selected := 1
	text, err := resolutionQuestionText(AskQuestionAnswer{SelectedOptionNumber: &selected})
	if err != nil {
		t.Fatalf("resolutionQuestionText: %v", err)
	}
	if text.freeform != nil {
		t.Fatalf("freeform = %q, want absent", *text.freeform)
	}
}
