package launch

import (
	"testing"

	"core/server/llm"
	"core/server/session"
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

func TestApplyContextPolicyPreservesLockedContinuity(t *testing.T) {
	t.Parallel()
	settings := config.DefaultOnboardingSettings()
	settings.ModelContextWindow = 300_000
	settings.ContextCompactionThresholdTokens = 250_000
	settings.CompactionMode = config.CompactionModeNative
	plan := ApplyContextPolicy(
		SessionPlan{
			ActiveSettings: settings,
			Locked: &session.LockedContract{
				ContextWindow: 100_000,
				ProviderContract: session.LockedProviderCapabilities{
					ProviderID:               "openai-compatible",
					SupportsResponsesCompact: false,
				},
			},
		},
		llm.ProviderCapabilities{SupportsResponsesCompact: true},
	)
	if plan.ActiveSettings.ModelContextWindow != 100_000 ||
		plan.ActiveSettings.ContextCompactionThresholdTokens != 100_000 ||
		plan.ActiveSettings.CompactionMode != config.CompactionModeLocal {
		t.Fatalf("ActiveSettings = %+v, want locked continuity and current clamped threshold", plan.ActiveSettings)
	}
}
