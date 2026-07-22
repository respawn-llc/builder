package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestBuildRequestUsesLatestHistoryReplacementAndActiveTail(t *testing.T) {
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
