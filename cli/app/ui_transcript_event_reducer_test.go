package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcript"
)

func TestActiveAssistantFinalizerGapPreservesRecentTailWhenDetailPinned(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.forwardToView(tui.SetConversationMsg{Ongoing: "final answer"})

	recentTail := []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "tail prompt", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "tail answer", Committed: true},
		{Role: tui.TranscriptRoleUser, Text: "tail prompt 2", Committed: true},
	}
	m.transcriptBaseOffset = 37
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), recentTail...)
	m.transcriptTotalEntries = 40

	m.detailTranscript = uiDetailTranscriptWindow{
		sessionID:    "session-1",
		offset:       0,
		totalEntries: 50,
		entries:      []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "older prompt", Committed: true}},
		loaded:       true,
		hasMoreBelow: true,
		newerCursor:  1234,
		segments:     []residentSegmentMeta{{startLocal: 0, hasMoreBelow: true, newerCursor: 1234}},
	}

	a := uiRuntimeAdapter{model: m}
	_, handled := a.applyActiveAssistantFinalizerGapAsRecentTail(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        40,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
	})
	if !handled {
		t.Fatal("finalizer-gap event in detail mode should be handled")
	}
	if got := len(m.transcriptEntries); got != len(recentTail) {
		t.Fatalf("recent-tail backing entries = %d, want preserved %d (not clobbered to the single finalizer row)", got, len(recentTail))
	}
	if m.transcriptBaseOffset != 37 {
		t.Fatalf("recent-tail base offset = %d, want preserved 37", m.transcriptBaseOffset)
	}
}

func TestActiveAssistantFinalizerGapPreservesPinnedDetailWindow(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatal("setup: expected detail mode")
	}
	m.forwardToView(tui.SetConversationMsg{Ongoing: "final answer"})

	pinned := []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "older prompt", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "older answer", Committed: true},
	}
	m.detailTranscript = uiDetailTranscriptWindow{
		sessionID:    "session-1",
		offset:       0,
		totalEntries: 50,
		entries:      pinned,
		loaded:       true,
		hasMoreBelow: true,
		newerCursor:  1234,
		segments:     []residentSegmentMeta{{startLocal: 0, hasMoreBelow: true, newerCursor: 1234}},
	}

	a := uiRuntimeAdapter{model: m}
	cmd, handled := a.applyActiveAssistantFinalizerGapAsRecentTail(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        40,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "final answer",
		}},
	})
	if !handled {
		t.Fatal("finalizer-gap event in detail mode should be handled")
	}
	_ = cmd

	if got := len(m.detailTranscript.entries); got != len(pinned) {
		t.Fatalf("pinned detail window entry count = %d, want preserved %d", got, len(pinned))
	}
	if m.detailTranscript.offset != 0 || m.detailTranscript.totalEntries != 50 {
		t.Fatalf("pinned detail window bounds = offset %d total %d, want preserved 0/50", m.detailTranscript.offset, m.detailTranscript.totalEntries)
	}
	if m.detailTranscript.entries[0].Text != "older prompt" || m.detailTranscript.entries[1].Text != "older answer" {
		t.Fatal("pinned detail window content was mutated by the recent-tail finalizer gap")
	}
}

func TestActiveAssistantFinalizerGapRequestsRecentTailWhenDetailPinned(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.forwardToView(tui.SetConversationMsg{Ongoing: "final answer"})
	m.detailTranscript = uiDetailTranscriptWindow{
		sessionID:    "session-1",
		offset:       0,
		totalEntries: 50,
		entries:      []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "older prompt", Committed: true}},
		loaded:       true,
		hasMoreBelow: true,
		newerCursor:  1234,
		olderCursor:  10,
		hasMoreAbove: true,
		segments:     []residentSegmentMeta{{startLocal: 0, hasMoreBelow: true, newerCursor: 1234}},
	}

	a := uiRuntimeAdapter{model: m}
	cmd, handled := a.applyActiveAssistantFinalizerGapAsRecentTail(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        40,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
	})
	if !handled {
		t.Fatal("finalizer-gap event in pinned detail mode should be handled")
	}
	if cmd == nil {
		t.Fatal("expected pinned detail finalizer gap to request recent-tail sync")
	}
	if got := m.runtimeTranscriptActiveRequest.page; got != (clientui.TranscriptPageRequest{}) {
		t.Fatalf("finalizer-gap sync request = %+v, want recent tail request", got)
	}
	if got := m.runtimeTranscriptActiveRequest.syncCause; got != runtimeTranscriptSyncCauseCommittedGap {
		t.Fatalf("finalizer-gap sync cause = %q, want %q", got, runtimeTranscriptSyncCauseCommittedGap)
	}
}

