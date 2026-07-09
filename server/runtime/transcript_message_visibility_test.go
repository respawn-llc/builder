package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestMessageTypeTranscriptVisibilityMatrix(t *testing.T) {
	cases := []struct {
		name        string
		messageType llm.MessageType
		want        transcript.EntryVisibility
	}{
		{name: "subagents", messageType: llm.MessageTypeSubagents, want: transcript.EntryVisibilityDetail},
		{name: "workflow mode", messageType: llm.MessageTypeWorkflowMode, want: transcript.EntryVisibilityOngoingCollapsed},
		{name: "worktree mode", messageType: llm.MessageTypeWorktreeMode, want: transcript.EntryVisibilityOngoing},
		{name: "worktree exit", messageType: llm.MessageTypeWorktreeModeExit, want: transcript.EntryVisibilityOngoing},
		{name: "goal", messageType: llm.MessageTypeGoal, want: transcript.EntryVisibilityOngoing},
		{name: "background notice", messageType: llm.MessageTypeBackgroundNotice, want: transcript.EntryVisibilityOngoingCollapsed},
		{name: "custom tool output", messageType: llm.MessageTypeCustomToolCallOutput, want: transcript.EntryVisibilityOngoing},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageTypeTranscriptVisibility(tt.messageType); got != tt.want {
				t.Fatalf("message type visibility = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCustomToolCallOutputProjectsAsCommittedToolRowFact(t *testing.T) {
	msg := llm.Message{
		Role:        llm.RoleTool,
		MessageType: llm.MessageTypeCustomToolCallOutput,
		ToolCallID:  "call-patch-1",
		Name:        string(toolspec.ToolPatch),
		Content:     `"patched"`,
	}
	completions := map[string]tools.Result{
		msg.ToolCallID: {
			CallID:        msg.ToolCallID,
			Name:          toolspec.ToolPatch,
			CondensedText: "patch result",
		},
	}

	facts := transcriptCommittedRowFactsFromMessage(msg, nil, completions, nil)
	if len(facts) != 1 || facts[0].Kind != TranscriptCommittedRowFactTool || facts[0].Tool == nil {
		t.Fatalf("custom tool output facts = %+v, want one tool row fact", facts)
	}
	fact := facts[0]
	if fact.Visibility != transcript.EntryVisibilityOngoingCollapsed {
		t.Fatalf("custom tool output fact visibility = %q, want %q", fact.Visibility, transcript.EntryVisibilityOngoingCollapsed)
	}
	if fact.Tool.ToolCallID != msg.ToolCallID || fact.Tool.ToolName != string(toolspec.ToolPatch) {
		t.Fatalf("custom tool output fact identity = %+v", fact.Tool)
	}
	if fact.Tool.CondensedText != "patch result" {
		t.Fatalf("custom tool output fact condensed text = %q, want completion metadata", fact.Tool.CondensedText)
	}
}
