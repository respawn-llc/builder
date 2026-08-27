package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call_patch_1", Name: string(toolspec.ToolPatch), Custom: true, CustomInput: textutil.Value("*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch")}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch, delay: 50 * time.Millisecond}}), Config{
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
	waitEngineLifecycleTasks(t, eng)
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call_patch_1", Name: string(toolspec.ToolPatch), Custom: true, CustomInput: textutil.Value("*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch")}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch, delay: 50 * time.Millisecond}}), Config{
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
			Assistant: llm.Message{Role: llm.RoleAssistant},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}
	if !client.calls[0].EnableNativeWebSearch {
		t.Fatalf("expected first request to enable native web search")
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	hostedCompletionCount := 0
	for _, evt := range events {
		if evt.Kind != "tool_completed" {
			continue
		}
		completion := persistedToolCompletionForTest(t, evt)
		name := completion.Name
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
		if item.CallID != nil && *item.CallID == "ws_1" {
			foundHostedOutput = true
			break
		}
	}
	if !foundHostedOutput {
		t.Fatalf("expected hosted tool output item in follow-up request, got %+v", secondReq.Items)
	}
}

func TestMixedAcceptedCallsPersistInProviderOutputOrder(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var startedCallIDs []string
	localOne := llm.ToolCall{
		ID:    "local-one",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	}
	localTwo := llm.ToolCall{
		ID:    "local-two",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	}
	client := &fakeClient{
		caps: openAIFirstPartyNativeWebSearchCaps(),
		responses: []llm.Response{
			{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("working"),
					Phase:   textutil.Value(llm.MessagePhaseCommentary),
				},
				ToolCalls: []llm.ToolCall{localOne, localTwo},
				OutputItems: []llm.ResponseItem{
					{
						Type:   llm.ResponseItemTypeFunctionCall,
						ID:     textutil.Value(localOne.ID),
						CallID: textutil.Value(localOne.ID),
					},
					{
						Type: llm.ResponseItemTypeOther,
						ID:   textutil.Value("hosted-middle"),
						Raw:  json.RawMessage(`{"type":"web_search_call","id":"hosted-middle","status":"completed","action":{"type":"search","query":"kent cli"}}`),
					},
					{
						Type:   llm.ResponseItemTypeFunctionCall,
						ID:     textutil.Value(localTwo.ID),
						CallID: textutil.Value(localTwo.ID),
					},
				},
				Usage: llm.Usage{WindowTokens: 200000},
			},
			{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("done"),
					Phase:   textutil.Value(llm.MessagePhaseFinal),
				},
				Usage: llm.Usage{WindowTokens: 200000},
			},
		},
	}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model:         "gpt-5",
			WebSearchMode: "native",
			EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
			OnEvent: func(event Event) {
				if event.Kind == EventToolCallStarted && event.ToolCall != nil {
					startedCallIDs = append(startedCallIDs, event.ToolCall.ID)
				}
			},
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "run mixed tools"); err != nil {
		t.Fatalf("submit mixed tools: %v", err)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read mixed-tool records: %v", err)
	}
	want := []string{"local-one", "hosted-middle", "local-two"}
	var persistedResults []string
	foundIntent := false
	for _, record := range window.Records {
		messageRecord, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if !ok {
			continue
		}
		message, err := llmMessageFromSessionRecord(messageRecord)
		if err != nil {
			t.Fatalf("restore mixed-tool assistant message: %v", err)
		}
		if message.Role != llm.RoleAssistant || len(message.ToolCalls) != len(want) {
			if message.Role == llm.RoleTool && message.ToolCallID != nil {
				persistedResults = append(persistedResults, *message.ToolCallID)
			}
			continue
		}
		got := make([]string, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			got[index] = call.ID
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mixed-tool persisted order = %v, want %v", got, want)
		}
		foundIntent = true
	}
	if !foundIntent {
		t.Fatal("mixed-tool assistant intent was not persisted")
	}
	if !reflect.DeepEqual(persistedResults, want) {
		t.Fatalf("mixed-tool result order = %v, want %v", persistedResults, want)
	}
	if len(startedCallIDs) < len(want) || startedCallIDs[0] != "hosted-middle" {
		t.Fatalf(
			"mixed-tool start order = %v, want hosted outcome materialized before local starts",
			startedCallIDs,
		)
	}
}

