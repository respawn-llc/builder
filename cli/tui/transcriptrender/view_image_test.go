package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestViewImageUsesTypedPathInOngoingAndExpandedDetail(t *testing.T) {
	const imagePath = "/tmp/kent-clipboard.png"
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{
			ToolName: string(toolspec.ToolViewImage),
			Text:     "tool result",
			Presentation: &transcript.ToolCallMeta{
				ToolName:    string(toolspec.ToolViewImage),
				Command:     "path: unexpected-raw-input.png",
				CompactText: "path: unexpected-raw-input.png",
				RenderHint: &transcript.ToolRenderHint{
					Kind: transcript.ToolRenderKindPlain,
					Path: imagePath,
				},
			},
		},
	}
	want := viewImageDisplayPrefix + imagePath

	for _, test := range []struct {
		name string
		mode Mode
	}{
		{name: "ongoing", mode: ModeOngoing},
		{name: "collapsed detail", mode: ModeDetailCollapsed},
		{name: "expanded detail", mode: ModeDetailExpanded},
	} {
		t.Run(test.name, func(t *testing.T) {
			rendered := RenderCommittedRow(row, 120, "dark", test.mode)
			got := strings.Join(PlainLines(rendered.Lines), "\n")
			if !strings.Contains(got, want) {
				t.Fatalf("view_image preview = %q, want typed path presentation %q", got, want)
			}
			if strings.Contains(got, "path: unexpected-raw-input.png") {
				t.Fatalf("view_image preview used raw input text: %q", got)
			}
		})
	}
}