func TestActiveAssistantFinalizerGapUsesEventBoundsAtTail(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.forwardToView(tui.SetConversationMsg{Ongoing: "final answer"})
	m.transcriptBaseOffset = 39
	m.transcriptTotalEntries = 40
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}}
	m.detailTranscript.setKnownBounds(39, 40)
	m.detailTranscript.replace(clientui.TranscriptPage{
		HasMoreAbove: true,
		Entries:      []clientui.ChatEntry{{Role: "user", Text: "prompt"}},
	})

	_, handled := uiRuntimeAdapter{model: m}.applyActiveAssistantFinalizerGapAsRecentTail(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        40,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        41,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
	})

	if !handled {
		t.Fatal("finalizer-gap event in detail mode should be handled")
	}
	if got := m.detailTranscript.offset; got != 39 {
		t.Fatalf("detail offset = %d, want existing tail offset 39", got)
	}
	if got := m.detailTranscript.totalEntries; got != 41 {
		t.Fatalf("detail total entries = %d, want finalizer total 41", got)
	}
	if got := len(m.detailTranscript.entries); got != 2 {
		t.Fatalf("detail entries = %d, want existing row plus finalizer", got)
	}
	if got := m.detailTranscript.entries[1].Text; got != "final answer" {
		t.Fatalf("finalizer entry text = %q, want appended final answer", got)
	}
}

func TestReduceProjectedTranscriptEventSkipsDuplicateToolStart(t *testing.T) {
	state := projectedTranscriptEventState{
		entries: []tui.TranscriptEntry{{
			Role:       tui.TranscriptRoleToolCall,
			Text:       "pwd",
			ToolCallID: "call-1",
			Committed:  true,
		}},
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind: clientui.EventToolCallStarted,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
		}},
	})

	if reduction.decision != projectedTranscriptDecisionSkip || !reduction.duplicateToolStarts {
		t.Fatalf("decision = %+v, want duplicate skip", reduction)
	}
	if reduction.skipReason != "duplicate_tool_call_start" {
		t.Fatalf("skip reason = %q", reduction.skipReason)
	}
}

func TestReduceProjectedTranscriptEventSkipsDuplicateTransientToolStart(t *testing.T) {
	state := projectedTranscriptEventState{
		entries: []tui.TranscriptEntry{{
			Role:       tui.TranscriptRoleToolCall,
			Text:       "pwd",
			ToolCallID: "call-1",
			Transient:  true,
		}},
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind: clientui.EventToolCallStarted,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
		}},
	})

	if reduction.decision != projectedTranscriptDecisionSkip || !reduction.duplicateToolStarts {
		t.Fatalf("decision = %+v, want duplicate transient tool start skip", reduction)
	}
}

func TestReduceProjectedTranscriptEventHydratesCommittedGap(t *testing.T) {
	state := projectedTranscriptEventState{
		baseOffset: 0,
		entries: []tui.TranscriptEntry{{
			Role:      tui.TranscriptRoleUser,
			Text:      "prompt",
			Committed: true,
		}},
		revision: 3,
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		TranscriptRevision:         4,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "answer"}},
	})

	if reduction.decision != projectedTranscriptDecisionHydrate {
		t.Fatalf("decision = %+v, want hydrate", reduction)
	}
	if reduction.plan.divergence != "gap_after_tail" {
		t.Fatalf("divergence = %q", reduction.plan.divergence)
	}
}

func TestReduceProjectedTranscriptEventAppendsCommittedSuffix(t *testing.T) {
	state := projectedTranscriptEventState{
		baseOffset: 0,
		entries: []tui.TranscriptEntry{{
			Role:      tui.TranscriptRoleUser,
			Text:      "prompt",
			Committed: true,
		}},
		revision: 1,
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptRevision:         2,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "assistant", Text: "answer"},
		},
	})

	if reduction.decision != projectedTranscriptDecisionApply || reduction.plan.mode != projectedTranscriptEntryPlanAppend {
		t.Fatalf("decision = %+v, want append apply", reduction)
	}
	if reduction.plan.rangeStart != 1 || reduction.plan.rangeEnd != 1 {
		t.Fatalf("range = [%d,%d], want [1,1]", reduction.plan.rangeStart, reduction.plan.rangeEnd)
	}
	if len(reduction.plan.entries) != 1 || reduction.plan.entries[0].Text != "answer" {
		t.Fatalf("entries = %+v, want answer suffix", reduction.plan.entries)
	}
	if !reduction.projectedCommitted || reduction.projectedTransient {
		t.Fatalf("commit flags = committed:%t transient:%t", reduction.projectedCommitted, reduction.projectedTransient)
	}
}

