package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestWorkflowReasoningOnlyResponseContinuesWithoutFeedback(t *testing.T) {
	t.Parallel()
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
				ID:               textutil.Value("rs_1"),
				EncryptedContent: textutil.Value("encrypted-reasoning"),
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
		CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
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
		if item.Type == llm.ResponseItemTypeReasoning &&
			item.ID != nil && *item.ID == "rs_1" &&
			item.EncryptedContent != nil && *item.EncryptedContent == "encrypted-reasoning" {
			reasoningPersisted = true
		}
	}
	if !reasoningPersisted {
		t.Fatalf("continuation request omitted reasoning output: %+v", client.calls[1].Items)
	}
	for _, message := range requestMessages(client.calls[1]) {
		if message.Role == llm.RoleDeveloper && message.MessageType != nil && *message.MessageType == llm.MessageTypeErrorFeedback {
			t.Fatalf("reasoning-only response added feedback: %+v", message)
		}
	}
}

func TestReasoningOnlyBoundaryFlushesQueuedBackgroundNoticeBeforeNextDispatch(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	completionTool := &externalCompletionTool{controller: controller}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role: llm.RoleAssistant,
				ReasoningItems: []llm.ReasoningItem{{
					ID:               "rs_background",
					EncryptedContent: "encrypted-reasoning",
				}},
			},
			ReasoningItems: []llm.ReasoningItem{{
				ID:               "rs_background",
				EncryptedContent: "encrypted-reasoning",
			}},
			OutputItems: []llm.ResponseItem{{
				Type:             llm.ResponseItemTypeReasoning,
				ID:               textutil.Value("rs_background"),
				EncryptedContent: textutil.Value("encrypted-reasoning"),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		commentaryResponse("complete the workflow", llm.ToolCall{
			ID:    "call_complete_background",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"kent task complete"}`),
		}),
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: completionTool,
	}), Config{
		CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
	})
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: engine,
		steps:  &stubExclusiveStepLifecycle{busy: true},
	}
	engine.backgroundFlow = scheduler
	scheduler.QueueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Name:    textutil.Value("background-1"),
		Content: textutil.Value("background completed"),
	})

	if _, err := engine.runStepLoop(t.Context(), "reasoning-background"); err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if scheduler.HasPendingNotices() {
		t.Fatal("reasoning-only boundary left queued background notice pending")
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want reasoning-only continuation after one boundary", len(client.calls))
	}
	foundNotice := false
	for _, message := range requestMessages(client.calls[1]) {
		if message.Role == llm.RoleDeveloper && message.Name != nil && *message.Name == "background-1" {
			foundNotice = true
			break
		}
	}
	if !foundNotice {
		t.Fatalf("next request omitted flushed background notice: %+v", requestMessages(client.calls[1]))
	}
}

func TestWorkflowEmptyFinalResponseUsesGenericEmptyFinalFeedback(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	completionTool := &externalCompletionTool{controller: controller}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:  llm.RoleAssistant,
				Phase: textutil.Value(llm.MessagePhaseFinal),
			},
			OutputItems: []llm.ResponseItem{{
				Type:    llm.ResponseItemTypeMessage,
				Role:    textutil.Value(llm.RoleAssistant),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value(""),
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
		CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
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
