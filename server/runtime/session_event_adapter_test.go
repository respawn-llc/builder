package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/rollbacktarget"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestSessionMessageRecordAdapterRoundTrip(t *testing.T) {
	t.Parallel()
	contextID := uuid.New()
	branch := "feature/session-v1"
	exitCode := 17
	message := llm.Message{
		Role:                 llm.RoleAssistant,
		MessageType:          textutil.Value(llm.MessageTypeBackgroundNotice),
		SourcePath:           textutil.Value("/tmp/source.go"),
		WorktreeContext:      &session.WorktreeContext{ContextID: &contextID, Branch: &branch, WorktreePath: "/tmp/worktree", WorkspaceRoot: "/tmp/workspace", EffectiveCwd: "/tmp/worktree"},
		Content:              textutil.Value("tool call follows"),
		CompactContent:       textutil.Value("tool call"),
		Name:                 textutil.Value("assistant"),
		ToolCallID:           textutil.Value("parent-call"),
		Phase:                textutil.Value(llm.MessagePhaseCommentary),
		BackgroundActivityID: textutil.Value("activity-1"),
		BackgroundExitCode:   &exitCode,
		ToolCalls: []llm.ToolCall{{
			ID:           "call-1",
			Name:         string(toolspec.ToolExecCommand),
			Presentation: json.RawMessage(`{"ToolName":"exec_command"}`),
			Input:        json.RawMessage(`{"cmd":"pwd"}`),
		}, {
			ID:          "call-2",
			Name:        string(toolspec.ToolPatch),
			Input:       json.RawMessage(`"*** Begin Patch\n*** End Patch"`),
			Custom:      true,
			CustomInput: textutil.Value("*** Begin Patch\n*** End Patch"),
		}},
		ReasoningItems: []llm.ReasoningItem{{
			ID:               "reasoning-1",
			EncryptedContent: "encrypted-1",
		}},
	}

	record, err := sessionMessageRecordFromLLM(message)
	if err != nil {
		t.Fatalf("adapt message to session record: %v", err)
	}
	restored, err := llmMessageFromSessionRecord(record)
	if err != nil {
		t.Fatalf("adapt session record to message: %v", err)
	}
	if !reflect.DeepEqual(restored, message) {
		t.Fatalf("restored message = %#v, want %#v", restored, message)
	}
}