func TestReduceProjectedTranscriptEventDefersUserFlushWhileLiveAssistantPending(t *testing.T) {
	state := projectedTranscriptEventState{
		hasRuntimeClient:     true,
		busy:                 true,
		liveAssistantPending: true,
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptRevision:         1,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "follow up"}},
	})

	if reduction.decision != projectedTranscriptDecisionDefer || !reduction.shouldDeferTail {
		t.Fatalf("decision = %+v, want defer", reduction)
	}
	if reduction.skipReason != "deferred_tail" {
		t.Fatalf("skip reason = %q", reduction.skipReason)
	}
}

func TestReduceProjectedTranscriptEventDefersCommittedRowsWhileLiveAssistantPending(t *testing.T) {
	state := projectedTranscriptEventState{
		entries: []tui.TranscriptEntry{{
			Role:      tui.TranscriptRoleAssistant,
			Text:      "streaming",
			Committed: true,
		}},
		baseOffset:           0,
		revision:             1,
		hasRuntimeClient:     true,
		liveAssistantPending: true,
	}
	cases := []struct {
		name    string
		kind    clientui.EventKind
		entries []clientui.ChatEntry
	}{
		{
			name:    "local entry",
			kind:    clientui.EventLocalEntryAdded,
			entries: []clientui.ChatEntry{{Role: "system", Text: "local diagnostic"}},
		},
		{
			name:    "tool completion",
			kind:    clientui.EventToolCallCompleted,
			entries: []clientui.ChatEntry{{Role: "tool_result_ok", Text: "done", ToolCallID: "call-1"}},
		},
		{
			name:    "cache warning",
			kind:    clientui.EventCacheWarning,
			entries: []clientui.ChatEntry{{Role: "system", Text: "cache warning"}},
		},
		{
			name:    "conversation update with entries",
			kind:    clientui.EventConversationUpdated,
			entries: []clientui.ChatEntry{{Role: "user", Text: "authoritative tail"}},
		},
	}
	for idx, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
				Kind:                       tc.kind,
				CommittedTranscriptChanged: true,
				CommittedEntryStart:        1 + idx,
				CommittedEntryStartSet:     true,
				CommittedEntryCount:        2 + idx,
				TranscriptRevision:         int64(2 + idx),
				TranscriptEntries:          tc.entries,
			})

			if reduction.decision != projectedTranscriptDecisionDefer || !reduction.shouldDeferTail {
				t.Fatalf("decision = %+v, want defer", reduction)
			}
			if reduction.skipReason != "deferred_tail" {
				t.Fatalf("skip reason = %q", reduction.skipReason)
			}
		})
	}
}

func TestReduceProjectedTranscriptEventAllowsFinalAssistantCommitToFinalizeLiveStream(t *testing.T) {
	state := projectedTranscriptEventState{
		liveAssistantPending: true,
		liveAssistantText:    "final",
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptRevision:         1,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "final",
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})

	if reduction.decision != projectedTranscriptDecisionApply {
		t.Fatalf("decision = %+v, want final assistant commit to apply", reduction)
	}
}

func TestReduceProjectedTranscriptEventAllowsLiveOnlyUnresolvedToolStartWhileLiveAssistantPending(t *testing.T) {
	state := projectedTranscriptEventState{
		entries: []tui.TranscriptEntry{{
			Role:      tui.TranscriptRoleAssistant,
			Text:      "streaming",
			Committed: true,
		}},
		liveAssistantPending: true,
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptRevision:         2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	})

	if reduction.decision != projectedTranscriptDecisionApply {
		t.Fatalf("decision = %+v, want live-only tool start to apply", reduction)
	}
}

func TestReduceProjectedTranscriptEventAllowsToolCompletionForVisiblePendingToolWhileLiveAssistantPending(t *testing.T) {
	state := projectedTranscriptEventState{
		entries: []tui.TranscriptEntry{{
			Role:       tui.TranscriptRoleToolCall,
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
			Committed:  true,
		}},
		liveAssistantPending: true,
	}
	reduction := reduceProjectedTranscriptEvent(state, clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptRevision:         2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_result_ok",
			Text:       "/tmp",
			ToolCallID: "call-1",
		}},
	})

	if reduction.decision != projectedTranscriptDecisionApply {
		t.Fatalf("decision = %+v, want visible pending tool completion to apply", reduction)
	}
}
