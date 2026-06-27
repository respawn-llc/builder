package app

import (
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/transcript"
	"strings"
	"testing"
)

func TestProjectedCommittedGoalFeedbackAppendsImmediately(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.forwardToView(tui.SetViewportSizeMsg{Width: 100, Lines: 20})

	cmd, mutated, needsHydration := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:          string(transcript.EntryRoleGoalFeedback),
			Text:          "Goal set developer prompt detail",
			CondensedText: `Goal set: "ship feature"`,
			Visibility:    clientui.EntryVisibilityAll,
		}},
	})

	if cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected direct goal feedback append, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}
	if got, want := len(m.transcriptEntries), 1; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	entry := m.transcriptEntries[0]
	if entry.Role != tui.TranscriptRoleGoalFeedback || entry.CondensedText != `Goal set: "ship feature"` || entry.Transient || !entry.Committed {
		t.Fatalf("goal feedback entry = %+v", entry)
	}
	if view := stripANSIAndTrimRight(m.view.OngoingSnapshot()); !strings.Contains(view, `Goal set: "ship feature"`) {
		t.Fatalf("expected ongoing view to update immediately, got %q", view)
	}
}

func TestProjectedAssistantMessageUsesCommittedEntryStartWhenPersistedToolCallsShareCommittedCount(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "prompt"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	cmd, mutated, needsHydration := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "working",
			Phase: string(llm.MessagePhaseCommentary),
		}},
	})

	if cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected direct live append using explicit committed start, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}
	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if m.transcriptEntries[1].Transient || !m.transcriptEntries[1].Committed {
		t.Fatalf("expected committed assistant entry to apply as committed transcript state, got %+v", m.transcriptEntries[1])
	}
}

func TestProjectedToolCallStartedUsesCommittedEntryStartWithinSharedCommittedCountBatch(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "assistant", Text: "working", Phase: string(llm.MessagePhaseCommentary)},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	cmd, mutated, needsHydration := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	})

	if cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected direct tool-call append using explicit committed start, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}
	if got, want := len(m.transcriptEntries), 3; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if m.transcriptEntries[2].Transient || !m.transcriptEntries[2].Committed {
		t.Fatalf("expected committed tool call entry to apply as committed transcript state, got %+v", m.transcriptEntries[2])
	}
}

func TestProjectedAssistantMessageUpdatesDetailViewImmediatelyWhenCommitted(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.layout().syncViewport()

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "committed after",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect assistant_message committed delta to trigger transcript hydration, got %+v", msgs)
		}
	}

	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if m.transcriptEntries[1].Transient || !m.transcriptEntries[1].Committed {
		t.Fatalf("expected committed assistant entry to apply as committed transcript state, got %+v", m.transcriptEntries[1])
	}
	if got := m.detailTranscript.totalEntries; got != 2 {
		t.Fatalf("detail transcript total entries = %d, want 2", got)
	}
	if got, want := len(m.detailTranscript.entries), 2; got != want {
		t.Fatalf("detail transcript entry count = %d, want %d", got, want)
	}
	if got := m.detailTranscript.entries[1].Text; got != "committed after" {
		t.Fatalf("detail transcript tail = %q, want committed after", got)
	}
	view := stripANSIAndTrimRight(m.View())
	if !strings.Contains(view, "seed") && !strings.Contains(view, "committed after") {
		t.Fatalf("expected detail view to reflect committed assistant delta, got %q", view)
	}
}

func TestProjectedReviewerCompletedUpdatesDetailViewImmediatelyWhenCommitted(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.layout().syncViewport()

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "reviewer_status",
			Text: "Supervisor ran and applied 2 suggestions.",
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect reviewer committed delta to trigger transcript hydration, got %+v", msgs)
		}
	}

	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if m.transcriptEntries[1].Transient || !m.transcriptEntries[1].Committed {
		t.Fatalf("expected committed reviewer status to apply as committed transcript state, got %+v", m.transcriptEntries[1])
	}
	if got := m.detailTranscript.totalEntries; got != 2 {
		t.Fatalf("detail transcript total entries = %d, want 2", got)
	}
	if got, want := len(m.detailTranscript.entries), 2; got != want {
		t.Fatalf("detail transcript entry count = %d, want %d", got, want)
	}
	if got := m.detailTranscript.entries[1].Text; got != "Supervisor ran and applied 2 suggestions." {
		t.Fatalf("detail transcript tail = %q, want reviewer status", got)
	}
	view := stripANSIAndTrimRight(m.View())
	if !containsInOrder(view, "seed", "Supervisor ran and applied 2 suggestions.") {
		t.Fatalf("expected detail view to reflect committed reviewer delta, got %q", view)
	}
}

