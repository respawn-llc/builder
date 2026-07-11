package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
)

func openAIFirstPartyNativeWebSearchCaps() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsNativeWebSearch:       true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}
}

func TestSetReviewerEnabledConcurrentWithBusyStep(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_patch_1", Name: string(toolspec.ToolPatch), Custom: true, CustomInput: "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch"}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch, delay: 50 * time.Millisecond}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "off",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			ClientFactory: func() (llm.Client, error) {
				return reviewerClient, nil
			},
		},
	})

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "edit file")
		submitDone <- submitErr
	}()

	time.Sleep(10 * time.Millisecond)
	if _, _, err := eng.SetReviewerEnabled(true); err != nil {
		t.Fatalf("enable reviewer while busy: %v", err)
	}

	if err := <-submitDone; err != nil {
		t.Fatalf("submit while enabling reviewer: %v", err)
	}
	if got := eng.ReviewerFrequency(); got != "edits" {
		t.Fatalf("reviewer frequency after concurrent enable = %q, want edits", got)
	}
	if got := len(reviewerClient.calls); got != 1 {
		t.Fatalf("expected reviewer to run for in-flight step after concurrent enable, got %d calls", got)
	}
}

func TestSetReviewerDisabledConcurrentWithBusyStepSkipsReviewerForCurrentRun(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_patch_1", Name: string(toolspec.ToolPatch), Custom: true, CustomInput: "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch"}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch, delay: 50 * time.Millisecond}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "edit file")
		submitDone <- submitErr
	}()

	time.Sleep(10 * time.Millisecond)
	if _, _, err := eng.SetReviewerEnabled(false); err != nil {
		t.Fatalf("disable reviewer while busy: %v", err)
	}

	if err := <-submitDone; err != nil {
		t.Fatalf("submit while disabling reviewer: %v", err)
	}
	if got := eng.ReviewerFrequency(); got != "off" {
		t.Fatalf("reviewer frequency after concurrent disable = %q, want off", got)
	}
	if got := len(reviewerClient.calls); got != 0 {
		t.Fatalf("expected reviewer to be skipped for in-flight step after concurrent disable, got %d calls", got)
	}
}

func TestHostedWebSearchExecutionFromOutputItem(t *testing.T) {
	item := llm.ResponseItem{
		Type: llm.ResponseItemTypeOther,
		Raw: json.RawMessage(`{
			"type":"web_search_call",
			"id":"ws_1",
			"status":"completed",
			"action":{"type":"search","query":"kent cli"}
		}`),
	}

	executions := hostedToolExecutionsFromOutputItems([]llm.ResponseItem{item}, tools.DefinitionsFor([]toolspec.ID{toolspec.ToolWebSearch}))
	if len(executions) != 1 {
		t.Fatal("expected hosted web search execution")
	}
	execution := executions[0]
	if execution.Call.Name != string(toolspec.ToolWebSearch) {
		t.Fatalf("unexpected hosted tool name: %+v", execution.Call)
	}
	if execution.Call.ID != "ws_1" {
		t.Fatalf("unexpected hosted call id: %+v", execution.Call)
	}
	var input map[string]string
	if err := json.Unmarshal(execution.Call.Input, &input); err != nil {
		t.Fatalf("decode hosted input: %v", err)
	}
	if input["query"] != "kent cli" {
		t.Fatalf("expected hosted query in input, got %+v", input)
	}
	if execution.Result.Name != toolspec.ToolWebSearch {
		t.Fatalf("unexpected hosted result tool name: %+v", execution.Result)
	}
	if execution.Result.IsError {
		t.Fatalf("expected hosted status completed to be non-error")
	}
}

func TestHostedWebSearchExecutionUsesURLAsQueryFallback(t *testing.T) {
	item := llm.ResponseItem{
		Type: llm.ResponseItemTypeOther,
		Raw: json.RawMessage(`{
			"type":"web_search_call",
			"id":"ws_2",
			"status":"completed",
			"action":{"type":"open_page","url":"https://example.com"}
		}`),
	}

	executions := hostedToolExecutionsFromOutputItems([]llm.ResponseItem{item}, tools.DefinitionsFor([]toolspec.ID{toolspec.ToolWebSearch}))
	if len(executions) != 1 {
		t.Fatal("expected hosted web search execution")
	}
	execution := executions[0]
	var input map[string]string
	if err := json.Unmarshal(execution.Call.Input, &input); err != nil {
		t.Fatalf("decode hosted input: %v", err)
	}
	if input["query"] != "https://example.com" {
		t.Fatalf("expected url fallback in query, got %+v", input)
	}
}

