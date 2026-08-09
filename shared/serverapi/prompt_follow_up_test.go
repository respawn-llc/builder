package serverapi

import "testing"

func TestPromptFollowUpContractCarriesOnlyFullIdentityAndTerminalOutcome(t *testing.T) {
	if err := (PromptFollowUpWatchRequest{
		SessionID: mustPromptBatchSessionID(t), StepID: mustPromptBatchStepID(t), PromptID: "prompt-1",
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
