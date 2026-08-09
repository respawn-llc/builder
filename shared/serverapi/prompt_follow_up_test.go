package serverapi

import (
	"testing"

	"core/shared/runtimeids"
)

func TestPromptFollowUpContractCarriesOnlyFullIdentityAndTerminalOutcome(t *testing.T) {
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse Step ID: %v", err)
	}
	if err := (PromptFollowUpWatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    stepID,
		PromptID:  "prompt-1",
	}).Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}
	for _, kind := range []PromptFollowUpEventKind{
		PromptFollowUpSuccessorReady,
		PromptFollowUpNoPreparedSuccessor,
		PromptFollowUpExecutionClosed,
	} {
		if err := (PromptFollowUpEvent{Kind: kind}).Validate(); err != nil {
			t.Fatalf("validate %q: %v", kind, err)
		}
	}
	if err := (PromptFollowUpEvent{}).Validate(); err == nil {
		t.Fatal("missing terminal outcome unexpectedly validated")
	}
}