func TestHostedWebSearchExecutionRejectsWhitespaceSearchQuery(t *testing.T) {
	item := llm.ResponseItem{
		Type: llm.ResponseItemTypeOther,
		Raw: json.RawMessage(`{
			"type":"web_search_call",
			"id":"ws_3",
			"status":"completed",
			"action":{"type":"search","query":"   "}
		}`),
	}

	executions := hostedToolExecutionsFromOutputItems([]llm.ResponseItem{item}, tools.DefinitionsFor([]toolspec.ID{toolspec.ToolWebSearch}))
	if len(executions) != 1 {
		t.Fatal("expected hosted web search execution")
	}
	execution := executions[0]
	if !execution.Result.IsError {
		t.Fatalf("expected hosted whitespace query to fail, got %+v", execution.Result)
	}
	var output map[string]string
	if err := json.Unmarshal(execution.Result.Output, &output); err != nil {
		t.Fatalf("decode hosted output: %v", err)
	}
	if output["error"] != tools.InvalidWebSearchQueryMessage {
		t.Fatalf("expected invalid query error, got %+v", output)
	}
	var input map[string]string
	if err := json.Unmarshal(execution.Call.Input, &input); err != nil {
		t.Fatalf("decode hosted input: %v", err)
	}
	if _, ok := input["query"]; !ok {
		t.Fatalf("expected hosted input to preserve query field, got %+v", input)
	}
	if input["query"] != "" {
		t.Fatalf("expected hosted input query to stay empty, got %+v", input)
	}
}

func TestHostedWebSearchExecutionRejectsHallucinatedSearchQuery(t *testing.T) {
	item := llm.ResponseItem{
		Type: llm.ResponseItemTypeOther,
		Raw: json.RawMessage(`{
			"type":"web_search_call",
			"id":"ws_4",
			"status":"completed",
			"action":{"type":"search","query":"web search"}
		}`),
	}

	executions := hostedToolExecutionsFromOutputItems([]llm.ResponseItem{item}, tools.DefinitionsFor([]toolspec.ID{toolspec.ToolWebSearch}))
	if len(executions) != 1 {
		t.Fatal("expected hosted web search execution")
	}
	execution := executions[0]
	if !execution.Result.IsError {
		t.Fatalf("expected hosted hallucinated query to fail, got %+v", execution.Result)
	}
	var output map[string]string
	if err := json.Unmarshal(execution.Result.Output, &output); err != nil {
		t.Fatalf("decode hosted output: %v", err)
	}
	if output["error"] != tools.InvalidWebSearchQueryMessage {
		t.Fatalf("expected invalid query error, got %+v", output)
	}
	var input map[string]string
	if err := json.Unmarshal(execution.Call.Input, &input); err != nil {
		t.Fatalf("decode hosted input: %v", err)
	}
	if input["query"] != "web search" {
		t.Fatalf("expected hosted input to preserve normalized query, got %+v", input)
	}
}

func TestSubmitUserMessageContinuesAfterHostedToolOnlyTurn(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: ""},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}
	if !client.calls[0].EnableNativeWebSearch {
		t.Fatalf("expected first request to enable native web search")
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	hostedCompletionCount := 0
	for _, evt := range events {
		if evt.Kind != "tool_completed" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			t.Fatalf("decode tool_completed payload: %v", err)
		}
		name, _ := payload["name"].(string)
		if strings.TrimSpace(name) == string(toolspec.ToolWebSearch) {
			hostedCompletionCount++
		}
	}
	if hostedCompletionCount != 1 {
		t.Fatalf("expected one hosted web_search tool completion, got %d", hostedCompletionCount)
	}

	secondReq := client.calls[1]
	foundHostedOutput := false
	for _, item := range secondReq.Items {
		if item.Type != llm.ResponseItemTypeFunctionCallOutput {
			continue
		}
		if item.CallID == "ws_1" {
			foundHostedOutput = true
			break
		}
	}
	if !foundHostedOutput {
		t.Fatalf("expected hosted tool output item in follow-up request, got %+v", secondReq.Items)
	}
}

