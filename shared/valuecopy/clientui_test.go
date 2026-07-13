package valuecopy

import (
	"testing"

	"core/shared/clientui"
	patchformat "core/shared/transcript/patchformat"
)

func TestToolCallMetaDeepClonesMutableFields(t *testing.T) {
	exitCode := 7
	source := &clientui.ToolCallMeta{
		ToolName:      "patch",
		Suggestions:   []string{"one"},
		RenderHint:    &clientui.ToolRenderHint{Path: "file.go"},
		ShellExitCode: &exitCode,
		PatchRender: &patchformat.RenderedPatch{
			Files: []patchformat.RenderedFile{{
				RelPath: "file.go",
				Diff:    []string{"old"},
			}},
			SummaryLines: []patchformat.RenderedLine{{Text: "summary"}},
			DetailLines:  []patchformat.RenderedLine{{Text: "detail"}},
		},
	}

	cloned := ToolCallMeta(source)
	source.Suggestions[0] = "changed"
	source.RenderHint.Path = "changed.go"
	*source.ShellExitCode = 9
	source.PatchRender.Files[0].Diff[0] = "changed"
	source.PatchRender.SummaryLines[0].Text = "changed"
	source.PatchRender.DetailLines[0].Text = "changed"

	if cloned == source {
		t.Fatal("clone returned source pointer")
	}
	if cloned.Suggestions[0] != "one" {
		t.Fatalf("suggestions clone = %#v, want source-isolated value", cloned.Suggestions)
	}
	if cloned.RenderHint.Path != "file.go" {
		t.Fatalf("render hint clone = %#v, want source-isolated value", cloned.RenderHint)
	}
	if cloned.ShellExitCode == nil || *cloned.ShellExitCode != 7 {
		t.Fatalf("shell exit code clone = %v, want source-isolated 7", cloned.ShellExitCode)
	}
	if cloned.PatchRender.Files[0].Diff[0] != "old" {
		t.Fatalf("patch diff clone = %#v, want source-isolated value", cloned.PatchRender.Files[0].Diff)
	}
	if cloned.PatchRender.SummaryLines[0].Text != "summary" {
		t.Fatalf("patch summary clone = %#v, want source-isolated value", cloned.PatchRender.SummaryLines)
	}
	if cloned.PatchRender.DetailLines[0].Text != "detail" {
		t.Fatalf("patch detail clone = %#v, want source-isolated value", cloned.PatchRender.DetailLines)
	}
}