func TestInvalidMixedAcceptedCallPositionsFailBeforeToolEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		localCallID string
		outputItems []llm.ResponseItem
	}{
		{
			name:        "missing local position",
			localCallID: "local-missing",
			outputItems: []llm.ResponseItem{{
				Type: llm.ResponseItemTypeOther,
				ID:   textutil.Value("hosted"),
				Raw:  json.RawMessage(`{"type":"web_search_call","id":"hosted","status":"completed","action":{"type":"search","query":"kent"}}`),
			}},
		},
		{
			name:        "duplicate accepted ID",
			localCallID: "duplicate",
			outputItems: []llm.ResponseItem{
				{
					Type:   llm.ResponseItemTypeFunctionCall,
					ID:     textutil.Value("duplicate"),
					CallID: textutil.Value("duplicate"),
				},
				{
					Type: llm.ResponseItemTypeOther,
					ID:   textutil.Value("duplicate"),
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"duplicate","status":"completed","action":{"type":"search","query":"kent"}}`),
				},
			},
		},
		{
			name:        "ambiguous local position",
			localCallID: "local-ambiguous",
			outputItems: []llm.ResponseItem{
				{
					Type:   llm.ResponseItemTypeFunctionCall,
					ID:     textutil.Value("local-ambiguous"),
					CallID: textutil.Value("local-ambiguous"),
				},
				{
					Type: llm.ResponseItemTypeOther,
					ID:   textutil.Value("hosted"),
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"hosted","status":"completed","action":{"type":"search","query":"kent"}}`),
				},
				{
					Type:   llm.ResponseItemTypeFunctionCall,
					ID:     textutil.Value("local-ambiguous"),
					CallID: textutil.Value("local-ambiguous"),
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			probe := &toolExecutionProbe{}
			client := &fakeClient{
				caps: openAIFirstPartyNativeWebSearchCaps(),
				responses: []llm.Response{{
					Assistant: llm.Message{
						Role:    llm.RoleAssistant,
						Content: textutil.Value("working"),
						Phase:   textutil.Value(llm.MessagePhaseCommentary),
					},
					ToolCalls: []llm.ToolCall{{
						ID:    test.localCallID,
						Name:  string(toolspec.ToolExecCommand),
						Input: json.RawMessage(`{"cmd":"true"}`),
					}},
					OutputItems: test.outputItems,
					Usage:       llm.Usage{WindowTokens: 200000},
				}},
			}
			engine := mustNewTestEngine(
				t,
				store,
				client,
				newTestToolRegistry(t, tools.HandlerRegistration{
					ID:      toolspec.ToolExecCommand,
					Handler: probe,
				}),
				Config{
					Model:         "gpt-5",
					WebSearchMode: "native",
					EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
				},
			)

			if _, err := engine.SubmitUserMessage(context.Background(), "run invalid mixed tools"); err == nil {
				t.Fatal("invalid mixed tool response succeeded")
			}
			if probe.calls.Load() != 0 {
				t.Fatalf("invalid mixed response executed %d local tools", probe.calls.Load())
			}
			window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
			if err != nil {
				t.Fatalf("read invalid mixed records: %v", err)
			}
			for _, record := range window.Records {
				messageRecord, ok := mustSessionEventPayload(record).(session.MessageRecord)
				if !ok {
					continue
				}
				message, err := llmMessageFromSessionRecord(messageRecord)
				if err != nil {
					t.Fatalf("restore invalid mixed message: %v", err)
				}
				if len(message.ToolCalls) > 0 {
					t.Fatalf("invalid mixed response persisted tool intent: %+v", message.ToolCalls)
				}
			}
		})
	}
}