func TestSubmitUserMessageContinuesAfterInvalidHostedWebSearch(t *testing.T) {
	store := mustCreateTestSession(t)
	var hostedStart *llm.ToolCall

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: ""},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_invalid","status":"completed","action":{"type":"search","query":"web search"}}`),
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
		OnEvent: func(evt Event) {
			if evt.Kind == EventToolCallStarted && evt.ToolCall != nil && evt.ToolCall.ID == "ws_invalid" {
				call := *evt.ToolCall
				hostedStart = &call
			}
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}
	if !client.calls[0].EnableNativeWebSearch {
		t.Fatalf("expected first request to enable native web search")
	}

	foundHostedOutput := false
	for _, item := range client.calls[1].Items {
		if item.Type != llm.ResponseItemTypeFunctionCallOutput || item.CallID != "ws_invalid" {
			continue
		}
		foundHostedOutput = true
		var output map[string]string
		if err := json.Unmarshal(item.Output, &output); err != nil {
			t.Fatalf("decode hosted tool output: %v", err)
		}
		if output["error"] != tools.InvalidWebSearchQueryMessage {
			t.Fatalf("expected invalid query error in follow-up output, got %+v", output)
		}
	}
	if !foundHostedOutput {
		t.Fatalf("expected invalid hosted tool output item in follow-up request, got %+v", client.calls[1].Items)
	}
	if hostedStart == nil {
		t.Fatal("expected explicit hosted tool start event")
	}
	if meta := decodeToolCallMeta(*hostedStart); meta == nil || meta.Command != "web search: invalid query" {
		t.Fatalf("hosted tool start presentation = %+v, want typed query input", meta)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	foundPersistedError := false
	for _, evt := range events {
		if evt.Kind != "tool_completed" {
			continue
		}
		var completion storedToolCompletion
		if err := json.Unmarshal(evt.Payload, &completion); err != nil {
			t.Fatalf("decode tool_completed payload: %v", err)
		}
		if completion.CallID != "ws_invalid" {
			continue
		}
		foundPersistedError = true
		if !completion.IsError {
			t.Fatalf("expected persisted hosted completion to be error, got %+v", completion)
		}
		var output map[string]string
		if err := json.Unmarshal(completion.Output, &output); err != nil {
			t.Fatalf("decode persisted hosted output: %v", err)
		}
		if output["error"] != tools.InvalidWebSearchQueryMessage {
			t.Fatalf("expected persisted invalid query error, got %+v", output)
		}
		if completion.Presentation == nil || completion.Presentation.Command != "web search: invalid query" {
			t.Fatalf("persisted hosted presentation = %+v, want typed query input", completion.Presentation)
		}
	}
	if !foundPersistedError {
		t.Fatalf("expected persisted hosted completion for ws_invalid")
	}
}

func TestSubmitUserMessageFinalAnswerWithHostedToolCallMaterializesToolBeforeFinal(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
				},
				{
					Type:    llm.ResponseItemTypeMessage,
					Role:    llm.RoleAssistant,
					Phase:   llm.MessagePhaseFinal,
					Content: "done",
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected final answer with hosted tool call to finish in 1 model call, got %d", len(client.calls))
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	toolCallBeforeFinal := false
	toolResultBeforeFinal := false
	finalSeen := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var persisted llm.Message
		if err := json.Unmarshal(evt.Payload, &persisted); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if persisted.Role == llm.RoleAssistant && len(persisted.ToolCalls) == 1 && persisted.ToolCalls[0].ID == "ws_1" {
			if finalSeen {
				t.Fatalf("hosted tool call persisted after final answer")
			}
			toolCallBeforeFinal = true
		}
		if persisted.Role == llm.RoleTool && persisted.ToolCallID == "ws_1" {
			if finalSeen {
				t.Fatalf("hosted tool result persisted after final answer")
			}
			toolResultBeforeFinal = true
		}
		if persisted.Role == llm.RoleAssistant && persisted.Phase == llm.MessagePhaseFinal && strings.TrimSpace(persisted.Content) == "done" {
			finalSeen = true
			if len(persisted.ToolCalls) != 0 {
				t.Fatalf("final assistant message retained tool calls: %+v", persisted.ToolCalls)
			}
		}
	}
	if !toolCallBeforeFinal || !toolResultBeforeFinal || !finalSeen {
		t.Fatalf("expected hosted tool call, result, and final answer in order; call=%t result=%t final=%t", toolCallBeforeFinal, toolResultBeforeFinal, finalSeen)
	}
}

