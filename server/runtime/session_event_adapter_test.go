package runtime

import (
	"bytes"
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
	restored := llmMessageFromSessionRecord(record)
	if !reflect.DeepEqual(restored, message) {
		t.Fatalf("restored message = %#v, want %#v", restored, message)
	}
}

func TestDecodedSessionRecordsReachRuntimeUnchanged(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	appendRawCurrentEventLine(t, store, []byte(`{"seq":1,"kind":"message","payload":{"role":"assistant","content":"kept exactly","tool_calls":[{"call_id":"call-1","name":"exec_command","kind":"function","presentation":{"ToolName":"exec_command"},"input":{"cmd":"pwd"}}]}}`))
	appendRawCurrentEventLine(t, store, []byte(`{"seq":2,"kind":"tool_completed","payload":{"call_id":"call-1","name":"exec_command","output_kind":"function","is_error":false,"output":{"cwd":"/tmp"},"summary":"kept summary","presentation":{"ToolName":"exec_command"},"provider_items":[{"type":"function_call_output","call_id":"call-1","raw":{"type":"function_call_output","call_id":"call-1","output":"{\"cwd\":\"/tmp\"}"}}]}}`))
	appendRawCurrentEventLine(t, store, []byte(`{"seq":3,"kind":"local_entry","payload":{"visibility":"detail","role":"developer","text":"kept local text","after_tool_call_id":"call-1"}}`))
	appendRawCurrentEventLine(t, store, []byte(`{"seq":4,"kind":"history_replaced","payload":{"engine":"local","mode":"auto","compaction_number":2,"items":[{"type":"message","role":"user","content":"kept history","raw":{"type":"message","role":"user","content":"kept history"}}]}}`))

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("decode persisted records: %v", err)
	}
	if len(window.Records) != 4 {
		t.Fatalf("decoded record count = %d, want 4", len(window.Records))
	}

	message := llmMessageFromSessionRecord(mustSessionEventPayload(window.Records[0]).(session.MessageRecord))
	if message.Content == nil || *message.Content != "kept exactly" ||
		len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "call-1" ||
		message.ToolCalls[0].Name != "exec_command" {
		t.Fatalf("runtime message changed decoded facts: %#v", message)
	}

	completion := storedToolCompletionFromSessionRecord(
		mustSessionEventPayload(window.Records[1]).(session.ToolCompletionRecord),
	)
	if completion.CallID != "call-1" || completion.Name != "exec_command" ||
		completion.Summary == nil || *completion.Summary != "kept summary" ||
		len(completion.ProviderItems) != 1 {
		t.Fatalf("runtime completion changed decoded facts: %#v", completion)
	}

	entry := storedLocalEntryFromSessionRecord(
		mustSessionEventPayload(window.Records[2]).(session.LocalEntryRecord),
	)
	if entry.Visibility != transcript.EntryVisibilityDetail ||
		entry.Text != "kept local text" ||
		entry.AfterToolCallID == nil || *entry.AfterToolCallID != "call-1" {
		t.Fatalf("runtime local entry changed decoded facts: %#v", entry)
	}

	replacement := historyReplacementPayloadFromSessionRecord(
		mustSessionEventPayload(window.Records[3]).(session.HistoryReplacementRecord),
	)
	if replacement.Engine != "local" || replacement.Mode != string(compactionModeAuto) ||
		replacement.CompactionNumber == nil || *replacement.CompactionNumber != 2 ||
		len(replacement.Items) != 1 ||
		replacement.Items[0].Content == nil || *replacement.Items[0].Content != "kept history" {
		t.Fatalf("runtime history replacement changed decoded facts: %#v", replacement)
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
	restored := llmMessageFromSessionRecord(record)
	if len(restored.ToolCalls) != 1 || restored.ToolCalls[0].ID != "call-1" {
		t.Fatalf("restored tool calls = %#v", restored.ToolCalls)
	}
}

func TestSessionMessageRecordAdapterPreservesAbsentOptionalFacts(t *testing.T) {
	t.Parallel()
	record, err := sessionMessageRecordFromLLM(llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("hello"),
	})
	if err != nil {
		t.Fatalf("adapt message with absent optional facts: %v", err)
	}
	restored := llmMessageFromSessionRecord(record)
	if restored.MessageType != nil ||
		restored.SourcePath != nil ||
		restored.CompactContent != nil ||
		restored.Name != nil ||
		restored.ToolCallID != nil ||
		restored.Phase != nil ||
		restored.BackgroundActivityID != nil {
		t.Fatalf("restored absent facts became present: %#v", restored)
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
	restored := storedToolCompletionFromSessionRecord(record)
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
	restoredLocal := storedLocalEntryFromSessionRecord(localRecord)
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
			restored := storedToolCompletionFromSessionRecord(record)
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
	restored := historyReplacementPayloadFromSessionRecord(record)
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
		restored := historyReplacementPayloadFromSessionRecord(record)
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
	restored := historyReplacementPayloadFromSessionRecord(record)
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
