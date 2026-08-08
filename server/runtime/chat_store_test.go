package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestBuildRequestUsesLatestHistoryReplacementAndActiveTail(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	for _, message := range []llm.Message{
		{Role: llm.RoleUser, Content: textutil.Value("before")},
	} {
		if _, _, err := appendTestEvent(t, store, "step", message); err != nil {
			t.Fatalf("append pre-compaction message: %v", err)
		}
	}
	if _, _, err := appendTestEvent(t, store, "step", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeAuto),
		Items: llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleUser,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("first summary"),
		}}),
	}); err != nil {
		t.Fatalf("append first history replacement: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("between"),
	}); err != nil {
		t.Fatalf("append between-compaction message: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "step", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeAuto),
		Items: llm.ItemsFromMessages([]llm.Message{
			{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeEnvironment),
				Content:     textutil.Value("environment"),
			},
			{
				Role:        llm.RoleUser,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("latest summary"),
			},
		}),
	}); err != nil {
		t.Fatalf("append second history replacement: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("after"),
	}); err != nil {
		t.Fatalf("append active-tail assistant message: %v", err)
	}

	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if len(request.Items) < 3 {
		t.Fatalf("request items = %+v, want replacement plus active tail", request.Items)
	}
	tail := request.Items[len(request.Items)-3:]
	assertHistoryReplacementTail(t, tail)
	for _, item := range request.Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.Role != nil &&
			*item.Role == llm.RoleUser &&
			item.MessageType == nil {
			t.Fatalf("pre-compaction ordinary user item reached the rebuilt request: %+v", item)
		}
	}
}

func TestHistoryReplacementRebasesDanglingToolCallStepOwnership(t *testing.T) {
	t.Parallel()
	const replacementStepID = "22222222-2222-4222-8222-222222222222"
	message := llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    "call-rebased",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}},
	}
	items := llm.ItemsFromMessages([]llm.Message{message})

	live := newChatStoreWithCWD(t.TempDir())
	if err := live.appendMessage(chatStoreTestStepID, message); err != nil {
		t.Fatalf("append live message: %v", err)
	}
	live.replaceHistory(replacementStepID, items)

	reopened := newChatStoreWithCWD(t.TempDir())
	reopened.replaceHistory(replacementStepID, items)

	for name, chat := range map[string]*chatStore{"live": live, "reopened": reopened} {
		dangling := chat.danglingToolCalls()
		if len(dangling) != 1 {
			t.Fatalf("%s dangling calls = %+v, want one", name, dangling)
		}
		if dangling[0].stepID == nil || *dangling[0].stepID != replacementStepID {
			t.Fatalf(
				"%s dangling call step = %v, want replacement step %q",
				name,
				dangling[0].stepID,
				replacementStepID,
			)
		}
	}
}

func TestConflictingAssistantToolCallStepReturnsError(t *testing.T) {
	t.Parallel()
	const conflictingStepID = "22222222-2222-4222-8222-222222222222"
	message := llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    "call-conflict",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}},
	}

	t.Run("before persistence", func(t *testing.T) {
		store := mustCreateTestSession(t)
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
		intent := steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{message},
		)
		if err := engine.steer(chatStoreTestStepID, intent); err != nil {
			t.Fatalf("append initial tool call: %v", err)
		}
		before, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
		if err != nil {
			t.Fatalf("read records before conflict: %v", err)
		}

		if err := engine.steer(conflictingStepID, intent); err == nil {
			t.Fatal("conflicting tool-call Step identity was accepted")
		}
		after, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
		if err != nil {
			t.Fatalf("read records after conflict: %v", err)
		}
		if len(after.Records) != len(before.Records) {
			t.Fatalf(
				"conflicting tool call persisted records: before=%d after=%d",
				len(before.Records),
				len(after.Records),
			)
		}
	})

	t.Run("restore", func(t *testing.T) {
		store := mustCreateTestSession(t)
		for _, stepID := range []string{chatStoreTestStepID, conflictingStepID} {
			if _, _, err := appendTestEvent(t, store, stepID, message); err != nil {
				t.Fatalf("append conflicting persisted tool call: %v", err)
			}
		}
		engine, err := New(
			store,
			mustMaterializeTestEventLog(t, store),
			&fakeClient{},
			tools.NewRegistry(),
			Config{Model: "gpt-5"},
		)
		if err == nil {
			if closeErr := engine.Close(); closeErr != nil {
				t.Fatalf("close unexpectedly restored engine: %v", closeErr)
			}
			t.Fatal("restored conflicting tool-call Step identity")
		}
	})
}

