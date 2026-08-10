package serverapi

import (
	"reflect"
	"testing"
)

func TestPromptFollowUpContractCarriesOnlyFullIdentityAndTerminalOutcome(t *testing.T) {
	eventType := reflect.TypeOf(PromptFollowUpEvent{})
	if eventType.NumField() != 1 || eventType.Field(0).Name != "Kind" || eventType.Field(0).Type != reflect.TypeOf(PromptFollowUpEventKind("")) {
		t.Fatalf("follow-up event fields = %+v, want only typed Kind", reflect.VisibleFields(eventType))
	}
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
