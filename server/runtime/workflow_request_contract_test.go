package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/toolspec"
)

func TestWorkflowToolModeAdvertisesCompleteNodeWithRequiredChoice(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		&workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		},
	)

	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build workflow request: %v", err)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeRequired {
		t.Fatalf("workflow tool choice mode = %q, want required", request.ToolChoiceMode)
	}

	advertised := make(map[toolspec.ID]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		id, ok := toolspec.ParseID(tool.Name)
		if !ok {
			t.Fatalf("workflow request advertised unknown tool: %+v", tool)
		}
		advertised[id] = struct{}{}
	}
	if len(advertised) != 2 {
		t.Fatalf("workflow request advertised tools = %v, want ask_question and complete_node", request.Tools)
	}
	for _, id := range []toolspec.ID{toolspec.ToolAskQuestion, toolspec.ToolCompleteNode} {
		if _, ok := advertised[id]; !ok {
			t.Fatalf("workflow request omitted tool %q: %+v", id, request.Tools)
		}
	}
}
