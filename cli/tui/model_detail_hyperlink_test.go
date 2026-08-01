package tui

import (
	"strings"
	"testing"

	"core/internal/testharness/pty"
	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestDetailPatchHyperlinkClosesBeforeUnselectedAndSelectedPadding(t *testing.T) {
	rendered := patchformat.Render("*** Begin Patch\n*** Update File: dir/file.go\n-old\n+new\n*** End Patch\n", "/worktree")
	row := clientui.TranscriptCommittedRow{Visibility: transcript.EntryVisibilityDetail, Integrity: transcript.RowIntegrityValid, Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{ToolName: "patch", Presentation: &transcript.ToolCallMeta{PatchRender: &rendered}}}
	for _, selected := range []bool{false, true} {
		model := NewModel()
		model.expanded = map[int]struct{}{0: {}}
		model.detailProjection.replaceSnapshot([]clientui.TranscriptCommittedRow{row}, model.detailContentWidth(), model.theme, model.expanded)
		if selected {
			model.setSelectedDetailIndex(0)
		}
		lines := model.detailProjectedLines()
		if selected {
			lines = model.detailVisibleProjectedLines()
		}
		trace := pty.TraceTerminalHyperlinks(t, strings.Join(renderDetailProjectedLines(lines, model.theme), "\n"))
		if got := trace.LinkedText("file:///worktree/dir/file.go"); got != "/worktree/dir/file.go" {
			t.Fatalf("linked detail path = %q, want represented path", got)
		}
	}
}
