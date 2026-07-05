package runtimeview

import (
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestTranscriptHydrationCarriesRuntimeNativeAssistantStreamIdentity(t *testing.T) {
	streamID := uuid.MustParse("f84c7d21-4c94-4a54-87fd-b41f5bd01d38")
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		ActiveAssistantText:     "hello",
		ActiveAssistantStreamID: &streamID,
		ActiveAssistantPhase:    "final_answer",
	})
	if hydration.ActiveAssistantStream == nil {
		t.Fatal("expected active assistant stream in hydration")
	}
	if got := hydration.ActiveAssistantStream.StreamID; got != streamID {
		t.Fatalf("active assistant stream id = %q, want %q", got, streamID)
	}
	if hydration.ActiveAssistantStream.Text != "hello" {
		t.Fatalf("active assistant stream text = %q, want hello", hydration.ActiveAssistantStream.Text)
	}
	if hydration.ActiveAssistantStream.Phase != "final_answer" {
		t.Fatalf("active assistant stream phase = %q, want final_answer", hydration.ActiveAssistantStream.Phase)
	}
}

func TestTranscriptCommittedRowsPreserveRuntimeVisibility(t *testing.T) {
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                runtime.EventLocalEntryAdded,
		LocalEntryProjected: true,
		LocalEntry: &runtime.ChatEntry{
			Visibility: transcript.EntryVisibilityDetail,
			Role:       "user",
			Text:       "detail-only row",
		},
	})
	if len(messages) != 1 || messages[0].CommittedRow == nil {
		t.Fatalf("messages = %+v, want one committed row", messages)
	}
	if got := messages[0].CommittedRow.Visibility; got != clientui.EntryVisibilityDetail {
		t.Fatalf("committed row visibility = %q, want detail", got)
	}

	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{{
			Visibility: transcript.EntryVisibilityHidden,
			Kind:       runtime.TranscriptCommittedRowFactUser,
			User:       &runtime.TranscriptUserRowFact{Text: "hidden row"},
		}},
	})
	if len(hydration.CommittedRows) != 1 {
		t.Fatalf("hydration rows = %+v, want one committed row", hydration.CommittedRows)
	}
	if got := hydration.CommittedRows[0].Visibility; got != clientui.EntryVisibilityHidden {
		t.Fatalf("hydration visibility = %q, want hidden", got)
	}
}

func TestTranscriptHydrationOmitsAssistantStreamWithoutRuntimeIdentity(t *testing.T) {
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		ActiveAssistantText:     "hello",
		ActiveAssistantMetadata: &runtime.AssistantStreamMetadata{StepID: "step-1"},
	})
	if hydration.ActiveAssistantStream != nil {
		t.Fatalf("active assistant stream = %+v, want nil without stream id", hydration.ActiveAssistantStream)
	}
}

func TestTranscriptMessagesIgnoreEmptyAssistantDelta(t *testing.T) {
	streamID := uuid.New()
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                        runtime.EventAssistantDelta,
		AssistantDelta:              "",
		AssistantTranscriptStreamID: &streamID,
	})
	if len(messages) != 0 {
		t.Fatalf("empty assistant delta messages = %+v, want none", messages)
	}
}

func TestTranscriptMessagesIgnoreNoopAssistantResetWithoutStream(t *testing.T) {
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventAssistantDeltaReset,
		AssistantStreamAbortReason: string(runtime.AssistantStreamAbortSuperseded),
	})
	if len(messages) != 0 {
		t.Fatalf("noop assistant reset messages = %+v, want none", messages)
	}
}

func TestTranscriptMessagesEmitAssistantRowBeforeToolStarts(t *testing.T) {
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind: runtime.EventAssistantMessage,
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "checking the repo",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-1",
				Name: "shell",
			}},
		},
	})
	if len(messages) != 2 {
		t.Fatalf("messages = %+v, want assistant row then tool start", messages)
	}
	if messages[0].Kind != clientui.TranscriptMessageCommittedRow || messages[0].CommittedRow == nil || messages[0].CommittedRow.Assistant == nil {
		t.Fatalf("first message = %+v, want assistant committed row", messages[0])
	}
	if messages[1].Kind != clientui.TranscriptMessageToolStart || messages[1].ToolStart == nil {
		t.Fatalf("second message = %+v, want tool start", messages[1])
	}
}
