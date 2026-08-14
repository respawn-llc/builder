package launch

import (
	"testing"

	"core/server/llm"
	"core/shared/config"
)

func TestApplyContextPolicyUsesFinalRoleResolvedSettings(t *testing.T) {
	t.Parallel()
	settings := config.DefaultOnboardingSettings()
	settings.ModelContextWindow = 128_000
	settings.ContextCompactionThresholdTokens = 121_600
	settings.CompactionMode = config.CompactionModeNative
	settings.OpenAIBaseURL = "https://compatible.example/v1"

	plan := ApplyContextPolicy(
		SessionPlan{ActiveSettings: settings},
		llm.ProviderCapabilities{SupportsResponsesCompact: false},
	)
	if plan.ActiveSettings.CompactionMode != config.CompactionModeLocal {
		t.Fatalf("CompactionMode = %q, want local fallback from final role endpoint", plan.ActiveSettings.CompactionMode)
	}
}