func TestSubmitUserMessageCommentaryWithoutToolCallsForcesNextLoop(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "Working on it",
				Phase:   llm.MessagePhaseCommentary,
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "running",
				Phase:   llm.MessagePhaseCommentary,
			},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "done",
				Phase:   llm.MessagePhaseFinal,
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected 3 model calls, got %d", len(client.calls))
	}

	secondReq := client.calls[1]
	foundWarning := false
	for _, reqMsg := range requestMessages(secondReq) {
		if reqMsg.Role == llm.RoleDeveloper && strings.Contains(reqMsg.Content, commentaryWithoutToolCallsWarning) {
			if reqMsg.MessageType != llm.MessageTypeErrorFeedback {
				t.Fatalf("expected commentary warning message type error_feedback, got %+v", reqMsg)
			}
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected commentary warning in next request, got %+v", requestMessages(secondReq))
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	toolCompleted := 0
	for _, evt := range events {
		if evt.Kind == "tool_completed" {
			toolCompleted++
		}
	}
	if toolCompleted != 1 {
		t.Fatalf("expected exactly one tool execution, got %d", toolCompleted)
	}
}

func TestSubmitUserMessage_ExposesViewImageToolForVisionModels(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolViewImage, Handler: fakeTool{name: toolspec.ToolViewImage}}), Config{
		Model:        "gpt-5.3-codex",
		EnabledTools: []toolspec.ID{toolspec.ToolViewImage},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "analyze image"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}
	found := false
	for _, tool := range client.calls[0].Tools {
		if strings.TrimSpace(tool.Name) == string(toolspec.ToolViewImage) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected view_image tool in request tools: %+v", client.calls[0].Tools)
	}
}

func TestSubmitUserMessage_HidesViewImageToolForTextOnlyModels(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolViewImage, Handler: fakeTool{name: toolspec.ToolViewImage}}), Config{
		Model:        "gpt-3.5-turbo",
		EnabledTools: []toolspec.ID{toolspec.ToolViewImage},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "analyze image"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}
	for _, tool := range client.calls[0].Tools {
		if strings.TrimSpace(tool.Name) == string(toolspec.ToolViewImage) {
			t.Fatalf("did not expect view_image tool in request for text-only model: %+v", client.calls[0].Tools)
		}
	}
}

func TestSubmitUserMessage_HidesViewImageToolForCodexSpark(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:     llm.Usage{WindowTokens: 128000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolViewImage, Handler: fakeTool{name: toolspec.ToolViewImage}}), Config{
		Model:        "gpt-5.3-codex-spark",
		EnabledTools: []toolspec.ID{toolspec.ToolViewImage},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "analyze image"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}
	for _, tool := range client.calls[0].Tools {
		if strings.TrimSpace(tool.Name) == string(toolspec.ToolViewImage) {
			t.Fatalf("did not expect view_image tool in request for codex spark: %+v", client.calls[0].Tools)
		}
	}
	locked := store.Meta().Locked
	if locked == nil {
		t.Fatal("expected locked contract")
	}
	if locked.ModelCapabilities.SupportsVisionInputs {
		t.Fatalf("expected codex spark locked capabilities to remain text-only, got %+v", locked.ModelCapabilities)
	}
}

func TestSubmitUserMessage_ExposesViewImageToolForUnlistedVisionModelWithOverride(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolViewImage, Handler: fakeTool{name: toolspec.ToolViewImage}}), Config{
		Model:             "gpt-4.1-2026-01-15",
		ModelCapabilities: session.LockedModelCapabilities{SupportsVisionInputs: true},
		EnabledTools:      []toolspec.ID{toolspec.ToolViewImage},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "analyze image"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}
	found := false
	for _, tool := range client.calls[0].Tools {
		if strings.TrimSpace(tool.Name) == string(toolspec.ToolViewImage) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected view_image tool in request tools for override-enabled alias: %+v", client.calls[0].Tools)
	}
	locked := store.Meta().Locked
	if locked == nil || !locked.ModelCapabilities.SupportsVisionInputs {
		t.Fatalf("expected locked model capability override to persist, got %+v", locked)
	}
}

func TestEnsureLocked_DoesNotPersistFallbackProviderContractOnTransientFailure(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		capsErr: errors.New("transient auth metadata failure"),
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		}},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5.3-codex"})

	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	locked := store.Meta().Locked
	if locked == nil {
		t.Fatal("expected session to lock")
	}
	if strings.TrimSpace(locked.ProviderContract.ProviderID) != "" {
		t.Fatalf("expected transient provider capability failure to avoid persisting fallback provider contract, got %+v", locked.ProviderContract)
	}

	client.mu.Lock()
	client.capsErr = nil
	client.caps = llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsNativeWebSearch:       true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}
	client.mu.Unlock()

	caps, err := eng.providerCapabilities(context.Background())
	if err != nil {
		t.Fatalf("providerCapabilities after recovery: %v", err)
	}
	if caps.ProviderID != "openai" || !caps.SupportsNativeWebSearch || !caps.SupportsResponsesCompact {
		t.Fatalf("expected live provider capabilities after recovery, got %+v", caps)
	}
}

