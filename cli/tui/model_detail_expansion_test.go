package tui

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestExpandingDetailEntryPreservesCameraPosition(t *testing.T) {
	m := NewModel()
	m.mode = ModeDetail
	m.viewportLines = 3
	rows := []clientui.TranscriptCommittedRow{
		detailExpansionNotice("one", "one"),
		detailExpansionNotice("two", "two"),
		detailExpansionNotice("preview", "preview\nbelow\nbelow\nbelow\nbelow"),
		detailExpansionNotice("after", "after"),
		detailExpansionNotice("later", "later"),
	}
	m.detailProjection.replaceSnapshot(rows, m.detailContentWidth(), m.theme, nil)
	m.detailPageLoaded = true
	m.detailScroll = 2
	m.setSelectedDetailIndex(2)

	beforeLines := len(m.detailProjection.lines)
	m.toggleSelectedDetailEntry()

	if got := len(m.detailProjection.lines); got <= beforeLines {
		t.Fatalf("detail entry did not expand: before=%d after=%d", beforeLines, got)
	}
	if got := m.detailScroll; got != 2 {
		t.Fatalf("detail scroll after expansion = %d, want preserved camera position", got)
	}
	viewport := m.detailProjectedCameraViewport()
	if len(viewport.Lines) == 0 || viewport.Lines[0].EntryIndex != 2 {
		t.Fatalf("expanded entry did not remain at the camera anchor: %+v", viewport.Lines)
	}
}

func detailExpansionNotice(compact, full string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:        clientui.TranscriptNoticeLegacyUntypedNotice,
			Severity:      clientui.TranscriptNoticeInfo,
			LegacyText:    &full,
			CondensedText: &compact,
		},
	}
}
