package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/runtimewirefixture"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	edittool "core/server/tools/edit"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestEditAliasCompletionDiffAndReviewerEditsFlow(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	target := filepath.Join(workspace, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store := mustCreateNamedTestSessionAt(t, filepath.Join(t.TempDir(), "sessions"), "ws", workspace)
	editTool, err := edittool.New(runtimewirefixture.FilesystemContext(t, workspace))
	if err != nil {
		t.Fatalf("new edit tool: %v", err)
	}
	editInput, _ := json.Marshal(map[string]any{
		"filePath": "a.txt",
		"oldText":  "old",
		"newText":  "new",
	})
	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-edit-1",
				Name:  "replace",
				Input: editInput,
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":[]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolEdit, Handler: editTool}), Config{
		Model:        "claude",
		EnabledTools: []toolspec.ID{toolspec.ToolEdit},
		Reviewer: ReviewerConfig{
			Frequency:     "edits",
			Model:         "claude",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})
	msg, err := eng.SubmitUserMessage(context.Background(), "edit file")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "final" {
		t.Fatalf("assistant content = %q, want final", messageContent(msg))
	}
	if len(mainClient.calls) < 2 {
		t.Fatalf("provider calls = %d, want continuation request", len(mainClient.calls))
	}
	var continuedCall *llm.ToolCall
	for _, message := range requestMessages(mainClient.calls[1]) {
		for index := range message.ToolCalls {
			if message.ToolCalls[index].ID == "call-edit-1" {
				call := message.ToolCalls[index]
				continuedCall = &call
			}
		}
	}
	if continuedCall == nil ||
		continuedCall.Name != string(toolspec.ToolEdit) ||
		string(continuedCall.Input) != `{"new_string":"new","old_string":"old","path":"a.txt"}` {
		t.Fatalf("continuation tool call = %+v, want canonical call", continuedCall)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(data) != "new\n" {
		t.Fatalf("edited content = %q, want new", string(data))
	}
	waitEngineLifecycleTasks(t, eng)
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to run after edit, got %d calls", len(reviewerClient.calls))
	}
	records, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("collect persisted events: %v", err)
	}
	var persistedCall *llm.ToolCall
	for _, event := range records {
		message, ok := mustSessionEventPayload(event.Record).(session.MessageRecord)
		if !ok {
			continue
		}
		restored, err := llmMessageFromSessionRecord(message)
		if err != nil {
			t.Fatalf("restore persisted assistant message: %v", err)
		}
		for index := range restored.ToolCalls {
			if restored.ToolCalls[index].ID == "call-edit-1" {
				call := restored.ToolCalls[index]
				persistedCall = &call
			}
		}
	}
	if persistedCall == nil {
		t.Fatal("persisted assistant history omitted alias-only Edit call")
	}
	if string(persistedCall.Input) != `{"new_string":"new","old_string":"old","path":"a.txt"}` {
		t.Fatalf("persisted Edit input = %s, want canonical provider input", persistedCall.Input)
	}
	persistedMeta := transcriptToolCallMeta(*persistedCall, workspace)
	if persistedMeta.PatchRender == nil || len(persistedMeta.PatchRender.Files) != 1 ||
		persistedMeta.PatchRender.Files[0].RelPath != "./a.txt" {
		t.Fatalf("persisted Edit presentation = %+v, want a.txt diff", persistedMeta)
	}

	snapshot := eng.ChatSnapshot()
	var callMetaName string
	var resultHasDiff bool
	for _, entry := range snapshot.Entries {
		if entry.ToolCallID != "call-edit-1" || entry.ToolCall == nil {
			continue
		}
		if entry.Role == "tool_call" {
			callMetaName = entry.ToolCall.ToolName
		}
		if entry.Role == "tool_result_ok" && entry.ToolCall.PatchRender != nil {
			resultHasDiff = true
		}
	}
	if callMetaName != string(toolspec.ToolEdit) {
		t.Fatalf("tool call metadata name = %q, want edit", callMetaName)
	}
	if !resultHasDiff {
		t.Fatalf("expected edit completion result to carry diff metadata, snapshot=%+v", snapshot.Entries)
	}
}
