package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"core/server/llm"
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
	editTool, err := edittool.New(workspace, true)
	if err != nil {
		t.Fatalf("new edit tool: %v", err)
	}
	editInput, _ := json.Marshal(map[string]any{
		"path":       "a.txt",
		"old_string": "old",
		"new_string": "new",
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

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolEdit, Handler: editTool}), Config{
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
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(data) != "new\n" {
		t.Fatalf("edited content = %q, want new", string(data))
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("expected reviewer to run after edit, got %d calls", len(reviewerClient.calls))
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
