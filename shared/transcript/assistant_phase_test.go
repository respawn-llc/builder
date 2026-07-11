package transcript

import "testing"

func TestClassifyAssistantPhaseMakesLegacyAbsenceExplicit(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want AssistantPhase
	}{
		{name: "commentary", raw: "commentary", want: AssistantPhaseCommentary},
		{name: "final", raw: "final_answer", want: AssistantPhaseFinal},
		{name: "legacy absence", raw: "", want: AssistantPhaseLegacyFinal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAssistantPhase(tc.raw); got != tc.want {
				t.Fatalf("ClassifyAssistantPhase(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseExplicitAssistantPhaseOwnsAcceptedAliases(t *testing.T) {
	tests := []struct {
		raw    string
		want   AssistantPhase
		wantOK bool
	}{
		{raw: "commentary", want: AssistantPhaseCommentary, wantOK: true},
		{raw: "COMMENTARY", want: AssistantPhaseCommentary, wantOK: true},
		{raw: "final_answer", want: AssistantPhaseFinal, wantOK: true},
		{raw: "finalanswer", want: AssistantPhaseFinal, wantOK: true},
		{raw: "final", want: AssistantPhaseFinal, wantOK: true},
		{raw: "unknown"},
		{raw: ""},
	}
	for _, tc := range tests {
		got, ok := ParseExplicitAssistantPhase(tc.raw)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("ParseExplicitAssistantPhase(%q) = %q/%t, want %q/%t", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestClassifyAssistantPhaseRejectsUnsupportedValue(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unsupported assistant phase did not panic")
		}
	}()
	ClassifyAssistantPhase("analysis")
}
