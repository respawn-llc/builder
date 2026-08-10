package serverapi

import "testing"

func TestPromptFollowUpContractCarriesOnlyFullIdentityAndTerminalOutcome(t *testing.T) {
	if err := (PromptFollowUpWatchRequest{
		SessionID: mustPromptBatchSessionID(t), StepID: mustPromptBatchStepID(t), PromptID: "prompt-1",
	}).Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}
	for _, event := range []PromptFollowUpEvent{{Kind: PromptFollowUpSuccessorReady}, {Kind: PromptFollowUpNoPreparedSuccessor}, {Kind: PromptFollowUpExecutionClosed}, {}} {
		if err := event.Validate(); (err == nil) != (event.Kind != "") {
			t.Fatalf("validate %q: %v", event.Kind, err)
		}
	}
}