func TestSessionMessageRecordAdapterPersistsToolCallWithoutSemanticContent(t *testing.T) {
	t.Parallel()
	message := llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    "call-1",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"kent_qa_nonexistent_command"}`),
		}},
	}

	record, err := sessionMessageRecordFromLLM(message)
	if err != nil {
		t.Fatalf("adapt tool-call-only message to session record: %v", err)
	}
	if record.Content != nil {
		t.Fatalf("tool-call-only message content = %#v, want absent", record.Content)
	}
	restored, err := llmMessageFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore tool-call-only message: %v", err)
	}
	if len(restored.ToolCalls) != 1 || restored.ToolCalls[0].ID != "call-1" {
		t.Fatalf("restored tool calls = %#v", restored.ToolCalls)
	}
}

func TestSessionMessageRecordAdapterPreservesAbsenceAndRejectsPresentBlankFacts(t *testing.T) {
	t.Parallel()
	record, err := sessionMessageRecordFromLLM(llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("hello"),
	})
	if err != nil {
		t.Fatalf("adapt message with absent optional facts: %v", err)
	}
	restored, err := llmMessageFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore message with absent optional facts: %v", err)
	}
	if restored.MessageType != nil ||
		restored.SourcePath != nil ||
		restored.CompactContent != nil ||
		restored.Name != nil ||
		restored.ToolCallID != nil ||
		restored.Phase != nil ||
		restored.BackgroundActivityID != nil {
		t.Fatalf("restored absent facts became present: %#v", restored)
	}

	_, err = sessionMessageRecordFromLLM(llm.Message{
		Role:       llm.RoleUser,
		Content:    textutil.Value("hello"),
		SourcePath: textutil.Value(" \t"),
	})
	if err == nil {
		t.Fatal("adapter accepted a present blank source path")
	}

	_, err = sessionMessageRecordFromLLM(llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:          "call-1",
			Name:        string(toolspec.ToolPatch),
			Custom:      true,
			CustomInput: textutil.Value(" \t"),
		}},
	})
	if err == nil {
		t.Fatal("adapter accepted a present blank custom tool input")
	}
}

func TestSessionToolCompletionRecordAdapterRoundTrip(t *testing.T) {
	t.Parallel()
	presentation := transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{
		ToolName:     string(toolspec.ToolExecCommand),
		Presentation: transcript.ToolPresentationShell,
		Command:      "pwd",
	})
	result := tools.Result{
		CallID:        "call-1",
		Name:          toolspec.ToolExecCommand,
		Output:        json.RawMessage(`{"cwd":"/tmp"}`),
		IsError:       true,
		Summary:       textutil.Value("command failed"),
		CondensedText: textutil.Value("failed"),
		Presentation:  &presentation,
	}
	providerItems := llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
		Type:   llm.ResponseItemTypeFunctionCallOutput,
		CallID: textutil.Value(result.CallID),
		Name:   textutil.Value(string(result.Name)),
		Output: result.Output,
	}})

	record, err := sessionToolCompletionRecordFromRuntime(result, providerItems)
	if err != nil {
		t.Fatalf("adapt completion to session record: %v", err)
	}
	restored, err := storedToolCompletionFromSessionRecord(record)
	if err != nil {
		t.Fatalf("adapt session record to completion: %v", err)
	}
	if restored.CallID != result.CallID ||
		restored.Name != string(result.Name) ||
		restored.IsError != result.IsError ||
		!reflect.DeepEqual(restored.Output, result.Output) ||
		!textutil.EqualOptional(restored.Summary, result.Summary) ||
		!textutil.EqualOptional(restored.CondensedText, result.CondensedText) ||
		!reflect.DeepEqual(restored.Presentation, result.Presentation) ||
		!reflect.DeepEqual(restored.ProviderItems, providerItems) {
		t.Fatalf("restored completion = %#v, want result=%#v provider_items=%#v", restored, result, providerItems)
	}

	_, err = sessionToolCompletionRecordFromRuntime(tools.Result{
		CallID:  "call-blank",
		Name:    toolspec.ToolExecCommand,
		Output:  json.RawMessage(`"done"`),
		Summary: textutil.Value(" \t"),
	}, nil)
	if err == nil {
		t.Fatal("tool-completion adapter accepted a present blank summary")
	}
}

func TestSessionQuestionCompletionAdapterCarriesTypedAnswer(t *testing.T) {
	t.Parallel()
	t.Run("runtime to Session", func(t *testing.T) {
		broker := tools.NewAskQuestionBroker()
		broker.SetAskHandler(func(
			_ context.Context,
			_ tools.AskQuestionRequest,
		) (tools.AskQuestionResolution, error) {
			return tools.AskQuestionAnswer{
				SelectedOptionNumber: textutil.Value(2),
				Freeform:             textutil.Value("keep the split"),
			}, nil
		})
		result, err := tools.NewAskQuestionTool(broker, nil).Call(
			context.Background(),
			tools.Call{
				ID:   "call-question",
				Name: toolspec.ToolAskQuestion,
				Input: json.RawMessage(
					`{"question":"Which option?","suggestions":["first","second"],"recommended_option_index":2}`,
				),
			},
		)
		if err != nil {
			t.Fatalf("execute typed Question: %v", err)
		}
		result.Presentation = questionCompletionPresentation()
		resultJSON, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("encode runtime Question result: %v", err)
		}
		assertQuestionAnswerJSONField(t, resultJSON, "runtime Result")

		providerItems := llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
			Type:   llm.ResponseItemTypeFunctionCallOutput,
			CallID: textutil.Value(result.CallID),
			Name:   textutil.Value(string(result.Name)),
			Output: result.Output,
		}})
		record, err := sessionToolCompletionRecordFromRuntime(result, providerItems)
		if err != nil {
			t.Fatalf("adapt typed Question completion: %v", err)
		}
		roundTrippedRecord, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode typed Session Question completion: %v", err)
		}
		assertQuestionAnswerJSONField(t, roundTrippedRecord, "Session completion")
	})

	t.Run("Session to runtime", func(t *testing.T) {
		var record session.ToolCompletionRecord
		if err := json.Unmarshal([]byte(`{
			"call_id":"call-question",
			"name":"ask_question",
			"output_kind":"function",
			"is_error":false,
			"output":"User selected option 2. User also said: keep the split",
			"question_answer":{
				"selected_option_number":2,
				"freeform":"keep the split"
			}
		}`), &record); err != nil {
			t.Fatalf("decode typed Session Question completion: %v", err)
		}
		record.Presentation = transcript.EncodeToolCallMeta(*questionCompletionPresentation())
		restored, err := storedToolCompletionFromSessionRecord(record)
		if err != nil {
			t.Fatalf("restore typed Question completion: %v", err)
		}
		roundTrippedStored, err := json.Marshal(restored)
		if err != nil {
			t.Fatalf("encode restored Question completion: %v", err)
		}
		assertQuestionAnswerJSONField(t, roundTrippedStored, "restored runtime completion")
	})
}

func TestChatStoreRestoresTypedQuestionAnswer(t *testing.T) {
	t.Parallel()
	answer := &session.QuestionAnswerRecord{
		SelectedOptionNumber: textutil.Value(2),
		Freeform:             textutil.Value("keep the split"),
	}
	record := session.ToolCompletionRecord{
		CallID:         "call-question",
		Name:           "ask_question",
		OutputKind:     session.ToolOutputKindFunction,
		Output:         json.RawMessage(`"flattened"`),
		Presentation:   transcript.EncodeToolCallMeta(*questionCompletionPresentation()),
		QuestionAnswer: answer,
	}
	store := newChatStore()
	if err := store.restoreToolCompletionRecord(record); err != nil {
		t.Fatalf("restore typed Question completion into chat store: %v", err)
	}
	restored, ok := store.toolCompletions[record.CallID]
	if !ok || restored.QuestionAnswer == nil ||
		!textutil.EqualOptional(
			restored.QuestionAnswer.SelectedOptionNumber,
			answer.SelectedOptionNumber,
		) ||
		!textutil.EqualOptional(restored.QuestionAnswer.Freeform, answer.Freeform) {
		t.Fatalf("chat-store Question answer = %#v", restored.QuestionAnswer)
	}
}

func questionCompletionPresentation() *transcript.ToolCallMeta {
	return &transcript.ToolCallMeta{
		ToolName:               string(toolspec.ToolAskQuestion),
		Presentation:           transcript.ToolPresentationAskQuestion,
		Question:               "Which option?",
		Suggestions:            []string{"first", "second"},
		RecommendedOptionIndex: 2,
	}
}

func assertQuestionAnswerJSONField(t *testing.T, raw []byte, owner string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode %s JSON: %v", owner, err)
	}
	answerRaw := object["question_answer"]
	if len(answerRaw) == 0 {
		t.Fatalf("%s lost typed Question-answer facts", owner)
	}
	var answer struct {
		SelectedOptionNumber *int    `json:"selected_option_number"`
		Freeform             *string `json:"freeform"`
	}
	if err := json.Unmarshal(answerRaw, &answer); err != nil {
		t.Fatalf("decode %s typed Question answer: %v", owner, err)
	}
	if answer.SelectedOptionNumber == nil ||
		*answer.SelectedOptionNumber != 2 ||
		answer.Freeform == nil ||
		*answer.Freeform != "keep the split" {
		t.Fatalf("%s typed Question answer = %#v", owner, answer)
	}
}

func TestSessionLocalAndCacheRecordAdaptersRoundTrip(t *testing.T) {
	t.Parallel()
	afterToolCallID := "call-1"
	durationMs := int64(1234)
	localEntry := storedLocalEntry{
		Visibility:      transcript.EntryVisibilityDetail,
		Role:            "developer",
		Text:            "operator feedback",
		DurationMs:      &durationMs,
		CondensedText:   textutil.Value("feedback"),
		DiagnosticKey:   textutil.Value("diagnostic-1"),
		NoticeID:        textutil.Value("notice-1"),
		AfterToolCallID: &afterToolCallID,
	}
	localRecord, err := sessionLocalEntryRecordFromRuntime(localEntry)
	if err != nil {
		t.Fatalf("adapt local entry: %v", err)
	}
	restoredLocal, err := storedLocalEntryFromSessionRecord(localRecord)
	if err != nil {
		t.Fatalf("restore local entry: %v", err)
	}
	if !reflect.DeepEqual(restoredLocal, localEntry) {
		t.Fatalf("restored local entry = %#v, want %#v", restoredLocal, localEntry)
	}
	if _, err := sessionLocalEntryRecordFromRuntime(storedLocalEntry{
		Visibility: transcript.EntryVisibility("unsupported"),
		Role:       "warning",
		Text:       "invalid visibility",
	}); err == nil {
		t.Fatal("local-entry adapter accepted unsupported runtime visibility")
	}
	if _, err := sessionLocalEntryRecordFromRuntime(storedLocalEntry{
		Visibility:    transcript.EntryVisibilityDetail,
		Role:          "warning",
		Text:          "invalid optional fact",
		DiagnosticKey: textutil.Value(" \t"),
	}); err == nil {
		t.Fatal("local-entry adapter accepted a present blank diagnostic key")
	}

	request := persistedCacheRequestObserved{
		DigestVersion: requestCacheDigestVersion,
		CacheKey:      "cache-key-1",
		Scope:         transcript.CacheWarningScopeConversation,
		ChunkCount:    2,
		TerminalHash:  "256b21d39576a67ee3ad5ebf4aa6f1dbb55c50f8ecb04f913a4c0f3f8e91332d",
	}
	requestRecord, err := sessionCacheRequestRecordFromRuntime(request)
	if err != nil {
		t.Fatalf("adapt cache request: %v", err)
	}
	restoredRequest := persistedCacheRequestObservedFromSessionRecord(requestRecord)
	if !reflect.DeepEqual(restoredRequest, request) {
		t.Fatalf("restored cache request = %#v, want %#v", restoredRequest, request)
	}

	response := persistedCacheResponseObserved{
		DigestVersion: requestCacheDigestVersion,
		CacheKey:      request.CacheKey,
		Scope:         request.Scope,
		ChunkCount:    request.ChunkCount,
		TerminalHash:  request.TerminalHash,

		CachedInputTokens: textutil.Value(0),
	}
	responseRecord, err := sessionCacheResponseRecordFromRuntime(response)
	if err != nil {
		t.Fatalf("adapt cache response: %v", err)
	}
	restoredResponse := persistedCacheResponseObservedFromSessionRecord(responseRecord)
	if !reflect.DeepEqual(restoredResponse, response) {
		t.Fatalf("restored cache response = %#v, want %#v", restoredResponse, response)
	}
	absentResponse := response
	absentResponse.CachedInputTokens = nil
	absentResponseRecord, err := sessionCacheResponseRecordFromRuntime(absentResponse)
	if err != nil {
		t.Fatalf("adapt cache response with absent cached tokens: %v", err)
	}
	if restored := persistedCacheResponseObservedFromSessionRecord(absentResponseRecord); restored.CachedInputTokens != nil {
		t.Fatalf("absent cached-token fact became present: %#v", restored)
	}
	negativeCachedInputTokens := -1
	invalidResponse := response
	invalidResponse.CachedInputTokens = &negativeCachedInputTokens
	if _, err := sessionCacheResponseRecordFromRuntime(invalidResponse); err == nil {
		t.Fatal("cache-response adapter accepted negative cached input tokens")
	}

	warning := transcript.CacheWarning{
		Scope:           transcript.CacheWarningScopeReviewer,
		Reason:          transcript.CacheWarningReasonReuseDropped,
		CacheKey:        textutil.Value(request.CacheKey),
		LostInputTokens: textutil.Value(42),
	}
	warningRecord, err := sessionCacheWarningRecordFromRuntime(warning)
	if err != nil {
		t.Fatalf("adapt cache warning: %v", err)
	}
	restoredWarning := cacheWarningFromSessionRecord(warningRecord)
	if !reflect.DeepEqual(restoredWarning, warning) {
		t.Fatalf("restored cache warning = %#v, want %#v", restoredWarning, warning)
	}
	invalidWarning := warning
	invalidWarning.CacheKey = textutil.Value(" \t")
	if _, err := sessionCacheWarningRecordFromRuntime(invalidWarning); err == nil {
		t.Fatal("cache-warning adapter accepted a present blank cache key")
	}
	invalidWarning = warning
	invalidWarning.Scope = ""
	if _, err := sessionCacheWarningRecordFromRuntime(invalidWarning); err == nil {
		t.Fatal("cache-warning adapter accepted an absent required scope")
	}
}

func TestSessionHistoryReplacementRecordAdapterRejectsInvalidOptionalFacts(t *testing.T) {
	t.Parallel()
	base := historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeAuto),
		Items: llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
			Type:    llm.ResponseItemTypeMessage,
			Role:    textutil.Value(llm.RoleUser),
			Content: textutil.Value("summary"),
		}}),
	}

	invalid := base
	invalid.CompactionNumber = textutil.Value(0)
	if _, err := sessionHistoryReplacementRecordFromRuntime(invalid); err == nil {
		t.Fatal("history-replacement adapter accepted a present zero compaction number")
	}

	invalid = base
	invalid.Items = llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
		Type:       llm.ResponseItemTypeMessage,
		Role:       textutil.Value(llm.RoleUser),
		Content:    textutil.Value("summary"),
		SourcePath: textutil.Value(" \t"),
	}})
	if _, err := sessionHistoryReplacementRecordFromRuntime(invalid); err == nil {
		t.Fatal("history-replacement adapter accepted a present blank provider source path")
	}
}

func TestMigratedToolCompletionRecordsPreserveProviderAndCacheLineage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record session.ToolCompletionRecord
		before []llm.ResponseItem
	}{
		{
			name: "authoritative present Raw",
			record: session.ToolCompletionRecord{
				CallID: "call-present", Name: "exec_command",
				OutputKind: session.ToolOutputKindFunction,
				Output:     json.RawMessage(`"done"`),
				ProviderItems: []session.ToolCompletionProviderItem{{
					Type: session.ProviderInputItemTypeFunctionCallOutput,
					Name: stringPointerRuntime("exec_command"), CallID: stringPointerRuntime("call-present"),
					Raw: json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-present", "output" : "done" }`),
				}},
			},
			before: []llm.ResponseItem{{
				Type: llm.ResponseItemTypeFunctionCallOutput, Name: textutil.Value("exec_command"),
				CallID: textutil.Value("call-present"), Output: json.RawMessage(`"done"`),
				Raw: json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-present", "output" : "done" }`),
			}},
		},
		{
			name: "direct generated Raw",
			record: session.ToolCompletionRecord{
				CallID: "call-generated", Name: "exec_command",
				OutputKind: session.ToolOutputKindFunction,
				Output:     json.RawMessage(`{"cwd":"/tmp"}`),
				ProviderItems: []session.ToolCompletionProviderItem{{
					Type: session.ProviderInputItemTypeFunctionCallOutput,
					Name: stringPointerRuntime("exec_command"), CallID: stringPointerRuntime("call-generated"),
				}},
			},
			before: llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
				Type: llm.ResponseItemTypeFunctionCallOutput, Name: textutil.Value("exec_command"),
				CallID: textutil.Value("call-generated"), Output: json.RawMessage(`{"cwd":"/tmp"}`),
			}}),
		},
		{
			name: "absent snapshot fallback",
			record: session.ToolCompletionRecord{
				CallID: "call-fallback", Name: "patch",
				OutputKind: session.ToolOutputKindCustom,
				Output:     json.RawMessage(`"patched"`),
				ProviderItems: []session.ToolCompletionProviderItem{{
					Type: session.ProviderInputItemTypeCustomToolOutput,
					Name: stringPointerRuntime("patch"), CallID: stringPointerRuntime("call-fallback"),
				}},
			},
			before: llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
				Type: llm.ResponseItemTypeCustomToolOutput, Name: textutil.Value("patch"),
				CallID: textutil.Value("call-fallback"), Output: json.RawMessage(`"patched"`),
			}}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := session.NewEventRecord(1, nil, test.record)
			if err != nil {
				t.Fatalf("canonicalize migrated completion: %v", err)
			}
			restored, err := storedToolCompletionFromSessionRecord(
				mustSessionEventPayload(normalized).(session.ToolCompletionRecord),
			)
			if err != nil {
				t.Fatalf("restore migrated completion: %v", err)
			}
			if !reflect.DeepEqual(restored.ProviderItems, test.before) {
				t.Fatalf(
					"provider input changed: got=%#v want=%#v",
					restored.ProviderItems,
					test.before,
				)
			}
			beforeSummary, err := summarizePromptCacheRequest(llm.Request{Items: test.before})
			if err != nil {
				t.Fatalf("summarize pre-migration provider input: %v", err)
			}
			afterSummary, err := summarizePromptCacheRequest(llm.Request{Items: restored.ProviderItems})
			if err != nil {
				t.Fatalf("summarize migrated provider input: %v", err)
			}
			if afterSummary.terminalHash != beforeSummary.terminalHash {
				t.Fatalf(
					"terminal lineage hash changed: got=%s want=%s",
					afterSummary.terminalHash,
					beforeSummary.terminalHash,
				)
			}
		})
	}
}

