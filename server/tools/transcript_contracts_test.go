package tools

import "testing"

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