func TestEnsureLocked_PersistsProviderCapabilityOverrideOverTransportMetadata(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		caps: llm.ProviderCapabilities{
			ProviderID:                 "anthropic",
			SupportsResponsesAPI:       false,
			SupportsResponsesCompact:   false,
			SupportsNativeWebSearch:    false,
			SupportsReasoningEncrypted: false,
			IsOpenAIFirstParty:         false,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage:     llm.Usage{WindowTokens: 200000},
		}},
	}

	override := &llm.ProviderCapabilities{
		ProviderID:                    "custom-provider",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsNativeWebSearch:       true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                        "gpt-5.4",
		ProviderCapabilitiesOverride: override,
		EnabledTools:                 []toolspec.ID{toolspec.ToolExecCommand},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	locked := store.Meta().Locked
	if locked == nil {
		t.Fatal("expected session to lock")
	}
	if locked.ProviderContract.ProviderID != override.ProviderID {
		t.Fatalf("expected override provider id to persist, got %+v", locked.ProviderContract)
	}
	if !locked.ProviderContract.SupportsResponsesCompact || !locked.ProviderContract.SupportsNativeWebSearch || !locked.ProviderContract.IsOpenAIFirstParty {
		t.Fatalf("expected override provider capabilities to persist, got %+v", locked.ProviderContract)
	}

	resumedCaps, err := eng.providerCapabilities(context.Background())
	if err != nil {
		t.Fatalf("providerCapabilities: %v", err)
	}
	if resumedCaps.ProviderID != override.ProviderID || !resumedCaps.SupportsResponsesCompact || !resumedCaps.SupportsNativeWebSearch || !resumedCaps.IsOpenAIFirstParty {
		t.Fatalf("expected locked override provider capabilities on subsequent reads, got %+v", resumedCaps)
	}
}

func TestSubmitUserMessageMissingPhaseDefaultsToCommentaryAndWarns(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "Working on it",
			},
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleAssistant, Content: "Working on it"},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "running",
				Phase:   llm.MessagePhaseCommentary,
			},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "done",
				Phase:   llm.MessagePhaseFinal,
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected 3 model calls, got %d", len(client.calls))
	}

	secondReq := client.calls[1]
	foundWarning := false
	for _, reqMsg := range requestMessages(secondReq) {
		if reqMsg.Role == llm.RoleDeveloper && strings.Contains(reqMsg.Content, missingAssistantPhaseWarning) {
			if reqMsg.MessageType != llm.MessageTypeErrorFeedback {
				t.Fatalf("expected missing-phase warning message type error_feedback, got %+v", reqMsg)
			}
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected missing-phase warning in next request, got %+v", requestMessages(secondReq))
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	persistedAsCommentary := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var persisted llm.Message
		if err := json.Unmarshal(evt.Payload, &persisted); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if persisted.Role == llm.RoleAssistant && strings.TrimSpace(persisted.Content) == "Working on it" {
			persistedAsCommentary = persisted.Phase == llm.MessagePhaseCommentary
			break
		}
	}
	if !persistedAsCommentary {
		t.Fatalf("expected missing-phase assistant message to be persisted as commentary")
	}
}

func TestSubmitUserMessageMissingPhaseLegacyClientRemainsTerminal(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "done",
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = llm.ProviderCapabilities{ProviderID: "anthropic", SupportsResponsesAPI: false, IsOpenAIFirstParty: false}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var persisted llm.Message
		if err := json.Unmarshal(evt.Payload, &persisted); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if persisted.Role == llm.RoleDeveloper && strings.Contains(persisted.Content, missingAssistantPhaseWarning) {
			t.Fatalf("did not expect missing-phase warning for legacy client response")
		}
	}
}

func TestSubmitUserMessageMissingPhaseLegacyClientEmitsAssistantEventOnce(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "done",
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	client.caps = llm.ProviderCapabilities{ProviderID: "anthropic", SupportsResponsesAPI: false, IsOpenAIFirstParty: false}

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt)
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	assistantEvents := 0
	for _, evt := range events {
		if evt.Kind == EventAssistantMessage && evt.Message.Content == "done" {
			assistantEvents++
		}
	}
	if assistantEvents != 1 {
		t.Fatalf("expected one assistant_message event for missing-phase terminal reply, got %d events=%+v", assistantEvents, events)
	}
}
