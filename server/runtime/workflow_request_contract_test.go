package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
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

func TestNonWorkflowRequestOmitsCompleteNodeWithAutomaticChoice(t *testing.T) {
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolCompleteNode},
		},
	)

	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build non-workflow request: %v", err)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("non-workflow tool choice mode = %q, want automatic", request.ToolChoiceMode)
	}
	if len(request.Tools) != 0 {
		t.Fatalf("non-workflow request advertised workflow-only tools: %+v", request.Tools)
	}
}

func TestShellWorkflowUsesNativeWebSearchAsRequiredToolChoice(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		&workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionModeShellCommand,
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{
			Model:         "gpt-5",
			EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
			WebSearchMode: "native",
		},
	)

	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build shell workflow request: %v", err)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeRequired ||
		!request.EnableNativeWebSearch ||
		len(request.Tools) != 0 {
		t.Fatalf("shell workflow tool policy = %+v, want required native-only web search", request)
	}
}

func TestShellWorkflowRejectsRequiredChoiceWithoutEffectiveTools(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	client := &fakeClient{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			WorkflowRun: &workflowruntime.Config{
				RunID:          runID,
				Contract:       workflowruntime.CompletionContract{RunID: runID},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     &externallyCompletedWorkflowController{},
			},
		},
	)

	if _, err := engine.buildRequest(context.Background(), "step", true); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("build shell workflow request error = %v, want invalid request", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("shell workflow invalid request dispatched provider calls = %d", len(client.calls))
	}
}
