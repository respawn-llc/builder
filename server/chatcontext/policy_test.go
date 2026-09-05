package chatcontext

import (
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestResolvePolicyUsesUnlockedFinalSettings(t *testing.T) {
	t.Parallel()
	settings := config.DefaultOnboardingSettings()
	settings.ModelContextWindow = 200_000
	settings.ContextCompactionThresholdTokens = 180_000
	settings.CompactionMode = config.CompactionModeNative

	got := ResolvePolicy(settings, llm.ProviderCapabilities{SupportsResponsesCompact: true}, nil)

	want := Policy{
		ContextWindowTokens:      200_000,
		AutomaticThresholdTokens: 180_000,
		CompactionMode:           serverapi.ChatContextCompactionModeProviderNative,
	}
	if got != want {
		t.Fatalf("ResolvePolicy() = %+v, want %+v", got, want)
	}
}

func TestResolvePolicyUsesCurrentBudgetAndPreservesProviderCapabilities(t *testing.T) {
	t.Parallel()
	settings := config.DefaultOnboardingSettings()
	settings.ModelContextWindow = 300_000
	settings.ContextCompactionThresholdTokens = 250_000
	settings.CompactionMode = config.CompactionModeNative
	locked := &session.LockedContract{
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID:               "openai-compatible",
			SupportsResponsesCompact: false,
		},
	}

	got := ResolvePolicy(settings, llm.ProviderCapabilities{
		ProviderID:               "chatgpt-codex",
		SupportsResponsesCompact: true,
	}, locked)

	if got.ContextWindowTokens != 300_000 ||
		got.AutomaticThresholdTokens != 250_000 ||
		got.CompactionMode != serverapi.ChatContextCompactionModeLocal {
		t.Fatalf("ResolvePolicy() = %+v, want current budget and preserved provider capabilities", got)
	}
}

func TestResolvePolicyCompactionModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mode     config.CompactionMode
		supports bool
		want     serverapi.ChatContextCompactionMode
	}{
		{name: "disabled", mode: config.CompactionModeNone, supports: true, want: serverapi.ChatContextCompactionModeDisabled},
		{name: "local", mode: config.CompactionModeLocal, supports: true, want: serverapi.ChatContextCompactionModeLocal},
		{name: "provider native", mode: config.CompactionModeNative, supports: true, want: serverapi.ChatContextCompactionModeProviderNative},
		{name: "unsupported native falls back locally", mode: config.CompactionModeNative, supports: false, want: serverapi.ChatContextCompactionModeLocal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := config.DefaultOnboardingSettings()
			settings.CompactionMode = test.mode
			got := ResolvePolicy(settings, llm.ProviderCapabilities{SupportsResponsesCompact: test.supports}, nil)
			if got.CompactionMode != test.want {
				t.Fatalf("CompactionMode = %q, want %q", got.CompactionMode, test.want)
			}
		})
	}
}

func TestResolvePolicyNormalizesThresholdAndConfiguredWindow(t *testing.T) {
	t.Parallel()
	defaultWindow := config.DefaultOnboardingSettings().ModelContextWindow
	tests := []struct {
		name      string
		window    int
		threshold int
		want      int64
	}{
		{name: "negative threshold", window: 100_000, threshold: -1, want: 0},
		{name: "threshold above window", window: 100_000, threshold: 120_000, want: 100_000},
		{name: "non-positive window uses effective configured default", window: 0, threshold: 50_000, want: 50_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := config.DefaultOnboardingSettings()
			settings.ModelContextWindow = test.window
			settings.ContextCompactionThresholdTokens = test.threshold
			got := ResolvePolicy(settings, llm.ProviderCapabilities{}, nil)
			if got.AutomaticThresholdTokens != test.want {
				t.Fatalf("AutomaticThresholdTokens = %d, want %d", got.AutomaticThresholdTokens, test.want)
			}
			if test.window <= 0 && got.ContextWindowTokens != int64(defaultWindow) {
				t.Fatalf("ContextWindowTokens = %d, want default %d", got.ContextWindowTokens, defaultWindow)
			}
		})
	}
}
