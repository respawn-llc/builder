package app

import (
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/llm"
	"core/shared/clientui"
)

func TestNewAssistantTurnFlushesSupersededStuckCommentary(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}}
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: m.transcriptEntries})

	_, c1 := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: "Continuing now.",
	}})
	_ = collectCmdMessages(t, c1)

	_, c2 := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-stale",
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        2,
		TranscriptRevision:         2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "Continuing now.",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}})
	_ = collectCmdMessages(t, c2)

	if got := len(m.deferredCommittedTail); got != 1 {
		t.Fatalf("mismatched-step commentary commit should defer until the next turn boundary, got %d", got)
	}

	_, c3 := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-2",
		AssistantDelta: "Now running tools.",
	}})
	_ = collectCmdMessages(t, c3)

	if got := len(m.deferredCommittedTail); got != 0 {
		t.Fatalf("turn-1 commentary still stuck in deferred tail after the next turn began: %d", got)
	}
	if got := m.view.OngoingStreamingText(); got != "Now running tools." {
		t.Fatalf("live area carried prior-turn commentary into the new turn: %q", got)
	}
	foundAssistant := false
	for _, entry := range committedTranscriptEntriesForApp(m.transcriptEntries) {
		if entry.Role == tui.TranscriptRoleAssistant && strings.TrimSpace(entry.Text) == "Continuing now." {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Fatal("superseded commentary was not committed into the working set")
	}
}

func TestNewToolTurnFlushesSupersededStuckCommentary(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}}
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: m.transcriptEntries})

	_, c1 := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: "Continuing now.",
	}})
	_ = collectCmdMessages(t, c1)

	_, c2 := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-stale",
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		CommittedEntryCount:        2,
		TranscriptRevision:         2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "Continuing now.",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}})
	_ = collectCmdMessages(t, c2)

	if got := len(m.deferredCommittedTail); got != 1 {
		t.Fatalf("mismatched-step commentary commit should defer until the next turn boundary, got %d", got)
	}

	_, c3 := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:                clientui.EventToolCallStarted,
		StepID:              "step-2",
		TranscriptRevision:  3,
		CommittedEntryCount: 3,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	}})
	_ = collectCmdMessages(t, c3)

	if got := len(m.deferredCommittedTail); got != 0 {
		t.Fatalf("turn-1 commentary still stuck after a tools-only next turn began: %d", got)
	}
	foundAssistant := false
	for _, entry := range committedTranscriptEntriesForApp(m.transcriptEntries) {
		if entry.Role == tui.TranscriptRoleAssistant && strings.TrimSpace(entry.Text) == "Continuing now." {
			foundAssistant = true
			break
		}
	}
	if !foundAssistant {
		t.Fatal("superseded commentary was not committed into the working set by the tool turn")
	}
}

func TestNewAssistantTurnFlushesStepLessDeferredFinalizerByText(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}}
	m.transcriptRevision = 1
	m.transcriptTotalEntries = 1
	m.deferredCommittedTail = []deferredProjectedTranscriptTail{{
		rangeStart: 1,
		rangeEnd:   2,
		revision:   2,
		entries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "Continuing now.",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}}
	m.activeAssistantStreamSource = "Continuing now."
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: m.transcriptEntries})

	_, cmd := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-2",
		AssistantDelta: "Next turn.",
	}})
	_ = collectCmdMessages(t, cmd)

	if got := len(m.deferredCommittedTail); got != 0 {
		t.Fatalf("step-less finalizer still stuck in deferred tail after the next turn began: %d", got)
	}
	foundAssistant := false
	for _, entry := range committedTranscriptEntriesForApp(m.transcriptEntries) {
		if entry.Role == tui.TranscriptRoleAssistant && strings.TrimSpace(entry.Text) == "Continuing now." {
			foundAssistant = true
			break
		}
	}
	if !foundAssistant {
		t.Fatal("step-less deferred finalizer was not committed into the working set")
	}
	if got := m.view.OngoingStreamingText(); got != "Next turn." {
		t.Fatalf("live area carried step-less finalizer into the new turn: %q", got)
	}
}