func TestCommittedTranscriptEntriesForAppSkipsPreCommitRowsAndKeepsLaterCommittedEntries(t *testing.T) {
	entries := []tui.TranscriptEntry{
		{Role: "assistant", Text: "seed"},
		{Role: "compaction_notice", Text: "context compacted for the 1st time", Transient: true},
		{Role: "reviewer_status", Text: "Supervisor ran: 1 suggestion, applied.", Transient: true, Committed: true},
	}

	committed := committedTranscriptEntriesForApp(entries)
	if got, want := len(committed), 2; got != want {
		t.Fatalf("committed entry count = %d, want %d (%+v)", got, want, committed)
	}
	if got := committed[0].Role; got != "assistant" {
		t.Fatalf("committed[0].Role = %q, want assistant", got)
	}
	if got := committed[1].Role; got != "reviewer_status" {
		t.Fatalf("committed[1].Role = %q, want reviewer_status", got)
	}
	if committed[1].Transient {
		t.Fatalf("expected committed reviewer status normalized to non-transient, got %+v", committed[1])
	}
}

func TestHandleProjectedRuntimeEventSkipsReplayedToolCallStartWithSameToolCallID(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "seed", Phase: llm.MessagePhaseCommentary},
		{Role: "tool_call", Text: "pwd", ToolCallID: "call-1", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
	}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = len(m.transcriptEntries)
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected replayed tool call start skipped, got %+v", m.transcriptEntries)
	}
	_ = cmd
}

func TestHandleProjectedRuntimeEventCommittedToolCallStartReplacesMatchingTransientToolRow(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "seed", Phase: llm.MessagePhaseFinal, Committed: true},
		{Role: "tool_call", Text: "pwd", ToolCallID: "call-1", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}, Transient: true},
	}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 2
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         11,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	}).
		cmd

	_ = cmd
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count = %d, want 2", got)
	}
	if got := m.transcriptEntries[1]; got.Transient || !got.Committed {
		t.Fatalf("expected committed tool row after replacement, got %+v", got)
	}
}

func TestHandleProjectedRuntimeEventAppendsDistinctToolCallStartByToolCallID(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "tool_call", Text: "pwd", ToolCallID: "call-1", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-2",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected distinct tool call id to append, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[1].ToolCallID; got != "call-2" {
		t.Fatalf("second tool call id = %q, want call-2", got)
	}
}

func TestHandleProjectedRuntimeEventDoesNotSuppressReviewerStatusEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "seed", Phase: llm.MessagePhaseCommentary}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "reviewer_status",
			Text: "Supervisor ran and applied 2 suggestions.",
		}},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected reviewer status appended immediately, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[1].Role; got != "reviewer_status" {
		t.Fatalf("second transcript role = %q, want reviewer_status", got)
	}
}

func TestHandleProjectedRuntimeEventSkipsHydratedReviewerStatusEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "seed", Phase: llm.MessagePhaseCommentary},
		{Role: "reviewer_status", Text: "Supervisor ran and applied 2 suggestions."},
	}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 2
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "reviewer_status",
			Text: "Supervisor ran and applied 2 suggestions.",
		}},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected hydrated reviewer status to be skipped, got %+v", m.transcriptEntries)
	}
}

func TestHandleProjectedRuntimeEventDoesNotAppendPrePersistCompactionStatusEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "seed", Phase: llm.MessagePhaseCommentary}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventCompactionCompleted,
		StepID: "step-1",
		Compaction: &runtime.CompactionStatus{
			Mode:  "auto",
			Count: 1,
		},
	})).
		cmd

	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected pre-persist compaction status to avoid transcript mutation, got %+v", m.transcriptEntries)
	}
}

func TestProjectedCompactionStatusClearsCompactingWithoutTranscriptNotice(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	msgs := collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventCompactionCompleted,
		StepID: "step-1",
		Compaction: &runtime.CompactionStatus{
			Mode:  "auto",
			Count: 1,
		},
	})).
		cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect compaction status to trigger transcript hydration, got %+v", msgs)
		}
	}
	if got, want := len(m.transcriptEntries), 1; got != want {
		t.Fatalf("transcript entry count after compaction status = %d, want %d", got, want)
	}
	loaded := m.view.LoadedTranscriptEntries()
	if got, want := len(loaded), 1; got != want {
		t.Fatalf("loaded transcript entry count = %d, want %d (%+v)", got, want, loaded)
	}
}

func TestProjectedCompactionStatusDoesNotDuplicateCommittedSummary(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "compaction_summary", Text: "summary", CondensedText: "context compacted for the 1st time", CompactLabel: "context compacted for the 1st time"},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventCompactionCompleted,
		StepID: "step-1",
		Compaction: &runtime.CompactionStatus{
			Mode:  "auto",
			Count: 1,
		},
	})).
		cmd

	loaded := m.view.LoadedTranscriptEntries()
	if got, want := len(loaded), 2; got != want {
		t.Fatalf("loaded transcript entry count = %d, want %d (%+v)", got, want, loaded)
	}
	notices := 0
	for _, entry := range loaded {
		if entry.Role == "compaction_summary" && entry.CompactLabel == "context compacted for the 1st time" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("expected exactly one loaded compaction summary, got %d (%+v)", notices, loaded)
	}
}

