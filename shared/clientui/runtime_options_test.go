package clientui

import "testing"

func TestNormalizeThinkingLevel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "low", raw: "low", want: "low", ok: true},
		{name: "medium", raw: "medium", want: "medium", ok: true},
		{name: "high", raw: "high", want: "high", ok: true},
		{name: "xhigh", raw: "xhigh", want: "xhigh", ok: true},
		{name: "max", raw: "max", want: "max", ok: true},
		{name: "ultra", raw: "ultra", want: "ultra", ok: true},
		{name: "trim lower", raw: " HIGH ", want: "high", ok: true},
		{name: "empty", raw: "", ok: false},
		{name: "unknown", raw: "unsupported", want: "unsupported", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeThinkingLevel(tt.raw)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("NormalizeThinkingLevel(%q) = %q, %t; want %q, %t", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}
