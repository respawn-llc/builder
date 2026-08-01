package transcriptrender

import (
	"testing"
	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestPatchHyperlinks(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: dir/file.go\n-old\n+new\n*** End Patch\n"
	rendered := patchformat.Render(patch, "/worktree")
	assertPatchLink(t, RenderCommittedRow(patchRow(rendered), 80, "dark", ModeOngoing).Lines, "./dir/file.go", "file:///worktree/dir/file.go")
	assertPatchLink(t, RenderCommittedRow(patchRow(rendered), 12, "dark", ModeDetailExpanded).Lines, "/worktree/dir/file.go", "file:///worktree/dir/file.go")
	moved := patchformat.Render("*** Begin Patch\n*** Update File: old.go\n*** Move to: new.go\n-old\n+new\n*** End Patch\n", "/worktree")
	assertPatchLink(t, RenderCommittedRow(patchRow(moved), 80, "dark", ModeOngoing).Lines, "./new.go", "file:///worktree/new.go")
	legacy := patchformat.Render(patch, "")
	for _, mode := range []Mode{ModeOngoing, ModeDetailExpanded} {
		if text, _ := patchLink(RenderCommittedRow(patchRow(legacy), 80, "dark", mode).Lines); text != "" { t.Fatalf("legacy path linked as %q", text) }
	}
	raw := clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{ToolName: "patch", Text: patch}}
	if text, _ := patchLink(RenderCommittedRow(raw, 80, "dark", ModeOngoing).Lines); text != "" { t.Fatalf("raw patch path linked as %q", text) }
}
func patchRow(rendered patchformat.RenderedPatch) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{ToolName: "patch", Presentation: &transcript.ToolCallMeta{PatchRender: &rendered}}}
}
func patchLink(lines []Line) (text, url string) {
	for _, line := range lines {
		for _, span := range line.Spans {
			if span.Hyperlink != nil { text += span.Text; url = span.Hyperlink.URL }
		}
	}
	return text, url
}
func assertPatchLink(t *testing.T, lines []Line, text, url string) {
	t.Helper()
	got, gotURL := patchLink(lines)
	if got != text || gotURL != url { t.Fatalf("patch link = (%q, %q), want (%q, %q)", got, gotURL, text, url) }
}
func TestTruncateLineLeavesEllipsisOutsidePatchHyperlink(t *testing.T) {
	line := Line{Spans: []Span{{Text: "/worktree/long-file.go", Style: SemanticStyle(StyleRoleToolPatch), Hyperlink: &Hyperlink{URL: "file:///worktree/long-file.go"}}}}
	truncated := TruncateLine(line, 10, false)
	if truncated.Spans[0].Hyperlink == nil || truncated.Spans[len(truncated.Spans)-1].Text != "…" || truncated.Spans[len(truncated.Spans)-1].Hyperlink != nil { t.Fatalf("truncated path hyperlink boundary = %+v", truncated.Spans) }
}
