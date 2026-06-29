package app

import (
	"testing"

	"core/cli/tui"
)

func TestInvalidateTransientTranscriptStateUsesDetailBounds(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	detailPage := testTranscriptPage(100, 2, 500)
	m.detailTranscript.setKnownBounds(100, 500)
	m.detailTranscript.replace(detailPage)
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.detailTranscript.offset,
		TotalEntries: m.detailTranscript.totalEntries,
		Entries:      m.detailTranscript.entries,
		Ongoing:      "draft",
	})
	m.transcriptBaseOffset = 490
	m.transcriptTotalEntries = 500
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "draft", Transient: true}}

	m.invalidateTransientTranscriptState()

	if got := m.view.TranscriptBaseOffset(); got != 100 {
		t.Fatalf("view base offset = %d, want detail window offset 100", got)
	}
	if got := m.view.TranscriptTotalEntries(); got != 500 {
		t.Fatalf("view total entries = %d, want detail window total 500", got)
	}
}
