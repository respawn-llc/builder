package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/transcript"
)

func TestTranscriptCommittedRowFactsFromSnapshotOwnsKindsVisibilityAndIntegrity(t *testing.T) {
	facts := TranscriptCommittedRowFactsFromSnapshot(ChatSnapshot{Entries: []ChatEntry{
		{Visibility: transcript.EntryVisibilityHidden, Role: "assistant", Text: "hidden"},
		{Role: "user", Text: "valid user"},
		{Role: "assistant", CompactLabel: "recoverable assistant"},
		{
			Role:       "tool_result_ok",
			Text:       "recoverable tool output",
			ToolCallID: "7ad05ef3-9afb-4be6-aaba-6a746fd87d80",
			ToolCall: &transcript.ToolCallMeta{
				ToolName:       "exec_command",
				Presentation:   transcript.ToolPresentationShell,
				RenderBehavior: transcript.ToolCallRenderBehaviorShell,
				RenderHint:     &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindSource},
			},
		},
		{
			Visibility:  transcript.EntryVisibilityDetail,
			Role:        string(transcript.EntryRoleWarning),
			Text:        "compaction reminder",
			MessageType: llm.MessageTypeCompactionSoonReminder,
		},
		{
			Visibility:    transcript.EntryVisibilityOngoingCollapsed,
			Role:          string(transcript.EntryRoleSystem),
			Text:          "background detail",
			CondensedText: "background compact",
			MessageType:   llm.MessageTypeBackgroundNotice,
		},
		{Role: "unknown_notice"},
	}})

	if len(facts) != 6 {
		t.Fatalf("snapshot facts = %+v, want six visible rows", facts)
	}
	if got := facts[0]; got.Kind != TranscriptCommittedRowFactUser ||
		got.Visibility != transcript.EntryVisibilityOngoing ||
		got.Integrity != transcript.RowIntegrityValid {
		t.Fatalf("valid user fact = %+v", got)
	}
	if got := facts[1]; got.Kind != TranscriptCommittedRowFactAssistant ||
		got.Visibility != transcript.EntryVisibilityOngoing ||
		got.Integrity != transcript.RowIntegrityRecoverableMalformed ||
		got.Assistant == nil ||
		got.Assistant.Text != "recoverable assistant" {
		t.Fatalf("recoverable assistant fact = %+v", got)
	}
	if got := facts[2]; got.Kind != TranscriptCommittedRowFactTool ||
		got.Visibility != transcript.EntryVisibilityOngoing ||
		got.Integrity != transcript.RowIntegrityRecoverableMalformed {
		t.Fatalf("recoverable tool fact = %+v", got)
	}
	if got := facts[3]; got.Kind != TranscriptCommittedRowFactNotice ||
		got.Visibility != transcript.EntryVisibilityDetail ||
		got.Integrity != transcript.RowIntegrityValid {
		t.Fatalf("valid warning fact = %+v", got)
	}
	if got := facts[4]; got.Kind != TranscriptCommittedRowFactNotice ||
		got.Visibility != transcript.EntryVisibilityOngoingCollapsed ||
		got.Integrity != transcript.RowIntegrityValid {
		t.Fatalf("valid system fact = %+v", got)
	}
	if got := facts[5]; got.Kind != TranscriptCommittedRowFactNotice ||
		got.Visibility != transcript.EntryVisibilityDetail ||
		got.Integrity != transcript.RowIntegrityUnrecoverableMalformed {
		t.Fatalf("unrecoverable notice fact = %+v", got)
	}
	for _, fact := range facts {
		switch fact.Visibility {
		case transcript.EntryVisibilityOngoing,
			transcript.EntryVisibilityOngoingCollapsed,
			transcript.EntryVisibilityDetail:
		default:
			t.Fatalf("delivered fact visibility = %q, want explicit visible value", fact.Visibility)
		}
	}
}

func TestTranscriptDetailSnapshotOmitsRawToolCallEntries(t *testing.T) {
	facts := TranscriptCommittedRowFactsFromSnapshot(ChatSnapshot{Entries: []ChatEntry{
		{
			Role:       "tool_call",
			Text:       "sed -n '220,335p' cli/tui/transcriptrender/tool.go",
			ToolCallID: "e4f34dd0-1d6d-4f98-a5e2-7dd8817def5a",
			ToolCall: &transcript.ToolCallMeta{
				ToolName:       "exec_command",
				Presentation:   transcript.ToolPresentationShell,
				RenderBehavior: transcript.ToolCallRenderBehaviorShell,
				Command:        "sed -n '220,335p' cli/tui/transcriptrender/tool.go",
			},
		},
		{
			Role:       "tool_result_ok",
			Text:       "func renderToolRow() {}",
			ToolCallID: "e4f34dd0-1d6d-4f98-a5e2-7dd8817def5a",
			ToolCall: &transcript.ToolCallMeta{
				ToolName:       "exec_command",
				Presentation:   transcript.ToolPresentationShell,
				RenderBehavior: transcript.ToolCallRenderBehaviorShell,
				Command:        "sed -n '220,335p' cli/tui/transcriptrender/tool.go",
			},
		},
	}})

	if len(facts) != 1 {
		t.Fatalf("snapshot facts = %+v, want completed tool result only", facts)
	}
	if facts[0].Kind != TranscriptCommittedRowFactTool || facts[0].Tool == nil {
		t.Fatalf("snapshot fact = %+v, want tool result", facts[0])
	}
	if facts[0].Tool.Text != "func renderToolRow() {}" {
		t.Fatalf("tool result text = %q, want completed output", facts[0].Tool.Text)
	}
	if facts[0].Tool.Presentation == nil ||
		facts[0].Tool.Presentation.Command != "sed -n '220,335p' cli/tui/transcriptrender/tool.go" {
		t.Fatalf("tool result presentation = %+v, want typed input metadata", facts[0].Tool.Presentation)
	}
}
