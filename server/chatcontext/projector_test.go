package chatcontext

import (
	"testing"

	"core/shared/serverapi"
)

func TestProjectNormalizesAndDerivesContextFacts(t *testing.T) {
	tests := []struct {
		name  string
		input ProjectionInput
		want  serverapi.ChatContext
	}{
		{
			name: "lazy zero usage",
			input: ProjectionInput{
				Policy:                Policy{ContextWindowTokens: 100, AutomaticThresholdTokens: 80, CompactionMode: serverapi.ChatContextCompactionModeLocal},
				AutoCompactionEnabled: true,
			},
			want: serverapi.ChatContext{
				ContextWindowTokens: 100, RemainingTokens: 100, AutomaticThresholdTokens: 80,
				AutoCompactionEnabled: true, CompactionMode: serverapi.ChatContextCompactionModeLocal,
			},
		},
		{
			name: "usage above window remains overrun",
			input: ProjectionInput{
				Policy:     Policy{ContextWindowTokens: 100, AutomaticThresholdTokens: 80, CompactionMode: serverapi.ChatContextCompactionModeProviderNative},
				UsedTokens: 125,
			},
			want: serverapi.ChatContext{
				ContextWindowTokens: 100, UsedTokens: 125, RemainingTokens: -25, AutomaticThresholdTokens: 80,
				CompactionMode: serverapi.ChatContextCompactionModeProviderNative,
			},
		},
		{
			name: "invalid numeric facts normalize independently",
			input: ProjectionInput{
				Policy: Policy{
					ContextWindowTokens:       -1,
					EffectiveConfiguredWindow: 200,
					AutomaticThresholdTokens:  300,
					CompactionMode:            serverapi.ChatContextCompactionModeDisabled,
				},
				UsedTokens:               -7,
				CompletedCompactionCount: -3,
				ManualCompactEligible:    true,
			},
			want: serverapi.ChatContext{
				ContextWindowTokens: 200, RemainingTokens: 200, AutomaticThresholdTokens: 200,
				CompactionMode: serverapi.ChatContextCompactionModeDisabled,
			},
		},
		{
			name: "negative threshold clamps to zero",
			input: ProjectionInput{
				Policy: Policy{ContextWindowTokens: 100, AutomaticThresholdTokens: -4, CompactionMode: serverapi.ChatContextCompactionModeLocal},
			},
			want: serverapi.ChatContext{
				ContextWindowTokens: 100, RemainingTokens: 100, CompactionMode: serverapi.ChatContextCompactionModeLocal,
			},
		},
		{
			name: "running compaction preserves usage and prevents manual Compact",
			input: ProjectionInput{
				Policy:                   Policy{ContextWindowTokens: 100, AutomaticThresholdTokens: 80, CompactionMode: serverapi.ChatContextCompactionModeLocal},
				UsedTokens:               45,
				CompletedCompactionCount: 2,
				CompactionRunning:        true,
				ManualCompactEligible:    true,
			},
			want: serverapi.ChatContext{
				ContextWindowTokens: 100, UsedTokens: 45, RemainingTokens: 55, AutomaticThresholdTokens: 80,
				CompactionMode: serverapi.ChatContextCompactionModeLocal, CompletedCompactionCount: 2, CompactionRunning: true,
			},
		},
		{
			name: "eligible idle enabled mode permits manual Compact",
			input: ProjectionInput{
				Policy:                Policy{ContextWindowTokens: 100, AutomaticThresholdTokens: 80, CompactionMode: serverapi.ChatContextCompactionModeProviderNative},
				ManualCompactEligible: true,
			},
			want: serverapi.ChatContext{
				ContextWindowTokens: 100, RemainingTokens: 100, AutomaticThresholdTokens: 80,
				CompactionMode: serverapi.ChatContextCompactionModeProviderNative, ManualCompactAvailable: true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Project(test.input)
			if got != test.want {
				t.Fatalf("Project() = %+v, want %+v", got, test.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("projected Context is invalid: %v", err)
			}
		})
	}
}

func TestProjectUsesPositiveConfiguredWindowWhenPolicyWindowIsInvalid(t *testing.T) {
	for _, invalid := range []int64{0, -1} {
		got := Project(ProjectionInput{
			Policy: Policy{
				ContextWindowTokens:       invalid,
				EffectiveConfiguredWindow: 372_000,
				CompactionMode:            serverapi.ChatContextCompactionModeLocal,
			},
		})
		if got.ContextWindowTokens != 372_000 {
			t.Fatalf("window for %d = %d, want 372000", invalid, got.ContextWindowTokens)
		}
	}
}
