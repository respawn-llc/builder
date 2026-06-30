package runtimeview

import (
	"testing"

	"core/server/runtime"
	"core/shared/clientui"
)

func TestCommittedTranscriptSuffixFromSegmentDoesNotAdvertiseUnknownOlderBounds(t *testing.T) {
	suffix := CommittedTranscriptSuffixFromSegment(
		"session-1",
		"Session 1",
		clientui.ConversationFreshnessEstablished,
		7,
		runtime.TranscriptSegmentPage{
			Snapshot:     runtime.ChatSnapshot{Entries: []runtime.ChatEntry{{Role: "assistant", Text: "tail"}}},
			HasMoreAbove: true,
		},
	)

	if suffix.HasMoreCommittedEntries {
		t.Fatal("segment-only suffix must not advertise older entries without absolute committed bounds")
	}
	if suffix.StartEntryCount != 0 || suffix.NextEntryCount != 1 || suffix.CommittedEntryCount != 1 {
		t.Fatalf("suffix bounds = start:%d next:%d total:%d, want segment-local 0/1/1", suffix.StartEntryCount, suffix.NextEntryCount, suffix.CommittedEntryCount)
	}
}
