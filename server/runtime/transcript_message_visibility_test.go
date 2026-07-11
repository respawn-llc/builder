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

func TestAssistantCommentaryIsDetailOnlyWhileFinalAnswersRemainOngoing(t *testing.T) {
	tests := []struct {
		name  string
		phase llm.MessagePhase
		want  transcript.EntryVisibility
	}{
		{name: "commentary", phase: llm.MessagePhaseCommentary, want: transcript.EntryVisibilityDetail},
		{name: "final", phase: llm.MessagePhaseFinal, want: transcript.EntryVisibilityOngoing},
		{name: "legacy final", want: transcript.EntryVisibilityOngoing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := VisibleChatEntriesFromMessage(llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   test.phase,
				Content: "assistant content",
			})
			if len(entries) != 1 || entries[0].Visibility != test.want {
				t.Fatalf("assistant entries = %+v, want one %q row", entries, test.want)
			}
			facts := transcriptCommittedRowFactsFromMessage(llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   test.phase,
				Content: "assistant content",
			}, nil, nil, nil)
			if len(facts) != 1 || facts[0].Visibility != test.want {
				t.Fatalf("assistant facts = %+v, want one %q row", facts, test.want)
			}
			scan := newInMemoryTranscriptScan(inMemoryTranscriptScanRequest{Limit: 1}, nil, nil)
			scan.ApplyMessage(llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   test.phase,
				Content: "assistant content",
			}, 1)
			scannedEntries := scan.PageSnapshot().Snapshot.Entries
			if len(scannedEntries) != 1 || scannedEntries[0].Visibility != test.want {
				t.Fatalf("scanned assistant entries = %+v, want one %q row", scannedEntries, test.want)
			}
		})
	}
}

func TestUnknownDeveloperMessageVisibilityDependsOnRecoverableContent(t *testing.T) {
	unknownType := llm.MessageType("unknown_future_context")

	withText := VisibleChatEntriesFromMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: unknownType, Content: "recoverable text"})
	if len(withText) != 1 || withText[0].Visibility != transcript.EntryVisibilityOngoing || withText[0].Text != "recoverable text" {
		t.Fatalf("unknown developer with text entries = %+v, want ongoing recoverable text", withText)
	}

	emptyUnknown := VisibleChatEntriesFromMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: unknownType, Content: " \n\t "})
	if len(emptyUnknown) != 1 || emptyUnknown[0].Visibility != transcript.EntryVisibilityDetail {
		t.Fatalf("empty unknown developer entries = %+v, want detail diagnostic", emptyUnknown)
	}

	emptyUntyped := VisibleChatEntriesFromMessage(llm.Message{Role: llm.RoleDeveloper, Content: " \n\t "})
	if len(emptyUntyped) != 0 {
		t.Fatalf("empty untyped developer entries = %+v, want hidden no-op", emptyUntyped)
	}
}

func TestEmptyUnknownDeveloperMessageProjectsDetailDiagnosticFact(t *testing.T) {
	unknownType := llm.MessageType("unknown_future_context")
	facts := transcriptCommittedRowFactsFromMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: unknownType, Content: " "}, nil, nil, nil)

	if len(facts) != 1 || facts[0].Kind != TranscriptCommittedRowFactNotice || facts[0].Visibility != transcript.EntryVisibilityDetail || facts[0].Notice == nil {
		t.Fatalf("empty unknown developer facts = %+v, want one detail diagnostic notice", facts)
	}
	notice := facts[0].Notice
	if notice.DiagnosticCode != string(unknownType) || notice.DiagnosticDetail == "" {
		t.Fatalf("diagnostic notice = %+v, want unknown type code and non-empty detail", notice)
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
