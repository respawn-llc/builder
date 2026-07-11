package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
)

func TestWorkflowReasoningOnlyResponseContinuesWithoutFeedback(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	completionTool := &externalCompletionTool{controller: controller}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role: llm.RoleAssistant,
				ReasoningItems: []llm.ReasoningItem{{
					ID:               "rs_1",
					EncryptedContent: "encrypted-reasoning",
				}},
			},
			ReasoningItems: []llm.ReasoningItem{{
				ID:               "rs_1",
				EncryptedContent: "encrypted-reasoning",
			}},
			OutputItems: []llm.ResponseItem{{
				Type:             llm.ResponseItemTypeReasoning,
				ID:               "rs_1",
				EncryptedContent: "encrypted-reasoning",
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		commentaryResponse("complete the workflow", llm.ToolCall{
			ID:    "call_complete",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"kent task complete"}`),
		}),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: completionTool,
	}), Config{
		WorkflowRun: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
	})

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}

	assertModelCallCount(t, client, 2)
	if got := controller.violations.Load(); got != 0 {
		t.Fatalf("workflow violations = %d, want 0", got)
	}
	reasoningPersisted := false
	for _, item := range client.calls[1].Items {
		if item.Type == llm.ResponseItemTypeReasoning && item.ID == "rs_1" && item.EncryptedContent == "encrypted-reasoning" {
			reasoningPersisted = true
		}
	}
	if !reasoningPersisted {
		t.Fatalf("continuation request omitted reasoning output: %+v", client.calls[1].Items)
	}
	for _, message := range requestMessages(client.calls[1]) {
		if message.Role == llm.RoleDeveloper && message.MessageType == llm.MessageTypeErrorFeedback {
			t.Fatalf("reasoning-only response added feedback: %+v", message)
		}
	}
}

func TestWorkflowEmptyFinalResponseUsesGenericEmptyFinalFeedback(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	completionTool := &externalCompletionTool{controller: controller}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   llm.MessagePhaseFinal,
				Content: "",
			},
			OutputItems: []llm.ResponseItem{{
				Type:    llm.ResponseItemTypeMessage,
				Role:    llm.RoleAssistant,
				Phase:   llm.MessagePhaseFinal,
				Content: "",
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		commentaryResponse("complete the workflow", llm.ToolCall{
			ID:    "call_complete",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"kent task complete"}`),
		}),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: completionTool,
	}), Config{
		WorkflowRun: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
	})

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}

	assertModelCallCount(t, client, 2)
	if got := controller.violations.Load(); got != 0 {
		t.Fatalf("workflow violations = %d, want 0", got)
	}
	if !requestHasDeveloperErrorFeedback(client.calls[1]) {
		t.Fatalf("empty final did not add generic developer feedback")
	}
}
