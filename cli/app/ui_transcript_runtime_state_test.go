package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
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

func TestRuntimeTranscriptPageRefreshesActiveAssistantStreamSourceFromStreaming(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	m.windowSizeKnown = true
	m.termWidth = 100
	m.termHeight = 20
	m.activeAssistantStreamSource = "stale partial"
	m.activeAssistantStreamIdentity = assistantStreamIdentityFromMetadata("step-stale", nil)
	m.forwardToView(tui.SetConversationMsg{Ongoing: "stale partial"})

	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		Revision:  2,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "prompt"}},
		Streaming: "hydrated full stream",
	}, clientui.TranscriptRecoveryCauseStreamGap)
	_ = collectCmdMessages(t, cmd)

	if got := m.activeAssistantStreamText(); got != "hydrated full stream" {
		t.Fatalf("active stream text = %q, want hydrated streaming text", got)
	}
	if got := m.activeAssistantStreamStepID(); got != "" {
		t.Fatalf("active stream step ID = %q, want unknown after page streaming hydration", got)
	}
	if got := m.view.OngoingStreamingText(); got != "hydrated full stream" {
		t.Fatalf("view ongoing = %q, want hydrated streaming text", got)
	}
}