func TestSessionToolCompletionRecordAdaptersPreserveProviderPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		result        tools.Result
		providerItems func(tools.Result) []llm.ResponseItem
		wantTypes     []llm.ResponseItemType
	}{
		{
			name: "function",
			result: tools.Result{
				CallID: "function-call",
				Name:   toolspec.ToolExecCommand,
				Output: json.RawMessage(`{"cwd":"/tmp"}`),
			},
			providerItems: func(result tools.Result) []llm.ResponseItem {
				return llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
					Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value(result.CallID),
					Name: textutil.Value(string(result.Name)), Output: result.Output,
				}})
			},
			wantTypes: []llm.ResponseItemType{llm.ResponseItemTypeFunctionCallOutput},
		},
		{
			name: "custom",
			result: tools.Result{
				CallID: "custom-call",
				Name:   toolspec.ToolPatch,
				Output: json.RawMessage(`"patched"`),
			},
			providerItems: func(result tools.Result) []llm.ResponseItem {
				return llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
					Type: llm.ResponseItemTypeCustomToolOutput, CallID: textutil.Value(result.CallID),
					Name: textutil.Value(string(result.Name)), Output: result.Output,
				}})
			},
			wantTypes: []llm.ResponseItemType{llm.ResponseItemTypeCustomToolOutput},
		},
		{
			name: "error",
			result: tools.Result{
				CallID:  "error-call",
				Name:    toolspec.ToolExecCommand,
				Output:  json.RawMessage(`{"error":"failed"}`),
				IsError: true,
			},
			providerItems: func(result tools.Result) []llm.ResponseItem {
				return llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
					Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value(result.CallID),
					Name: textutil.Value(string(result.Name)), Output: result.Output,
				}})
			},
			wantTypes: []llm.ResponseItemType{llm.ResponseItemTypeFunctionCallOutput},
		},
		{
			name: "view image attachment",
			result: tools.Result{
				CallID: "image-call",
				Name:   toolspec.ToolViewImage,
				Output: json.RawMessage(`[{"type":"input_file","file_id":"file-1"}]`),
			},
			providerItems: func(result tools.Result) []llm.ResponseItem {
				return llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
					Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value(result.CallID),
					Name: textutil.Value(string(result.Name)), Output: result.Output,
				}})
			},
			wantTypes: []llm.ResponseItemType{
				llm.ResponseItemTypeFunctionCallOutput,
				llm.ResponseItemTypeOther,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerItems := test.providerItems(test.result)
			record, err := sessionToolCompletionRecordFromRuntime(test.result, providerItems)
			if err != nil {
				t.Fatalf("adapt completion: %v", err)
			}
			restored, err := storedToolCompletionFromSessionRecord(record)
			if err != nil {
				t.Fatalf("restore completion: %v", err)
			}
			if restored.IsError != test.result.IsError {
				t.Fatalf("restored error fact = %t, want %t", restored.IsError, test.result.IsError)
			}
			if len(restored.ProviderItems) != len(test.wantTypes) {
				t.Fatalf("provider items = %+v, want types %+v", restored.ProviderItems, test.wantTypes)
			}
			for index, wantType := range test.wantTypes {
				if restored.ProviderItems[index].Type != wantType {
					t.Fatalf("provider item %d type = %q, want %q", index, restored.ProviderItems[index].Type, wantType)
				}
				if !reflect.DeepEqual(restored.ProviderItems[index].Raw, providerItems[index].Raw) {
					t.Fatalf("provider item %d Raw changed: got=%s want=%s", index, restored.ProviderItems[index].Raw, providerItems[index].Raw)
				}
			}
		})
	}
}

