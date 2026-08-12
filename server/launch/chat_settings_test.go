package launch

import (
	"slices"
	"testing"
)

func TestSupportedChatThinkingValuesUsesKnownModelContract(t *testing.T) {
	got := supportedChatThinkingValues("gpt-5", "ultra")
	if slices.Contains(got, "ultra") {
		t.Fatalf("supported thinking values = %v, unexpectedly included configured value outside the known model contract", got)
	}
}

func TestSupportedChatThinkingValuesPreservesConfiguredUnknownModelValue(t *testing.T) {
	got := supportedChatThinkingValues("custom-model", "ultra")
	if !slices.Contains(got, "ultra") {
		t.Fatalf("supported thinking values = %v, want configured unknown-model value", got)
	}
}
