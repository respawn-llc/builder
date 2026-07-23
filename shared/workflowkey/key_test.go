package workflowkey

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	max := strings.Repeat("a", MaxChars)
	tooLong := max + "a"
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "empty", value: "", want: false},
		{name: "whitespace", value: "   ", want: false},
		{name: "min lowercase", value: "a", want: true},
		{name: "typical", value: "agent_2", want: true},
		{name: "max length", value: max, want: true},
		{name: "too long", value: tooLong, want: false},
		{name: "uppercase first", value: "Agent", want: false},
		{name: "digit first", value: "1agent", want: false},
		{name: "underscore first", value: "_agent", want: false},
		{name: "symbol first", value: "-agent", want: false},
		{name: "uppercase later", value: "aGent", want: false},
		{name: "symbol later", value: "agent-name", want: false},
		{name: "digit later", value: "agent2", want: true},
		{name: "underscore later", value: "agent_two", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Valid(tt.value); got != tt.want {
				t.Fatalf("Valid(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
