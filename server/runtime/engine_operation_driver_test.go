package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
)

func TestWorkflowTurnActiveStepAdmissionHookFiresOnce(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
	}}}, tools.NewRegistry(), Config{
		Model:                "gpt-5",
		CurrentNodeExecution: testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool),
	})

	admissions := 0
	_, _ = engine.SubmitWorkflowTurnWithActiveHook(context.Background(), func() {
		admissions++
	})
	if admissions != 1 {
		t.Fatalf("active-step admissions = %d, want 1", admissions)
	}
}
