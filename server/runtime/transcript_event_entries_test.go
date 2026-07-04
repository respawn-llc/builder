package runtime

import (
	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"encoding/json"
	"strings"
	"testing"
)

func TestTranscriptEntriesFromEventBuildsToolCallFallbackWithoutPresentation(t *testing.T) {
	entries := TranscriptEntriesFromEvent(Event{
		Kind: EventToolCallStarted,
		ToolCall: &llm.ToolCall{
			ID:    "call-1",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"command":"pwd"}`),
		},
	})
	if len(entries) != 1 {
		t.Fatalf("expected one transcript entry, got %+v", entries)
	}
	entry := entries[0]
	if entry.Role != "tool_call" {
		t.Fatalf("entry role = %q, want tool_call", entry.Role)
	}
	if entry.Text != "pwd" {
		t.Fatalf("entry text = %q, want pwd", entry.Text)
	}
	if entry.ToolCall == nil || !entry.ToolCall.IsShell {
		t.Fatalf("expected rebuilt shell tool metadata, got %+v", entry.ToolCall)
	}
	if entry.ToolCall.Command != "pwd" {
		t.Fatalf("tool metadata command = %q, want pwd", entry.ToolCall.Command)
	}
}

func TestNormalizeToolCallForTranscriptRepairsMalformedPresentation(t *testing.T) {
	normalized := normalizeToolCallForTranscript(llm.ToolCall{
		ID:           "call-1",
		Name:         string(toolspec.ToolExecCommand),
		Presentation: json.RawMessage(`{"broken":`),
		Input:        json.RawMessage(`{"command":"pwd"}`),
	}, "/tmp")
	meta := decodeToolCallMeta(normalized)
	if meta == nil {
		t.Fatal("expected rebuilt tool presentation metadata")
	}
	if !meta.IsShell {
		t.Fatalf("expected rebuilt shell metadata, got %+v", meta)
	}
	if meta.Command != "pwd" {
		t.Fatalf("rebuilt command = %q, want pwd", meta.Command)
	}
}

func TestTranscriptEntriesFromEventEmitsVisibleToolCompletionEntriesForOrdinaryAndTriggerHandoffTools(t *testing.T) {
	testCases := []struct {
		name   string
		result tools.Result
	}{
		{
			name: "ordinary shell result",
			result: tools.Result{
				CallID:        "call-shell-1",
				Name:          toolspec.ToolExecCommand,
				Output:        json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`),
				CondensedText: "compact shell result",
			},
		},
		{
			name: "trigger handoff synthetic success result",
			result: tools.Result{
				CallID: "call-handoff-1",
				Name:   toolspec.ToolTriggerHandoff,
				Output: json.RawMessage(`""`),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			entries := TranscriptEntriesFromEvent(Event{
				Kind:       EventToolCallCompleted,
				ToolResult: &tc.result,
			})
			if len(entries) != 1 {
				t.Fatalf("expected one visible transcript entry, got %+v", entries)
			}
			entry := entries[0]
			if entry.Role != "tool_result_ok" {
				t.Fatalf("entry role = %q, want tool_result_ok", entry.Role)
			}
			if entry.ToolCallID != tc.result.CallID {
				t.Fatalf("entry tool call id = %q, want %q", entry.ToolCallID, tc.result.CallID)
			}
			if entry.CondensedText != strings.TrimSpace(tc.result.CondensedText) {
				t.Fatalf("entry condensed text = %q, want %q", entry.CondensedText, tc.result.CondensedText)
			}
		})
	}
}

func TestTranscriptEntriesFromEventOmitsPrePersistCompactionStatusRows(t *testing.T) {
	testCases := []struct {
		name string
		evt  Event
	}{
		{
			name: "compaction completed",
			evt: Event{
				Kind: EventCompactionCompleted,
				Compaction: &CompactionStatus{
					Mode:  "auto",
					Count: 1,
				},
			},
		},
		{
			name: "compaction failed",
			evt: Event{
				Kind: EventCompactionFailed,
				Compaction: &CompactionStatus{
					Mode:  "manual",
					Error: "quota exceeded",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if entries := TranscriptEntriesFromEvent(tc.evt); len(entries) != 0 {
				t.Fatalf("expected no transcript entries for pre-persist compaction status, got %+v", entries)
			}
		})
	}
}
