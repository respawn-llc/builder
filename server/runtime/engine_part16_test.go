package runtime

import (
	"context"
	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoCompactionDoesNotRetryNonOverflow400(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
				},
				Usage: llm.Usage{InputTokens: 390000, OutputTokens: 1000, WindowTokens: 400000},
			},
		},
		compactionErrors: []error{
			&llm.APIStatusError{StatusCode: 400, Body: `{"error":{"type":"invalid_request_error","code":"invalid_tool_arguments","message":"tool arguments must be an object"}}`},
			nil,
		},
		compactionResponses: []llm.CompactionResponse{
			{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("run tools")},
					{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
				},
				Usage: llm.Usage{InputTokens: 8000, OutputTokens: 500, WindowTokens: 400000},
			},
		},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5.3-codex"})

	if _, err := eng.SubmitUserMessage(context.Background(), "run tools"); err == nil {
		t.Fatal("expected compaction to fail on non-overflow 400")
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("expected one compact call for non-overflow 400, got %d", len(client.compactionCalls))
	}
}

func TestAutoCompactionRetries413ByCollapsingShellOutput(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
				},
				Usage: llm.Usage{InputTokens: 390000, OutputTokens: 1000, WindowTokens: 400000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
				Usage:     llm.Usage{InputTokens: 2000, OutputTokens: 500, WindowTokens: 400000},
			},
		},
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 413, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "payload too large"},
			nil,
		},
		compactionResponses: []llm.CompactionResponse{
			{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("run tools")},
					{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
				},
				Usage: llm.Usage{InputTokens: 8000, OutputTokens: 500, WindowTokens: 400000},
			},
		},
	}

	largeOutput := json.RawMessage(`{"output":"` + strings.Repeat("x", 120_000) + `"}`)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand, out: largeOutput}}), Config{Model: "gpt-5.3-codex"})

	msg, err := eng.SubmitUserMessage(context.Background(), "run tools")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("expected two compact calls (retry after 413), got %d", len(client.compactionCalls))
	}
	if len(client.compactionCalls[1].InputItems) != len(client.compactionCalls[0].InputItems) {
		t.Fatalf("expected repair to preserve item count, first=%d second=%d", len(client.compactionCalls[0].InputItems), len(client.compactionCalls[1].InputItems))
	}
	foundCollapsed := false
	for _, item := range client.compactionCalls[1].InputItems {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID != nil && *item.CallID == "call_1" {
			foundCollapsed = isCollapsedCompactionOverflowShellOutput(item.Output)
		}
	}
	if !foundCollapsed {
		t.Fatalf("expected repaired retry to collapse shell output, got %+v", client.compactionCalls[1].InputItems)
	}
}

func TestOpenAIModelCompact404DoesNotFallbackToLocalCompaction(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
				},
				Usage: llm.Usage{InputTokens: 190000, OutputTokens: 2000, WindowTokens: 200000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
				Usage:     llm.Usage{InputTokens: 8000, OutputTokens: 1000, WindowTokens: 200000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
				Usage:     llm.Usage{InputTokens: 4000, OutputTokens: 1000, WindowTokens: 200000},
			},
		},
		compactionErr: &llm.APIStatusError{StatusCode: 404, Body: "not found"},
		caps: llm.ProviderCapabilities{
			ProviderID:                    "openai",
			SupportsResponsesAPI:          true,
			SupportsResponsesCompact:      true,
			SupportsReasoningEncrypted:    true,
			SupportsServerSideContextEdit: true,
			IsOpenAIFirstParty:            true,
		},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "run tools")
	if err == nil {
		t.Fatalf("expected compaction error, got success message %+v", msg)
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("expected one compact call, got %d", len(client.compactionCalls))
	}
	for _, req := range client.calls {
		for _, item := range req.Items {
			if item.Type == llm.ResponseItemTypeMessage && item.MessageType != nil && *item.MessageType == llm.MessageTypeCompactionSummary {
				t.Fatalf("did not expect local compaction summary fallback, request=%+v", req.Items)
			}
		}
	}
}
