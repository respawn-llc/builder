package app

import (
	"testing"
	"time"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailRecentTailRefreshDoesNotTeleportScrolledAwayWindow(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 18)
	m.layout().syncViewport()
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode = %q, want detail", m.view.Mode())
	}

	older := testTranscriptPage(100, 5, 400)
	older.OlderCursor = 4096
	older.HasMoreAbove = true
	older.NewerCursor = 9001
	older.HasMoreBelow = true
	m.detailTranscript.replace(older)

	beforeOffset := m.detailTranscript.offset
	beforeLen := len(m.detailTranscript.entries)

	tail := testTranscriptPage(380, 5, 400)
	tail.HasMoreAbove = true
	uiRuntimeAdapter{model: m}.applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, tail, clientui.TranscriptRecoveryCauseNone)

	if m.detailTranscript.offset != beforeOffset || len(m.detailTranscript.entries) != beforeLen {
		t.Fatalf("recent-tail refresh teleported scrolled-away detail window: offset %d->%d, len %d->%d",
			beforeOffset, m.detailTranscript.offset, beforeLen, len(m.detailTranscript.entries))
	}
}

func TestDetailRecentTailRefreshUpdatesWindowAtLiveTail(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 18)
	m.layout().syncViewport()
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode = %q, want detail", m.view.Mode())
	}

	atTail := testTranscriptPage(100, 5, 105)
	atTail.OlderCursor = 4096
	atTail.HasMoreAbove = true
	atTail.HasMoreBelow = false
	m.detailTranscript.replace(atTail)

	tail := testTranscriptPage(380, 5, 400)
	tail.HasMoreAbove = true
	uiRuntimeAdapter{model: m}.applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, tail, clientui.TranscriptRecoveryCauseNone)

	if m.detailTranscript.offset != 380 {
		t.Fatalf("recent-tail refresh at live tail must update window, offset = %d, want 380", m.detailTranscript.offset)
	}
}

func TestDetailContentMatchingPageRefreshesEdgeCursors(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 18)
	m.layout().syncViewport()
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode = %q, want detail", m.view.Mode())
	}

	seed := testTranscriptPage(100, 5, 105)
	m.detailTranscript.replace(seed)
	if _, ok := m.detailTranscript.pageBefore(); ok {
		t.Fatal("precondition: cursorless tail must not expose a scroll-up page")
	}

	authoritative := testTranscriptPage(100, 5, 105)
	authoritative.OlderCursor = 4096
	authoritative.HasMoreAbove = true
	uiRuntimeAdapter{model: m}.applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, authoritative, clientui.TranscriptRecoveryCauseNone)

	if !m.detailTranscript.hasMoreAbove {
		t.Fatal("content-matching authoritative page did not refresh hasMoreAbove")
	}
	req, ok := m.detailTranscript.pageBefore()
	if !ok || req.Cursor != 4096 {
		t.Fatalf("scroll-up page unavailable after cursor refresh: ok=%t req=%+v", ok, req)
	}
}

func TestRefreshTranscriptPagePreservesCommittedCountForCursorPages(t *testing.T) {
	reads := &flakySessionViewClient{
		errs: []error{nil, nil},
		pages: []serverapi.SessionTranscriptPageResponse{
			{Transcript: clientui.TranscriptPage{
				SessionID:           "session-1",
				Revision:            7,
				StartEntryCount:     395,
				CommittedEntryCount: 400,
				Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "tail"}},
			}},
			{Transcript: clientui.TranscriptPage{
				SessionID:           "session-1",
				Revision:            7,
				StartEntryCount:     0,
				CommittedEntryCount: 12,
				NewerCursor:         9001,
				HasMoreBelow:        true,
				Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "older"}},
			}},
		},
	}
	concrete := newRuntimeClientReadTest(reads).(*sessionRuntimeClient)

	if _, err := concrete.refreshTranscriptPageSync(clientui.TranscriptPageRequest{}, time.Millisecond); err != nil {
		t.Fatalf("recent-tail refresh error: %v", err)
	}
	if got := concrete.MainView().Session.Transcript.CommittedEntryCount; got != 400 {
		t.Fatalf("cached committed count after recent tail = %d, want 400", got)
	}

	if _, err := concrete.refreshTranscriptPageSync(clientui.TranscriptPageRequest{Cursor: 4096}, time.Millisecond); err != nil {
		t.Fatalf("cursor page refresh error: %v", err)
	}
	if got := concrete.MainView().Session.Transcript.CommittedEntryCount; got != 400 {
		t.Fatalf("cursor page clobbered cached committed count = %d, want 400 preserved", got)
	}
}
