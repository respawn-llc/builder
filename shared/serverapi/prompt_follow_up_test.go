package serverapi

import (
	"reflect"
	"testing"

	"core/shared/protocol"
)

func TestPromptFollowUpContractCarriesOnlyFullIdentityAndTerminalOutcome(t *testing.T) {
	for contract, fields := range map[reflect.Type][]string{
		reflect.TypeOf(PromptFollowUpWatchRequest{}):         {"SessionID", "StepID", "PromptID"},
		reflect.TypeOf(PromptFollowUpEvent{}):                {"Kind"},
		reflect.TypeOf(protocol.PromptFollowUpEventParams{}): {"Event"},
		reflect.TypeOf(protocol.PromptFollowUpEvent{}):       {"Kind"},
	} {
		if contract.NumField() != len(fields) {
			t.Fatalf("%s fields = %+v, want %v", contract.Name(), reflect.VisibleFields(contract), fields)
		}
		for _, field := range fields {
			if _, exists := contract.FieldByName(field); !exists {
				t.Fatalf("%s missing field %s", contract.Name(), field)
			}
		}
	}
	if field := reflect.TypeOf(PromptFollowUpEvent{}).Field(0); field.Type != reflect.TypeOf(PromptFollowUpEventKind("")) {
		t.Fatalf("follow-up event Kind type = %v", field.Type)
	}
	request := PromptFollowUpWatchRequest{SessionID: mustPromptBatchSessionID(t), StepID: mustPromptBatchStepID(t), PromptID: "prompt-1"}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}
	for _, event := range []PromptFollowUpEvent{{Kind: PromptFollowUpSuccessorReady}, {Kind: PromptFollowUpNoPreparedSuccessor}, {Kind: PromptFollowUpExecutionClosed}, {}} {
		if err := event.Validate(); (err == nil) != (event.Kind != "") {
			t.Fatalf("validate %q: %v", event.Kind, err)
		}
	}
}