func stringPointerRuntime(value string) *string {
	return &value
}

func TestSessionToolCompletionRecordAdapterRejectsUnsupportedProviderItems(t *testing.T) {
	t.Parallel()
	for _, itemType := range []llm.ResponseItemType{
		llm.ResponseItemTypeMessage,
		llm.ResponseItemTypeFunctionCall,
		llm.ResponseItemTypeCustomToolCall,
		llm.ResponseItemTypeReasoning,
		llm.ResponseItemTypeCompaction,
	} {
		t.Run(string(itemType), func(t *testing.T) {
			_, err := sessionToolCompletionRecordFromRuntime(tools.Result{
				CallID: "call-1",
				Name:   toolspec.ToolExecCommand,
				Output: json.RawMessage(`{"ok":true}`),
			}, []llm.ResponseItem{{
				Type:   itemType,
				CallID: textutil.Value("call-1"),
				Raw:    json.RawMessage(`{"type":"opaque"}`),
			}})
			if !errors.Is(err, ErrUnsupportedSessionProviderItem) {
				t.Fatalf("error = %v, want unsupported provider item", err)
			}
		})
	}
}

func TestSessionHistoryReplacementRecordAdapterPreservesProviderHistoryAndProvenance(t *testing.T) {
	t.Parallel()
	committedEntryStart := 19
	contextID := uuid.New()
	branch := "feature/compacted-history"
	backgroundExitCode := 23
	payload := historyReplacementPayload{
		Engine:                            "local",
		Mode:                              string(compactionModeAuto),
		CompactionNumber:                  textutil.Value(4),
		CommittedEntryStart:               &committedEntryStart,
		PendingHandoffFutureMessage:       textutil.Value("continue from the compacted prefix"),
		LastCommittedAssistantFinalAnswer: textutil.Value("previous final answer"),
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq:       11,
			CandidatePageEndByte: 8192,
		},
		Items: llm.PrepareOpenAIInputItems([]llm.ResponseItem{
			{
				Type:        llm.ResponseItemTypeMessage,
				Role:        textutil.Value(llm.RoleUser),
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				SourcePath:  textutil.Value("/tmp/source.md"),
				WorktreeContext: &session.WorktreeContext{
					ContextID:     &contextID,
					Branch:        &branch,
					WorktreePath:  "/tmp/worktree",
					WorkspaceRoot: "/tmp/workspace",
					EffectiveCwd:  "/tmp/worktree",
				},
				Phase:                textutil.Value(llm.MessagePhaseCommentary),
				Content:              textutil.Value("hello"),
				CompactContent:       textutil.Value("hi"),
				BackgroundActivityID: textutil.Value("activity-1"),
				BackgroundExitCode:   &backgroundExitCode,
			},
			{
				Type:             llm.ResponseItemTypeFunctionCall,
				ID:               textutil.Value("item-call-1"),
				Name:             textutil.Value("exec_command"),
				CallID:           textutil.Value("call-1"),
				ToolPresentation: json.RawMessage(`{ "ToolName" : "exec_command" }`),
				Arguments:        json.RawMessage(`{ "cmd" : "pwd" }`),
			},
			{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				Name:   textutil.Value("exec_command"),
				CallID: textutil.Value("call-1"),
				Output: json.RawMessage(`{ "cwd" : "/tmp" }`),
			},
			{
				Type:        llm.ResponseItemTypeCustomToolCall,
				ID:          textutil.Value("item-call-2"),
				Name:        textutil.Value("patch"),
				CallID:      textutil.Value("call-2"),
				CustomInput: textutil.Value("*** Begin Patch\n*** End Patch"),
			},
			{
				Type:   llm.ResponseItemTypeCustomToolOutput,
				Name:   textutil.Value("patch"),
				CallID: textutil.Value("call-2"),
				Output: json.RawMessage(`"patched"`),
			},
			{
				Type: llm.ResponseItemTypeReasoning,
				ID:   textutil.Value("reasoning-1"),
				ReasoningSummary: []llm.ReasoningEntry{{
					Role: textutil.Value("assistant"),
					Text: "kept",
				}},
				EncryptedContent: textutil.Value("encrypted-reasoning"),
			},
			{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value("compaction-1"),
				EncryptedContent: textutil.Value("encrypted-compaction"),
			},
			{
				Type:         llm.ResponseItemTypeOther,
				Name:         textutil.Value("view_image"),
				CallID:       textutil.Value("call-1"),
				Raw:          json.RawMessage(`{ "type" : "message", "role" : "user", "content" : [ { "type" : "input_file", "file_id" : "file-1" } ] }`),
				LinkedCallID: textutil.Value("call-1"),
				LinkKind:     textutil.Value(llm.ResponseItemLinkToolOutputAttachment),
			},
		}),
	}

	record, err := sessionHistoryReplacementRecordFromRuntime(payload)
	if err != nil {
		t.Fatalf("adapt history replacement: %v", err)
	}
	restored, err := historyReplacementPayloadFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore history replacement: %v", err)
	}
	if !reflect.DeepEqual(restored, payload) {
		t.Fatalf("restored history replacement = %#v, want %#v", restored, payload)
	}
	for index := range payload.Items {
		if !bytes.Equal(restored.Items[index].Raw, payload.Items[index].Raw) {
			t.Fatalf(
				"provider history item %d Raw changed: got=%s want=%s",
				index,
				restored.Items[index].Raw,
				payload.Items[index].Raw,
			)
		}
	}

	beforeChunks, err := promptCacheChunks(llm.Request{Items: payload.Items})
	if err != nil {
		t.Fatalf("hash original provider history: %v", err)
	}
	afterChunks, err := promptCacheChunks(llm.Request{Items: restored.Items})
	if err != nil {
		t.Fatalf("hash restored provider history: %v", err)
	}
	if !reflect.DeepEqual(afterChunks, beforeChunks) {
		t.Fatalf("prompt cache chunks changed after history round trip")
	}
	beforeSummary, err := summarizePromptCacheRequest(llm.Request{Items: payload.Items})
	if err != nil {
		t.Fatalf("summarize original provider history: %v", err)
	}
	afterSummary, err := summarizePromptCacheRequest(llm.Request{Items: restored.Items})
	if err != nil {
		t.Fatalf("summarize restored provider history: %v", err)
	}
	if beforeSummary.terminalHash != afterSummary.terminalHash {
		t.Fatalf(
			"terminal lineage hash changed: got=%s want=%s",
			afterSummary.terminalHash,
			beforeSummary.terminalHash,
		)
	}
}

