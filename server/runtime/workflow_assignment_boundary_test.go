package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestQueuedWorkflowAssignmentCommitsBeforeToolFollowUpModelTurn(t *testing.T) {
	client := &fakeClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Phase:   textutil.Value(llm.MessagePhaseCommentary),
					Content: textutil.Value("transitioning"),
				},
				ToolCalls: []llm.ToolCall{{
					ID:    "transition",
					Name:  string(toolspec.ToolExecCommand),
					Input: json.RawMessage(`{"cmd":"kent task complete"}`),
				}},
				Usage: llm.Usage{WindowTokens: 200_000},
			},
			{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Phase:   textutil.Value(llm.MessagePhaseCommentary),
					Content: textutil.Value("done"),
				},
				ToolCalls: []llm.ToolCall{completeNodeCall(
					"complete",
					json.RawMessage(`{"transition":"done","summary":"done"}`),
				)},
				Usage: llm.Usage{WindowTokens: 200_000},
			},
		},
	}
	targetReference, err := workflow.NewCurrentNodeReference("task", "target", nil)
	if err != nil {
		t.Fatalf("target reference: %v", err)
	}
	targetAssignment := WorkflowAssignment{
		ContextMode:    workflow.ContextModeContinueSession,
		CompletionMode: workflowruntime.CompletionModeTool,
		Prompt: workflowruntime.PromptContract{
			Identity:       workflowruntime.CurrentNodePromptIdentity(targetReference),
			CompletionMode: workflowruntime.CompletionModeTool,
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode: targetReference,
				WorkflowID:  runtimeids.NewWorkflowID(),
			},
		},
	}
	assignmentTool := &queueWorkflowAssignmentTool{assignment: targetAssignment}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: assignmentTool,
		}),
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
			CurrentNodeExecution: testWorkflowConfig(
				&externallyCompletedWorkflowController{},
				config.WorkflowCompletionModeTool,
			),
		},
	)
	assignmentTool.engine = engine

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}

	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want two", len(requests))
	}
	foundTarget := false
	for _, item := range requests[1].Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeWorkflowMode &&
			item.SourcePath != nil &&
			*item.SourcePath == targetAssignment.Prompt.Identity {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Fatal("tool follow-up model turn omitted queued workflow assignment")
	}
}

type queueWorkflowAssignmentTool struct {
	engine     *Engine
	assignment WorkflowAssignment
}

func (t *queueWorkflowAssignmentTool) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	_, err := t.engine.SteerWorkflowAssignment(t.assignment)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"scheduled":true}`),
	}, nil
}
