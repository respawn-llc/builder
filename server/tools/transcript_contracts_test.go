package tools

import (
	"testing"

	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestFormatAskQuestionToolOutputRejectsPresentZeroSelection(t *testing.T) {
	if formatted, ok := formatAskQuestionToolOutput([]byte(`{"selected_option_number":0,"freeform_answer":"typed"}`)); ok || formatted != "" {
		t.Fatalf("present zero selection formatted as %q, %t; want malformed payload rejection", formatted, ok)
	}
}

func TestFormatAskQuestionToolOutputAcceptsLegacyOmittedSelection(t *testing.T) {
	formatted, ok := formatAskQuestionToolOutput([]byte(`{"freeform_answer":"typed"}`))
	if !ok || formatted == "" {
		t.Fatalf("legacy omitted selection formatted as %q, %t; want freeform answer", formatted, ok)
	}
}

func TestViewImageCallMetadataCarriesTypedPath(t *testing.T) {
	const imagePath = "/tmp/kent-clipboard.png"

	meta := BuildCallTranscriptMeta(
		string(toolspec.ToolViewImage),
		ToolCallContext{},
		[]byte(`{"path":"`+imagePath+`"}`),
	)

	if meta.RenderHint == nil {
		t.Fatal("view_image metadata is missing render hint")
	}
	if meta.RenderHint.Kind != transcript.ToolRenderKindPlain {
		t.Fatalf("view_image render hint kind = %q, want %q", meta.RenderHint.Kind, transcript.ToolRenderKindPlain)
	}
	if meta.RenderHint.Path != imagePath {
		t.Fatalf("view_image render hint path = %q, want %q", meta.RenderHint.Path, imagePath)
	}
	if meta.Command != "" {
		t.Fatalf("view_image metadata carries command text %q; path belongs only in render hint", meta.Command)
	}
	if meta.CompactText != "" {
		t.Fatalf("view_image metadata carries compact text %q; path belongs only in render hint", meta.CompactText)
	}
}

func TestWebSearchCallMetadataKeepsQueryAsInput(t *testing.T) {
	const query = "Go error handling"

	meta := BuildCallTranscriptMeta(
		string(toolspec.ToolWebSearch),
		ToolCallContext{},
		[]byte(`{"query":"`+query+`"}`),
	)

	if meta.Command != query {
		t.Fatalf("web_search command = %q, want query %q", meta.Command, query)
	}
	if meta.CompactText != query {
		t.Fatalf("web_search compact text = %q, want query %q", meta.CompactText, query)
	}
}
