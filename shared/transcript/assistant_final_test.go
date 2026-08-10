package transcript

import "testing"

func TestIsBlankAssistantFinalRequiresExplicitUntypedFinalContent(t *testing.T) {
	t.Parallel()
	blank := ""
	whitespace := " \n\t"
	tests := []struct {
		name string
		in   AssistantFinalCandidate
		want bool
	}{
		{
			name: "empty assistant final",
			in: AssistantFinalCandidate{
				IsAssistant: true,
				IsFinal:     true,
				Content:     &blank,
			},
			want: true,
		},
		{
			name: "whitespace assistant final",
			in: AssistantFinalCandidate{
				IsAssistant: true,
				IsFinal:     true,
				Content:     &whitespace,
			},
			want: true,
		},
		{
			name: "typed assistant final",
			in: AssistantFinalCandidate{
				IsAssistant:    true,
				IsFinal:        true,
				HasMessageType: true,
				Content:        &blank,
			},
			want: false,
		},
		{
			name: "omitted content",
			in: AssistantFinalCandidate{
				IsAssistant: true,
				IsFinal:     true,
			},
			want: false,
		},
		{
			name: "commentary",
			in: AssistantFinalCandidate{
				IsAssistant: true,
				Content:     &blank,
			},
			want: false,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsBlankAssistantFinal(testCase.in); got != testCase.want {
				t.Fatalf("IsBlankAssistantFinal(%+v) = %t, want %t", testCase.in, got, testCase.want)
			}
		})
	}
}
