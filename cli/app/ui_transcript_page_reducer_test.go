package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
)

func TestReduceRuntimeTranscriptPageRejectsOlderTailRevision(t *testing.T) {
	reduction := reduceRuntimeTranscriptPage(newRuntimeTranscriptPageState(runtimeTranscriptPageSnapshot{
		entries:                 []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "newer", Committed: true}},
		revision:                11,
		effectiveRevision:       11,
		effectiveCommittedCount: 1,
		viewMode:                tui.ModeOngoing,
	}), clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "older"}},
	}, clientui.TranscriptRecoveryCauseNone)

	if reduction.decision != runtimeTranscriptPageDecisionReject {
		t.Fatalf("decision = %+v, want reject", reduction)
	}
	if reduction.rejectReason != "stale_revision" {
		t.Fatalf("reject reason = %q, want stale_revision", reduction.rejectReason)
	}
}

func TestReduceRuntimeTranscriptPageRejectsEqualRevisionTailPageThatClearsLiveOngoing(t *testing.T) {
	reduction := reduceRuntimeTranscriptPage(newRuntimeTranscriptPageState(runtimeTranscriptPageSnapshot{
		entries:                 []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed", Committed: true}},
		revision:                10,
		effectiveRevision:       10,
		effectiveCommittedCount: 1,
		viewMode:                tui.ModeOngoing,
		liveOngoing:             "working",
	}), clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}, clientui.TranscriptRecoveryCauseNone)

	if reduction.decision != runtimeTranscriptPageDecisionReject {
		t.Fatalf("decision = %+v, want reject", reduction)
	}
	if reduction.rejectReason != "same_revision_would_clear_ongoing" {
		t.Fatalf("reject reason = %q", reduction.rejectReason)
	}
}

func TestReduceRuntimeTranscriptPagePreservesLiveOngoingForEqualRevisionDetailPage(t *testing.T) {
	reduction := reduceRuntimeTranscriptPage(newRuntimeTranscriptPageState(runtimeTranscriptPageSnapshot{
		entries:                 []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed", Committed: true}},
		revision:                10,
		effectiveRevision:       10,
		effectiveCommittedCount: 1,
		viewMode:                tui.ModeDetail,
		liveOngoing:             "working",
		liveOngoingError:        "boom",
	}), clientui.TranscriptPageRequest{Cursor: 4096}, clientui.TranscriptPage{
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}, clientui.TranscriptRecoveryCauseNone)

	if reduction.decision != runtimeTranscriptPageDecisionApply {
		t.Fatalf("decision = %+v, want apply", reduction)
	}
	if !reduction.preserveLiveAssistantOngoing {
		t.Fatalf("expected preserve live assistant ongoing")
	}
	if reduction.page.Streaming != "working" || reduction.page.StreamingError != "boom" {
		t.Fatalf("ongoing = %q/%q, want working/boom", reduction.page.Streaming, reduction.page.StreamingError)
	}
}

func TestReduceRuntimeTranscriptPageDefaultDetailHydrationUsesDetailMerge(t *testing.T) {
	reduction := reduceRuntimeTranscriptPage(newRuntimeTranscriptPageState(runtimeTranscriptPageSnapshot{
		entries:                 []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed", Committed: true}},
		revision:                10,
		effectiveRevision:       10,
		effectiveCommittedCount: 1,
		viewMode:                tui.ModeDetail,
	}), clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}, clientui.TranscriptRecoveryCauseNone)

	if reduction.decision != runtimeTranscriptPageDecisionApply {
		t.Fatalf("decision = %+v, want apply", reduction)
	}
	if reduction.branch != "detail_merge" {
		t.Fatalf("branch = %q, want detail_merge", reduction.branch)
	}
}

func TestReduceRuntimeTranscriptPageAcceptsEqualRevisionTailCorrection(t *testing.T) {
	reduction := reduceRuntimeTranscriptPage(newRuntimeTranscriptPageState(runtimeTranscriptPageSnapshot{
		entries: []tui.TranscriptEntry{
			{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true},
			{Role: tui.TranscriptRoleToolCall, Text: "pwd", ToolCallID: "stale-call", Committed: true},
		},
		revision:                10,
		effectiveRevision:       10,
		effectiveCommittedCount: 2,
		viewMode:                tui.ModeOngoing,
		transcriptLiveDirty:     true,
	}), clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 3,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "tool_call", Text: "pwd", ToolCallID: "call-1"},
			{Role: "tool_result_ok", Text: "/tmp", ToolCallID: "call-1"},
		},
	}, clientui.TranscriptRecoveryCauseStreamGap)

	if reduction.decision != runtimeTranscriptPageDecisionApply {
		t.Fatalf("decision = %+v, want apply", reduction)
	}
	if reduction.branch != "recent_tail_replace" {
		t.Fatalf("branch = %q", reduction.branch)
	}
}

func TestRuntimeTranscriptPageSnapshotCopiesTranscriptEntries(t *testing.T) {
	m := newProjectedTestUIModel(nil, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed"}}

	snapshot := runtimeTranscriptPageSnapshotFromModel(m)
	m.transcriptEntries[0].Text = "mutated"

	if got := snapshot.entries[0].Text; got != "seed" {
		t.Fatalf("snapshot entry text = %q, want seed", got)
	}
}