func TestHostedOnlyAcceptedCallsPersistInOutputOrder(t *testing.T) {
	t.Parallel()
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(durability),
	)
	client := &fakeClient{
		caps: openAIFirstPartyNativeWebSearchCaps(),
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant},
				OutputItems: []llm.ResponseItem{
					{
						Type: llm.ResponseItemTypeOther,
						ID:   textutil.Value("hosted-one"),
						Raw:  json.RawMessage(`{"type":"web_search_call","id":"hosted-one","status":"completed","action":{"type":"search","query":"one"}}`),
					},
					{
						Type: llm.ResponseItemTypeOther,
						ID:   textutil.Value("hosted-two"),
						Raw:  json.RawMessage(`{"type":"web_search_call","id":"hosted-two","status":"completed","action":{"type":"search","query":"two"}}`),
					},
				},
				Usage: llm.Usage{WindowTokens: 200000},
			},
			{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("done"),
					Phase:   textutil.Value(llm.MessagePhaseFinal),
				},
				Usage: llm.Usage{WindowTokens: 200000},
			},
		},
	}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		newTestToolRegistry(t),
		Config{
			Model:         "gpt-5",
			WebSearchMode: "native",
			EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "run hosted tools"); err != nil {
		t.Fatalf("submit hosted tools: %v", err)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(24)
	if err != nil {
		t.Fatalf("read hosted-only records: %v", err)
	}
	want := []string{"hosted-one", "hosted-two"}
	var persistedResults []string
	foundIntent := false
	for _, record := range window.Records {
		messageRecord, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if !ok {
			continue
		}
		message, err := llmMessageFromSessionRecord(messageRecord)
		if err != nil {
			t.Fatalf("restore hosted-only message: %v", err)
		}
		if message.Role != llm.RoleAssistant || len(message.ToolCalls) != len(want) {
			if message.Role == llm.RoleTool && message.ToolCallID != nil {
				persistedResults = append(persistedResults, *message.ToolCallID)
			}
			continue
		}
		got := []string{message.ToolCalls[0].ID, message.ToolCalls[1].ID}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("hosted-only persisted order = %v, want %v", got, want)
		}
		foundIntent = true
	}
	if !foundIntent {
		t.Fatal("hosted-only assistant intent was not persisted")
	}
	if !reflect.DeepEqual(persistedResults, want) {
		t.Fatalf("hosted-only result order = %v, want %v", persistedResults, want)
	}
	appends, _ := durability.snapshot()
	groupAppends := 0
	for _, observation := range appends {
		if observation.RecordCount == len(want)*2 {
			groupAppends++
		}
	}
	if groupAppends != 1 {
		t.Fatalf("hosted result-group appends = %d, want one: %+v", groupAppends, appends)
	}
	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(
		t,
		reopened,
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	snapshot := mustTranscriptHydrationSnapshot(t, restored)
	for _, callID := range want {
		if rows := countHydratedToolRows(snapshot, callID); rows != 1 {
			t.Fatalf("reopened hosted rows for %s = %d, want 1", callID, rows)
		}
	}
}