func TestProjectedCompactionStatusDoesNotAppendOngoingNoticeInDetailMode(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.layout().syncViewport()

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventCompactionCompleted,
		StepID: "step-1",
		Compaction: &runtime.CompactionStatus{
			Mode:  "auto",
			Count: 1,
		},
	})).
		cmd

	loaded := m.view.LoadedTranscriptEntries()
	if got, want := len(loaded), 1; got != want {
		t.Fatalf("loaded transcript entry count = %d, want %d (%+v)", got, want, loaded)
	}
	if strings.Contains(stripANSIAndTrimRight(m.View()), "context compacted for the 1st time") {
		t.Fatalf("did not expect compaction status notice in detail view, got %q", stripANSIAndTrimRight(m.View()))
	}
}

func TestProjectedCompactionStatusUsesPersistedLocalEntryAsTranscriptSource(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	msgs := collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventCompactionCompleted,
		StepID: "step-1",
		Compaction: &runtime.CompactionStatus{
			Mode:  "auto",
			Count: 1,
		},
	})).
		cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect pre-persist compaction status to trigger transcript hydration, got %+v", msgs)
		}
	}
	if got, want := len(m.transcriptEntries), 1; got != want {
		t.Fatalf("transcript entry count after compaction status = %d, want %d", got, want)
	}

	msgs = collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         11,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "compaction_notice",
			Text: "context compacted for the 1st time",
		}},
	}).
		cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect persisted compaction notice to trigger transcript hydration, got %+v", msgs)
		}
	}
	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count after persisted compaction notice = %d, want %d", got, want)
	}
	if m.transcriptEntries[1].Transient || !m.transcriptEntries[1].Committed {
		t.Fatalf("expected persisted compaction notice to apply as committed transcript state, got %+v", m.transcriptEntries[1])
	}
	loaded := m.view.LoadedTranscriptEntries()
	if got, want := len(loaded), 2; got != want {
		t.Fatalf("loaded transcript entry count = %d, want %d (%+v)", got, want, loaded)
	}
	if got := loaded[1].Role; got != "compaction_notice" {
		t.Fatalf("loaded compaction role = %q, want compaction_notice", got)
	}
}

func TestProjectedCompactionReplacementEntriesAndNoticeAppendWithoutHydration(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "before compaction"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	for _, evt := range []clientui.Event{
		{
			Kind:                       clientui.EventLocalEntryAdded,
			CommittedTranscriptChanged: true,
			StepID:                     "step-1",
			TranscriptRevision:         11,
			CommittedEntryStart:        1,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "developer_context",
				Text:       "environment info",
				Visibility: clientui.EntryVisibilityVerbose,
			}},
		},
		{
			Kind:                       clientui.EventLocalEntryAdded,
			CommittedTranscriptChanged: true,
			StepID:                     "step-1",
			TranscriptRevision:         11,
			CommittedEntryStart:        2,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role: "compaction_summary",
				Text: "condensed summary",
			}},
		},
		{
			Kind:                       clientui.EventLocalEntryAdded,
			CommittedTranscriptChanged: true,
			StepID:                     "step-1",
			TranscriptRevision:         11,
			CommittedEntryStart:        3,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role: "compaction_notice",
				Text: "context compacted for the 1st time",
			}},
		},
	} {
		msgs := collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(evt).cmd)
		for _, msg := range msgs {
			if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
				t.Fatalf("did not expect projected compaction transcript entries to trigger hydration, got %+v", msgs)
			}
		}
	}

	if got, want := len(m.transcriptEntries), 4; got != want {
		t.Fatalf("transcript entry count after projected compaction events = %d, want %d (%+v)", got, want, m.transcriptEntries)
	}
	if got := m.transcriptEntries[1].Role; got != "developer_context" {
		t.Fatalf("second transcript role = %q, want developer_context", got)
	}
	if got := m.transcriptEntries[2].Role; got != "compaction_summary" {
		t.Fatalf("third transcript role = %q, want compaction_summary", got)
	}
	if got := m.transcriptEntries[3].Role; got != "compaction_notice" {
		t.Fatalf("fourth transcript role = %q, want compaction_notice", got)
	}
}

