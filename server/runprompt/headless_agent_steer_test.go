package runprompt

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestAgentSteerForRunPromptOnlyRelabelsExistingSessionContinuation(t *testing.T) {
	sourceID := runtimeids.NewSessionID().String()
	caller := sourceID
	req := serverapi.RunPromptRequest{CallerSessionID: &caller, Prompt: "continue"}
	steer, err := agentSteerForRunPrompt(req, true)
	if err != nil {
		t.Fatalf("agentSteerForRunPrompt existing: %v", err)
	}
	if steer == nil || steer.Message().MessageType == nil {
		t.Fatalf("existing continuation steer = %+v, want typed message", steer)
	}
	for name, openingExisting := range map[string]bool{"human existing": true, "new Session": false} {
		t.Run(name, func(t *testing.T) {
			branch := req
			if name == "human existing" {
				branch.CallerSessionID = nil
			}
			got, err := agentSteerForRunPrompt(branch, openingExisting)
			if err != nil {
				t.Fatalf("agentSteerForRunPrompt: %v", err)
			}
			if got != nil {
				t.Fatalf("steer = %+v, want ordinary user prompt", got)
			}
		})
	}
}