func assertHistoryReplacementTail(t *testing.T, items []llm.ResponseItem) {
	t.Helper()
	if len(items) != 3 {
		t.Fatalf("history replacement tail = %+v, want exactly three items", items)
	}
	if items[0].Type != llm.ResponseItemTypeMessage ||
		items[0].Role == nil ||
		*items[0].Role != llm.RoleDeveloper ||
		items[0].MessageType == nil ||
		*items[0].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("history replacement environment item = %+v", items[0])
	}
	if items[1].Type != llm.ResponseItemTypeMessage ||
		items[1].Role == nil ||
		*items[1].Role != llm.RoleUser ||
		items[1].MessageType == nil ||
		*items[1].MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("history replacement summary item = %+v", items[1])
	}
	if items[2].Type != llm.ResponseItemTypeMessage ||
		items[2].Role == nil ||
		*items[2].Role != llm.RoleAssistant ||
		items[2].Phase == nil ||
		*items[2].Phase != llm.MessagePhaseFinal {
		t.Fatalf("active-tail assistant item = %+v", items[2])
	}
}

func TestBuildRequestPreservesMaterializedToolOutputOrder(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	calls := []llm.ToolCall{
		{ID: "call-one", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "call-two", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
	}
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: calls,
	}); err != nil {
		t.Fatalf("append tool calls: %v", err)
	}
	for _, call := range calls {
		output := json.RawMessage(`{"ok":true}`)
		if _, _, err := appendTestEvent(t, store, "step", storedToolCompletion{
			CallID: call.ID,
			Name:   call.Name,
			Output: output,
			ProviderItems: []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				CallID: textutil.Value(call.ID),
				Name:   textutil.Value(call.Name),
				Output: output,
			}},
		}); err != nil {
			t.Fatalf("append completion for %s: %v", call.ID, err)
		}
	}

	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	type toolItem struct {
		kind   llm.ResponseItemType
		callID string
	}
	var actual []toolItem
	for _, item := range request.Items {
		if !isToolCallItem(item.Type) && !isToolOutputItem(item.Type) {
			continue
		}
		callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if !present {
			t.Fatalf("tool request item lacks call identity: %+v", item)
		}
		actual = append(actual, toolItem{kind: item.Type, callID: callID})
	}
	want := []toolItem{
		{kind: llm.ResponseItemTypeFunctionCall, callID: "call-one"},
		{kind: llm.ResponseItemTypeFunctionCall, callID: "call-two"},
		{kind: llm.ResponseItemTypeFunctionCallOutput, callID: "call-one"},
		{kind: llm.ResponseItemTypeFunctionCallOutput, callID: "call-two"},
	}
	if len(actual) != len(want) {
		t.Fatalf("tool request item count = %d, want %d (%+v)", len(actual), len(want), actual)
	}
	for index, expected := range want {
		if actual[index] != expected {
			t.Fatalf("tool request item[%d] = %+v, want %+v", index, actual[index], expected)
		}
	}
}

func TestHistoryReplacementPrunesPriorToolWorkingState(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	calls := []llm.ToolCall{
		{ID: "pruned-one", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "pruned-two", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
	}
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: calls,
	}); err != nil {
		t.Fatalf("append pre-compaction tool calls: %v", err)
	}
	for _, call := range calls {
		if _, _, err := appendTestEvent(t, store, "step", storedToolCompletion{
			CallID: call.ID,
			Name:   call.Name,
			Output: json.RawMessage(`{"ok":true}`),
			ProviderItems: []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				CallID: textutil.Value(call.ID),
				Name:   textutil.Value(call.Name),
				Output: json.RawMessage(`{"ok":true}`),
			}},
		}); err != nil {
			t.Fatalf("append pre-compaction completion for %s: %v", call.ID, err)
		}
	}
	if _, _, err := appendTestEvent(t, store, "step", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeAuto),
		Items: llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleUser,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	}); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("active input"),
	}); err != nil {
		t.Fatalf("append active input: %v", err)
	}

	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	for _, call := range calls {
		if _, ok := engine.transcriptRuntimeState().ToolCompletionSnapshot(call.ID); ok {
			t.Fatalf("history replacement retained prior tool completion %q", call.ID)
		}
	}
	items := engine.transcriptRuntimeState().SnapshotItems()
	if len(items) != 2 {
		t.Fatalf("active provider items = %+v, want summary and active input", items)
	}
	if items[0].MessageType == nil || *items[0].MessageType != llm.MessageTypeCompactionSummary ||
		items[1].Role == nil || *items[1].Role != llm.RoleUser || items[1].MessageType != nil {
		t.Fatalf("active provider items = %+v, want typed summary plus user input", items)
	}
	for _, item := range items {
		if isToolCallItem(item.Type) || isToolOutputItem(item.Type) {
			t.Fatalf("history replacement retained a prior tool item: %+v", item)
		}
	}
}
