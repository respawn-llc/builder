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

func TestTranscriptProjectionClassifiesBlankLegacyAssistantPhase(t *testing.T) {
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{{
			Kind: runtime.TranscriptCommittedRowFactAssistant,
			Assistant: &runtime.TranscriptAssistantRowFact{
				Text: "legacy final answer",
			},
		}},
	})
	if len(hydration.CommittedRows) != 1 || hydration.CommittedRows[0].Assistant == nil {
		t.Fatalf("hydration rows = %+v, want one assistant row", hydration.CommittedRows)
	}
	if got := hydration.CommittedRows[0].Assistant.Phase; got != clientui.TranscriptAssistantPhaseLegacyFinal {
		t.Fatalf("legacy assistant phase = %q, want explicit legacy final classification", got)
	}
}

func TestTranscriptPageProjectsReviewerAndBackgroundMetadata(t *testing.T) {
	exitCode := 9
	page := TranscriptPageFromSegment("58e121b5-30f7-4d0f-a1fa-fb3e6695e39c", "name", clientui.ConversationFreshnessEstablished, runtime.TranscriptSegmentPage{
		Snapshot: runtime.ChatSnapshot{Entries: []runtime.ChatEntry{
			{Role: "reviewer_status", Text: "review complete"},
			{
				Role:               "system",
				Text:               "background failed",
				MessageType:        llm.MessageTypeBackgroundNotice,
				BackgroundExitCode: &exitCode,
			},
		}},
	})

	if len(page.Entries) != 2 {
		t.Fatalf("page entries = %+v", page.Entries)
	}
	if got := page.Entries[0].MessageType; got != clientui.MessageTypeReviewerFeedback {
		t.Fatalf("reviewer message type = %q, want reviewer feedback", got)
	}
	if got := page.Entries[1].BackgroundExitCode; got == nil || *got != exitCode {
		t.Fatalf("background exit code = %+v, want %d", got, exitCode)
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

func TestTranscriptBackgroundActivityUsesRuntimeActivityID(t *testing.T) {
	activityID := uuid.New()
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind: runtime.EventBackgroundUpdated,
		Background: &runtime.BackgroundShellEvent{
			ID:         "1000",
			ActivityID: activityID,
			State:      "running",
			Preview:    "tests",
		},
	})
	if len(messages) != 1 || messages[0].BackgroundActivity == nil {
		t.Fatalf("messages = %+v, want one background activity", messages)
	}
	if got := messages[0].BackgroundActivity.ID; got != activityID.String() {
		t.Fatalf("background transcript id = %q, want activity id %q", got, activityID)
	}
}

func TestTranscriptBackgroundActivityRemovalFollowsLifecycleNotPreviewTruncation(t *testing.T) {
	tests := []struct {
		name           string
		eventType      runtime.BackgroundShellEventType
		previewRemoved int
		wantRemoved    bool
	}{
		{name: "running truncated preview remains live", eventType: runtime.BackgroundShellEventBackgrounded, previewRemoved: 2},
		{name: "completed activity leaves live band", eventType: runtime.BackgroundShellEventCompleted, wantRemoved: true},
		{name: "killed activity leaves live band", eventType: runtime.BackgroundShellEventKilled, wantRemoved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
				Kind: runtime.EventBackgroundUpdated,
				Background: &runtime.BackgroundShellEvent{
					Type:           tt.eventType,
					ID:             "1000",
					ActivityID:     uuid.New(),
					State:          string(tt.eventType),
					Command:        "sleep 2",
					PreviewRemoved: tt.previewRemoved,
				},
			})
			if len(messages) != 1 || messages[0].BackgroundActivity == nil {
				t.Fatalf("messages = %+v, want one background activity", messages)
			}
			if got := messages[0].BackgroundActivity.Removed; got != tt.wantRemoved {
				t.Fatalf("background activity removed = %t, want %t", got, tt.wantRemoved)
			}
		})
	}
}

func TestTranscriptBackgroundNoticeCarriesTypedExitCode(t *testing.T) {
	exitCode := 3
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind: runtime.EventConversationUpdated,
		Message: llm.Message{
			Role:               llm.RoleDeveloper,
			MessageType:        llm.MessageTypeBackgroundNotice,
			Content:            "background failed",
			CompactContent:     "background failed",
			BackgroundExitCode: &exitCode,
		},
	})

	if len(messages) != 1 || messages[0].CommittedRow == nil || messages[0].CommittedRow.Notice == nil {
		t.Fatalf("messages = %+v, want one background notice", messages)
	}
	got := messages[0].CommittedRow.Notice.Data.BackgroundExitCode
	if got == nil || *got != exitCode {
		t.Fatalf("background exit code = %+v, want %d", got, exitCode)
	}
}

func TestTranscriptBackgroundActivityRejectsMissingRuntimeActivityID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected missing background activity id panic")
		}
	}()

	_ = TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind: runtime.EventBackgroundUpdated,
		Background: &runtime.BackgroundShellEvent{
			ID:      "1000",
			State:   "running",
			Preview: "tests",
		},
	})
}

func TestAssistantTranscriptMessagesDoNotReemitLiveToolStarts(t *testing.T) {
	for _, kind := range []runtime.EventKind{runtime.EventAssistantMessage, runtime.EventConversationUpdated} {
		t.Run(string(kind), func(t *testing.T) {
			messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
				Kind: kind,
				Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: "checking the repo",
					ToolCalls: []llm.ToolCall{{
						ID:   "call-1",
						Name: "shell",
					}},
				},
			})
			if len(messages) != 1 {
				t.Fatalf("messages = %+v, want only assistant committed row", messages)
			}
			if messages[0].Kind != clientui.TranscriptMessageCommittedRow || messages[0].CommittedRow == nil || messages[0].CommittedRow.Assistant == nil {
				t.Fatalf("message = %+v, want assistant committed row", messages[0])
			}
		})
	}
}
