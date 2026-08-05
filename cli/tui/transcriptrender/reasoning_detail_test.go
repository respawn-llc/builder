package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestReasoningTraceRendersProjectedPlaintextFaintInDetailOnly(t *testing.T) {
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowReasoningTrace,
		ReasoningTrace: &clientui.TranscriptReasoningTraceRow{
			StepID:      stepID,
			CompactText: "Preparing to investigate issue",
			Text:        "Preparing to investigate issue\nDetails",
		},
	}
	if rendered := RenderCommittedRow(row, 80, "dark", ModeOngoing); len(rendered.Lines) != 0 {
		t.Fatalf("reasoning trace rendered in ongoing mode: %+v", rendered)
	}
	rendered := RenderCommittedRow(row, 80, "dark", ModeDetailExpanded)
	if len(rendered.Lines) == 0 {
		t.Fatal("reasoning trace omitted in detail mode")
	}
	for _, span := range rendered.Lines[0].Spans {
		if strings.TrimSpace(span.Text) == "" {
			continue
		}
		if !span.Style.Has(SpanAttributeFaint) {
			t.Fatalf("reasoning text span is not faint: %+v", span)
		}
		return
	}
	t.Fatalf("reasoning trace has no content span: %+v", rendered.Lines[0])
}