func TestSessionHistoryReplacementRecordAdapterGeneratesOnlyDerivableOutputRaw(t *testing.T) {
	t.Parallel()
	for _, item := range []llm.ResponseItem{
		{
			Type:   llm.ResponseItemTypeFunctionCallOutput,
			CallID: textutil.Value("function-call"),
			Output: json.RawMessage(`{"ok":true}`),
		},
		{
			Type:   llm.ResponseItemTypeCustomToolOutput,
			CallID: textutil.Value("custom-call"),
			Output: json.RawMessage(`"patched"`),
		},
	} {
		record, err := sessionHistoryReplacementRecordFromRuntime(historyReplacementPayload{
			Engine: "local",
			Mode:   string(compactionModeAuto),
			Items:  []llm.ResponseItem{item},
		})
		if err != nil {
			t.Fatalf("adapt %q output history: %v", item.Type, err)
		}
		restored, err := historyReplacementPayloadFromSessionRecord(record)
		if err != nil {
			t.Fatalf("restore %q output history: %v", item.Type, err)
		}
		want := llm.PrepareOpenAIInputItems([]llm.ResponseItem{item})
		if len(restored.Items) != 1 || !bytes.Equal(restored.Items[0].Raw, want[0].Raw) {
			t.Fatalf("restored %q Raw = %s, want %s", item.Type, restored.Items[0].Raw, want[0].Raw)
		}
	}

	_, err := sessionHistoryReplacementRecordFromRuntime(historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeAuto),
		Items:  []llm.ResponseItem{{Type: llm.ResponseItemTypeOther}},
	})
	var itemErr session.ProviderHistoryItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("missing opaque Raw error = %T %v, want ProviderHistoryItemError", err, err)
	}
	if itemErr.Reason != session.ProviderHistoryItemMissingRaw {
		t.Fatalf("missing opaque Raw reason = %q, want %q", itemErr.Reason, session.ProviderHistoryItemMissingRaw)
	}
}