func TestSubmitUserMessageContinuesAfterInvalidHostedWebSearch(t *testing.T) {
	store := mustCreateTestSession(t)
	var hostedStart *llm.ToolCall

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_invalid","status":"completed","action":{"type":"search","query":"web search"}}`),
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}
	if !client.calls[0].EnableNativeWebSearch {
		t.Fatalf("expected first request to enable native web search")
	}

	foundHostedOutput := false
	for _, item := range client.calls[1].Items {
		if item.Type != llm.ResponseItemTypeFunctionCallOutput || item.CallID == nil || *item.CallID != "ws_invalid" {
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
	if meta := decodeToolCallMeta(*hostedStart); meta == nil || meta.Command != "web search" {
		t.Fatalf("hosted tool start presentation = %+v, want typed query input", meta)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	foundPersistedError := false
	for _, evt := range events {
		if evt.Kind != "tool_completed" {
			continue
		}
		completion := persistedToolCompletionForTest(t, evt)
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
		if completion.Presentation == nil || completion.Presentation.Command != "web search" {
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
				},
				{
					Type:    llm.ResponseItemTypeMessage,
					Role:    textutil.Value(llm.RoleAssistant),
					Phase:   textutil.Value(llm.MessagePhaseFinal),
					Content: textutil.Value("done"),
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected final answer with hosted tool call to finish in 1 model call, got %d", len(client.calls))
	}

	events, err := collectTestEventRecords(store)
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
		persisted := persistedMessageForTest(t, evt)
		if persisted.Role == llm.RoleAssistant && len(persisted.ToolCalls) == 1 && persisted.ToolCalls[0].ID == "ws_1" {
			if finalSeen {
				t.Fatalf("hosted tool call persisted after final answer")
			}
			toolCallBeforeFinal = true
		}
		if persisted.Role == llm.RoleTool && persisted.ToolCallID != nil && *persisted.ToolCallID == "ws_1" {
			if finalSeen {
				t.Fatalf("hosted tool result persisted after final answer")
			}
			toolResultBeforeFinal = true
		}
		if persisted.Role == llm.RoleAssistant && persisted.Phase != nil && *persisted.Phase == llm.MessagePhaseFinal && strings.TrimSpace(messageContent(persisted)) == "done" {
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
				Content: textutil.Value("Working on it"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("running"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected 3 model calls, got %d", len(client.calls))
	}

	secondReq := client.calls[1]
	foundWarning := false
	for _, reqMsg := range requestMessages(secondReq) {
		if reqMsg.Role == llm.RoleDeveloper && strings.Contains(messageContent(reqMsg), commentaryWithoutToolCallsWarning) {
			if reqMsg.MessageType == nil || *reqMsg.MessageType != llm.MessageTypeErrorFeedback {
				t.Fatalf("expected commentary warning message type error_feedback, got %+v", reqMsg)
			}
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected commentary warning in next request, got %+v", requestMessages(secondReq))
	}

	events, err := collectTestEventRecords(store)
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

func TestSubmitUserMessageViewImageToolFollowsModelCapabilities(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolViewImage, Handler: fakeTool{name: toolspec.ToolViewImage}}), Config{
		Model:        "gpt-5.3-codex",
		EnabledTools: []toolspec.ID{toolspec.ToolViewImage},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "analyze image"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(client.calls))
	}
	found := false
	for _, tool := range client.calls[0].Tools {
		if strings.TrimSpace(tool.Name) == string(toolspec.ToolViewImage) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("view_image not advertised to vision model; tools=%+v", client.calls[0].Tools)
	}
}

func TestNewRejectsTransientProviderCapabilityFailure(t *testing.T) {
	store := mustCreateTestSession(t)

	capabilityErr := errors.New("transient auth metadata failure")
	_, err := New(
		store,
		mustMaterializeTestEventLog(t, store),
		&fakeClient{capsErr: capabilityErr},
		newTestToolRegistry(t),
		Config{Model: "gpt-5.3-codex"},
	)
	if !errors.Is(err, capabilityErr) {
		t.Fatalf("New error = %v, want provider capability error", err)
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
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

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
				Content: textutil.Value("Working on it"),
			},
			ProviderPhase: llm.AbsentProviderPhase(),
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleAssistant), Content: textutil.Value("Working on it")},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("running"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected 3 model calls, got %d", len(client.calls))
	}

	secondReq := client.calls[1]
	foundWarning := false
	for _, reqMsg := range requestMessages(secondReq) {
		if reqMsg.Role == llm.RoleDeveloper && strings.Contains(messageContent(reqMsg), missingAssistantPhaseWarning) {
			if reqMsg.MessageType == nil || *reqMsg.MessageType != llm.MessageTypeErrorFeedback {
				t.Fatalf("expected missing-phase warning message type error_feedback, got %+v", reqMsg)
			}
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected missing-phase warning in next request, got %+v", requestMessages(secondReq))
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	persistedAsCommentary := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		persisted := persistedMessageForTest(t, evt)
		if persisted.Role == llm.RoleAssistant && strings.TrimSpace(messageContent(persisted)) == "Working on it" {
			persistedAsCommentary = persisted.Phase != nil && *persisted.Phase == llm.MessagePhaseCommentary
			break
		}
	}
	if !persistedAsCommentary {
		t.Fatalf("expected missing-phase assistant message to be persisted as commentary")
	}
}

func TestSubmitUserMessageMissingPhaseRemainsTerminal(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = llm.ProviderCapabilities{ProviderID: "anthropic", SupportsResponsesAPI: false, IsOpenAIFirstParty: false}

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		persisted := persistedMessageForTest(t, evt)
		if persisted.Role == llm.RoleDeveloper && strings.Contains(messageContent(persisted), missingAssistantPhaseWarning) {
			t.Fatalf("did not expect missing-phase warning for legacy client response")
		}
	}
}

func TestSubmitUserMessageCompatibleResponsesMissingPhaseRemainsTerminal(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{
		caps: llm.ProviderCapabilities{
			ProviderID:           "openai-compatible",
			SupportsResponsesAPI: true,
			IsOpenAIFirstParty:   false,
		},
		responses: []llm.Response{{
			Assistant:     llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
			ProviderPhase: llm.AbsentProviderPhase(),
			Usage:         llm.Usage{WindowTokens: 200000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "compatible-model"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(client.calls))
	}
}
