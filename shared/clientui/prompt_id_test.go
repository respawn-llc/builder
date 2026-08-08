package clientui

import "testing"

func TestPromptIDRejectsPaddedIdentity(t *testing.T) {
	for _, promptID := range []PromptID{" prompt-1", "prompt-1 "} {
		if err := promptID.Validate(); err == nil {
			t.Fatalf("PromptID %q validated", promptID)
		}
	}
}
