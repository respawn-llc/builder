package llm

import (
	"testing"

	"core/server/session"
)

func TestLookupModelMetadata(t *testing.T) {
	meta, ok := LookupModelMetadata("gpt-5.3-codex")
	if !ok {
		t.Fatal("expected model metadata for gpt-5.3-codex")
	}
	if meta.ContextWindowTokens != 400_000 {
		t.Fatalf("unexpected context window: %d", meta.ContextWindowTokens)
	}
}

func TestLookupModelMetadataCaseInsensitive(t *testing.T) {
	meta, ok := LookupModelMetadata(" GPT-5.3-CODEX ")
	if !ok {
		t.Fatal("expected case-insensitive model metadata lookup")
	}
	if meta.ContextWindowTokens != 400_000 {
		t.Fatalf("unexpected context window: %d", meta.ContextWindowTokens)
	}
}

func TestLookupModelMetadataForCodexSpark(t *testing.T) {
	meta, ok := LookupModelMetadata("gpt-5.3-codex-spark")
	if !ok {
		t.Fatal("expected model metadata for gpt-5.3-codex-spark")
	}
	if meta.ContextWindowTokens != 128_000 {
		t.Fatalf("unexpected context window: %d", meta.ContextWindowTokens)
	}
}

func TestLookupModelMetadataForGPT55ContextWindow(t *testing.T) {
	meta, ok := LookupModelMetadata("gpt-5.5")
	if !ok {
		t.Fatal("expected model metadata for gpt-5.5")
	}
	if meta.ContextWindowTokens != 272_000 {
		t.Fatalf("unexpected default context window: %d", meta.ContextWindowTokens)
	}
	if meta.LargeContextWindowTokens != 272_000 {
		t.Fatalf("unexpected large context window: %d", meta.LargeContextWindowTokens)
	}
}

func TestLookupModelMetadataForGPT54LargeContext(t *testing.T) {
	meta, ok := LookupModelMetadata("gpt-5.4")
	if !ok {
		t.Fatal("expected model metadata for gpt-5.4")
	}
	if meta.ContextWindowTokens != 272_000 {
		t.Fatalf("unexpected default context window: %d", meta.ContextWindowTokens)
	}
	if meta.LargeContextWindowTokens != 1_000_000 {
		t.Fatalf("unexpected large context window: %d", meta.LargeContextWindowTokens)
	}
}

func TestLookupModelMetadataForGPT54MiniLargeContext(t *testing.T) {
	meta, ok := LookupModelMetadata("gpt-5.4-mini")
	if !ok {
		t.Fatal("expected model metadata for gpt-5.4-mini")
	}
	if meta.ContextWindowTokens != 272_000 {
		t.Fatalf("unexpected default context window: %d", meta.ContextWindowTokens)
	}
	if meta.LargeContextWindowTokens != 400_000 {
		t.Fatalf("unexpected large context window: %d", meta.LargeContextWindowTokens)
	}
}

func TestSupportedThinkingLevelsModel(t *testing.T) {
	levels := SupportedThinkingLevelsModel("gpt-5.5")
	if got := len(levels); got != 4 {
		t.Fatalf("expected 4 gpt-5.5 thinking levels, got %d (%v)", got, levels)
	}
	if levels[3] != "xhigh" {
		t.Fatalf("expected xhigh support for gpt-5.5, got %v", levels)
	}
	unknown := SupportedThinkingLevelsModel("custom-alias")
	if got := len(unknown); got != 3 {
		t.Fatalf("expected default thinking levels for unknown model, got %d (%v)", got, unknown)
	}
}

func TestSupportsReasoningEffortModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-5.5", want: true},
		{model: "gpt-5.4", want: true},
		{model: "gpt-5.4-mini", want: true},
		{model: "gpt-5.4-nano", want: true},
		{model: "gpt-5.3-codex", want: true},
		{model: "gpt-5.3-codex-spark", want: true},
		{model: "claude-3-7-sonnet", want: true},
		{model: "custom-alias", want: true},
		{model: "", want: false},
	}

	for _, tc := range tests {
		if got := SupportsReasoningEffortModel(tc.model); got != tc.want {
			t.Fatalf("SupportsReasoningEffortModel(%q)=%v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestSupportsReasoningSummaryModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-5.5", want: true},
		{model: "gpt-5.4", want: true},
		{model: "gpt-5.4-mini", want: true},
		{model: "gpt-5.4-nano", want: true},
		{model: "gpt-5.3-codex", want: true},
		{model: "gpt-5.3-codex-spark", want: false},
		{model: "custom-alias", want: false},
		{model: "", want: false},
	}

	for _, tc := range tests {
		if got := SupportsReasoningSummaryModel(tc.model); got != tc.want {
			t.Fatalf("SupportsReasoningSummaryModel(%q)=%v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestSupportsVisionInputsModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-5.5", want: true},
		{model: "gpt-5.3-codex", want: true},
		{model: "gpt-5.3-codex-spark", want: false},
		{model: " GPT-4.1 ", want: true},
		{model: "gpt-5.4-mini", want: true},
		{model: "gpt-5.4-nano", want: false},
		{model: "claude-3-7-sonnet", want: false},
		{model: "", want: false},
	}

	for _, tc := range tests {
		if got := SupportsVisionInputsModel(tc.model); got != tc.want {
			t.Fatalf("SupportsVisionInputsModel(%q)=%v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestSupportsVerbosityModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-5.5", want: true},
		{model: "gpt-5.4", want: true},
		{model: "gpt-5.4-mini", want: true},
		{model: "gpt-5.4-nano", want: true},
		{model: "gpt-5.3-codex", want: true},
		{model: "gpt-5.3-codex-spark", want: true},
		{model: " GPT-5-preview ", want: false},
		{model: "custom-alias", want: false},
		{model: "", want: false},
	}

	for _, tc := range tests {
		if got := SupportsVerbosityModel(tc.model); got != tc.want {
			t.Fatalf("SupportsVerbosityModel(%q)=%v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestVerbositySupportForModelAndProvider(t *testing.T) {
	firstParty := ProviderCapabilities{ProviderID: "openai", IsOpenAIFirstParty: true}
	compatible := ProviderCapabilities{ProviderID: "openai-compatible", IsOpenAIFirstParty: false}

	if support := VerbositySupportForModelAndProvider("gpt-5-preview", firstParty); !support.Supported || support.Source != ModelVerbositySupportSourceProviderDefault {
		t.Fatalf("unknown first-party support = %+v, want provider default support", support)
	}
	if support := VerbositySupportForModelAndProvider("gpt-5-preview", compatible); support.Supported || support.Source != ModelVerbositySupportSourceProviderDefault {
		t.Fatalf("unknown compatible support = %+v, want provider default unsupported", support)
	}
	if support := VerbositySupportForModelAndProvider("gpt-4.1", firstParty); support.Supported || support.Source != ModelVerbositySupportSourceModelCatalog {
		t.Fatalf("known unsupported support = %+v, want model catalog unsupported", support)
	}
	if support := VerbositySupportForModelAndProvider("gpt-5", compatible); !support.Supported || support.Source != ModelVerbositySupportSourceModelCatalog {
		t.Fatalf("known supported support = %+v, want model catalog support", support)
	}
}

func TestKnownModelCapabilityContractsDeterministic(t *testing.T) {
	first := KnownModelCapabilityContracts()
	second := KnownModelCapabilityContracts()
	if len(first) == 0 {
		t.Fatal("expected known model contracts")
	}
	if len(first) != len(second) {
		t.Fatalf("catalog length changed across calls: %d vs %d", len(first), len(second))
	}
	seen := map[string]struct{}{}
	for i := range first {
		if first[i].Model != second[i].Model {
			t.Fatalf("catalog order changed at %d: %q vs %q", i, first[i].Model, second[i].Model)
		}
		if _, ok := seen[first[i].Model]; ok {
			t.Fatalf("duplicate model contract for %q", first[i].Model)
		}
		seen[first[i].Model] = struct{}{}
		if _, ok := LookupModelCapabilityContract(first[i].Model); !ok {
			t.Fatalf("catalog model %q missing lookup contract", first[i].Model)
		}
	}
}

func TestModelDisplayLabel(t *testing.T) {
	tests := []struct {
		model         string
		thinkingLevel string
		want          string
	}{
		{model: "gpt-5.3-codex", thinkingLevel: "high", want: "gpt-5.3-codex high"},
		{model: "claude-3-7-sonnet", thinkingLevel: "high", want: "claude-3-7-sonnet high"},
		{model: "custom-alias", thinkingLevel: "high", want: "custom-alias high"},
		{model: "", thinkingLevel: "", want: "gpt-5"},
	}

	for _, tc := range tests {
		if got := ModelDisplayLabel(tc.model, tc.thinkingLevel); got != tc.want {
			t.Fatalf("ModelDisplayLabel(%q, %q)=%q, want %q", tc.model, tc.thinkingLevel, got, tc.want)
		}
	}
}

func TestLockedContractCapabilityFallbackForLegacySessions(t *testing.T) {
	legacy := &session.LockedContract{Model: "gpt-5.3-codex"}
	if !LockedContractSupportsReasoningEffort(legacy, legacy.Model) {
		t.Fatal("expected legacy locked session to fall back to registry reasoning support")
	}
	if !LockedContractSupportsVisionInputs(legacy, legacy.Model) {
		t.Fatal("expected legacy locked session to fall back to registry vision support")
	}
}

func TestLockedContractCapabilityFallbackIgnoresProviderOnlySnapshot(t *testing.T) {
	locked := &session.LockedContract{
		Model: "gpt-5.4",
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID: "chatgpt-codex",
		},
	}
	if !LockedContractSupportsReasoningEffort(locked, locked.Model) {
		t.Fatal("expected provider-only locked session to fall back to registry reasoning support")
	}
	if !LockedContractSupportsVisionInputs(locked, locked.Model) {
		t.Fatal("expected provider-only locked session to fall back to registry vision support")
	}
}