func TestSessionHistoryReplacementUsesItemOrderInsteadOfProviderParserOutputIndex(t *testing.T) {
	t.Parallel()
	items := llm.PrepareOpenAIInputItems([]llm.ResponseItem{
		{
			Type:        llm.ResponseItemTypeMessage,
			OutputIndex: 91,
			Role:        textutil.Value(llm.RoleUser),
			Content:     textutil.Value("first"),
		},
		{
			Type:        llm.ResponseItemTypeMessage,
			OutputIndex: 4,
			Role:        textutil.Value(llm.RoleAssistant),
			Content:     textutil.Value("second"),
		},
	})
	record, err := sessionHistoryReplacementRecordFromRuntime(historyReplacementPayload{
		Engine:           "local",
		Mode:             string(compactionModeAuto),
		CompactionNumber: textutil.Value(1),
		Items:            items,
	})
	if err != nil {
		t.Fatalf("adapt history replacement: %v", err)
	}
	restored, err := historyReplacementPayloadFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore history replacement: %v", err)
	}
	if len(restored.Items) != len(items) {
		t.Fatalf("restored item count = %d, want %d", len(restored.Items), len(items))
	}
	for index := range items {
		if restored.Items[index].OutputIndex != 0 {
			t.Fatalf("restored item %d output index = %d, want parser-only fact omitted", index, restored.Items[index].OutputIndex)
		}
		if !bytes.Equal(restored.Items[index].Raw, items[index].Raw) {
			t.Fatalf("restored item %d Raw changed", index)
		}
	}
	if !reflect.DeepEqual(
		transcriptEntriesFromHistoryReplacement(restored.Items, restored.CompactionNumber),
		transcriptEntriesFromHistoryReplacement(items, textutil.Value(1)),
	) {
		t.Fatal("transcript projection changed when parser output indexes were omitted")
	}
	beforeChunks, err := promptCacheChunks(llm.Request{Items: items})
	if err != nil {
		t.Fatalf("hash original provider history: %v", err)
	}
	afterChunks, err := promptCacheChunks(llm.Request{Items: restored.Items})
	if err != nil {
		t.Fatalf("hash restored provider history: %v", err)
	}
	if !reflect.DeepEqual(afterChunks, beforeChunks) {
		t.Fatal("cache lineage changed when parser output indexes were omitted")
	}
}
