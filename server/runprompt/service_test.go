package runprompt

import (
	"testing"

	"core/shared/serverapi"
)

func TestPrepareRunPromptRequestDoesNotRetainReceiptPayload(t *testing.T) {
	callerSessionID := "caller-session"
	agentRole := "coder_high"
	receiptPayload := serverapi.RunPromptRequest{
		ClientRequestID: "  request-id  ",
		CallerSessionID: &callerSessionID,
		Prompt:          "  prompt  ",
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &agentRole},
	}

	prepared := prepareRunPromptRequest(receiptPayload)
	callerSessionID = "mutated-caller"
	agentRole = "mutated-role"

	if prepared.ClientRequestID != "request-id" || prepared.Prompt != "prompt" {
		t.Fatalf("canonical request = %+v, want trimmed ID and Prompt", prepared)
	}
	if prepared.CallerSessionID == nil || *prepared.CallerSessionID != "caller-session" {
		t.Fatalf("prepared caller session ID = %v, want isolated original value", prepared.CallerSessionID)
	}
	if prepared.Overrides.AgentRole == nil || *prepared.Overrides.AgentRole != "coder_high" {
		t.Fatalf("prepared agent role = %v, want isolated original value", prepared.Overrides.AgentRole)
	}
	if prepared.CallerSessionID == receiptPayload.CallerSessionID {
		t.Fatal("prepared caller session ID retains receipt payload pointer")
	}
	if prepared.Overrides.AgentRole == receiptPayload.Overrides.AgentRole {
		t.Fatal("prepared agent role retains receipt payload pointer")
	}
}