func TestHandleProjectedRuntimeEventAppendsLocalEntryImmediately(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:          "reviewer_suggestions",
			Text:          "Supervisor suggested:\n1. Add verification notes.",
			CondensedText: "Supervisor made 1 suggestion.",
		}},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected local entry appended immediately, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[0].Role; got != "reviewer_suggestions" {
		t.Fatalf("local entry role = %q, want reviewer_suggestions", got)
	}
	loaded := m.view.LoadedTranscriptEntries()
	if len(loaded) != 1 || loaded[0].Role != "reviewer_suggestions" {
		t.Fatalf("expected local entry visible in view, got %+v", loaded)
	}
}

func TestLocalEntryAddedRemainsVisibleAfterHydrationSync(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed", Phase: string(llm.MessagePhaseFinal)}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:          "reviewer_suggestions",
			Text:          "Supervisor suggested:\n1. Add verification notes.",
			CondensedText: "Supervisor made 1 suggestion.",
		}},
	}).
		cmd

	hydrated := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed", Phase: string(llm.MessagePhaseFinal)},
			{Role: "reviewer_suggestions", Text: "Supervisor suggested:\n1. Add verification notes.", CondensedText: "Supervisor made 1 suggestion."},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, hydrated, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected hydrated transcript without duplication, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[1].Role; got != "reviewer_suggestions" {
		t.Fatalf("local entry role after hydration = %q, want reviewer_suggestions", got)
	}
	loaded := m.view.LoadedTranscriptEntries()
	if len(loaded) != 2 {
		t.Fatalf("expected hydrated loaded transcript length 2, got %+v", loaded)
	}
	count := 0
	for _, entry := range loaded {
		if entry.Role == "reviewer_suggestions" && entry.Text == "Supervisor suggested:\n1. Add verification notes." {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected reviewer_suggestions exactly once after hydration, got %+v", loaded)
	}
}

func TestLocalFirstEntryHydrationAcknowledgesNoticeIDWithoutDroppingDistinctEntries(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	if cmd := m.appendLocalEntryWithNoticeID("system", "same feedback", "notice-1"); cmd == nil {
		t.Fatal("expected local entry persistence command")
	}
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:               clientui.EventLocalEntryAdded,
		TranscriptRevision: 2,
		TranscriptEntries:  []clientui.ChatEntry{{Role: "system", Text: "same feedback", NoticeID: "notice-1"}},
	}).
		cmd

	hydrated := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  2,
		Entries: []clientui.ChatEntry{
			{Role: "system", Text: "same feedback", NoticeID: "notice-1"},
			{Role: "system", Text: "same feedback", NoticeID: "notice-2"},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, hydrated, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	loaded := m.view.LoadedTranscriptEntries()
	if len(loaded) != 2 {
		t.Fatalf("expected two distinct NoticeID entries after hydration, got %+v", loaded)
	}
	if loaded[0].NoticeID != "notice-1" || loaded[1].NoticeID != "notice-2" {
		t.Fatalf("unexpected hydrated NoticeIDs: %+v", loaded)
	}
}

func TestHandleProjectedRuntimeEventAppendsCleanupAndBackgroundEntriesImmediately(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventInFlightClearFailed,
		StepID: "step-1",
		Error:  "mark in-flight false",
	})).
		cmd
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind: runtime.EventBackgroundUpdated,
		Background: &runtime.BackgroundShellEvent{
			Type:        "completed",
			ID:          "1000",
			State:       "completed",
			NoticeText:  "Background shell 1000 completed.\nNo output",
			CompactText: "Background shell 1000 completed",
		},
	})).
		cmd

	if len(m.transcriptEntries) != 2 {
		t.Fatalf("expected two immediate transcript entries, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[0].Role; got != "error" {
		t.Fatalf("entry[0].Role = %q, want error", got)
	}
	if got := m.transcriptEntries[1].Role; got != "system" {
		t.Fatalf("entry[1].Role = %q, want system", got)
	}
	if got := m.transcriptEntries[1].CondensedText; got != "Background shell 1000 completed" {
		t.Fatalf("background ongoing text = %q", got)
	}
}

func TestRuntimeSessionViewUsesLocalFallbackWhenRuntimeClientMissing(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUISessionName("incident triage"),
		WithUISessionID("session-123"),
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
	)
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "hello"}}
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "streaming"})

	view := m.runtimeSessionView()
	if view.SessionName != "incident triage" {
		t.Fatalf("session name = %q, want incident triage", view.SessionName)
	}
	if view.SessionID != "session-123" {
		t.Fatalf("session id = %q, want session-123", view.SessionID)
	}
	if view.ConversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", view.ConversationFreshness)
	}
	if len(view.Chat.Entries) != 1 || view.Chat.Entries[0].Text != "hello" {
		t.Fatalf("unexpected fallback chat entries: %+v", view.Chat.Entries)
	}
	if view.Chat.Streaming != "streaming" {
		t.Fatalf("ongoing = %q, want streaming", view.Chat.Streaming)
	}
}
