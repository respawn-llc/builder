package runtime

import "testing"

func TestProjectReasoningTraceRemovesOnlyBoundaryMarkers(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		compact string
		text    string
	}{
		{"both boundaries", "**Plan**", "Plan", "Plan"},
		{"leading only", "**Plan", "Plan", "Plan"},
		{"trailing only", "Plan**", "Plan", "Plan"},
		{"interior markers", "2 ** 3", "2 ** 3", "2 ** 3"},
		{"unmatched interior", "Plan ** details", "Plan ** details", "Plan ** details"},
		{"first nonblank unix", "\n\nPlan\nDetails", "Plan", "\n\nPlan\nDetails"},
		{"first nonblank windows", "\r\n\r\nPlan\r\nDetails", "Plan", "\r\n\r\nPlan\r\nDetails"},
		{"whitespace preserved", "  **Plan**  \n\n", "  **Plan**  ", "  **Plan**  \n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ProjectReasoningTrace(test.source)
			if got.CompactText != test.compact || got.Text != test.text {
				t.Fatalf("projection = %+v, want compact=%q text=%q", got, test.compact, test.text)
			}
		})
	}
}
