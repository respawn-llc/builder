package config

import "testing"

func TestMinimumThresholdTokens(t *testing.T) {
	if got := MinimumThresholdTokens(300_000); got != 150_000 {
		t.Fatalf("MinimumThresholdTokens = %d, want %d", got, 150_000)
	}
}

func TestEffectivePreSubmitThresholdTokens(t *testing.T) {
	tests := []struct {
		name          string
		autoThreshold int
		runwayTokens  int
		expected      int
	}{
		{
			name:          "subtracts runway from auto threshold",
			autoThreshold: 190_000,
			runwayTokens:  35_000,
			expected:      155_000,
		},
		{
			name:          "large windows still use fixed runway",
			autoThreshold: 950_000,
			runwayTokens:  35_000,
			expected:      915_000,
		},
		{
			name:          "clamps to one token minimum",
			autoThreshold: 20,
			runwayTokens:  50,
			expected:      1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectivePreSubmitThresholdTokens(tt.autoThreshold, tt.runwayTokens); got != tt.expected {
				t.Fatalf("EffectivePreSubmitThresholdTokens = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestEstimatedToolCallsForTokenBudget(t *testing.T) {
	if got := EstimatedToolCallsForTokenBudget(258_400); got != 185 {
		t.Fatalf("EstimatedToolCallsForTokenBudget = %d, want 185", got)
	}
	if got := EstimatedToolCallsForTokenBudget(0); got != 0 {
		t.Fatalf("EstimatedToolCallsForTokenBudget(0) = %d, want 0", got)
	}
}

func TestEstimatedToolCallsForCompactionThreshold(t *testing.T) {
	if got := EstimatedToolCallsForCompactionThreshold(258_400); got != 185 {
		t.Fatalf("EstimatedToolCallsForCompactionThreshold = %d, want 185", got)
	}
}

func TestEstimatedToolCallsForContextWindow(t *testing.T) {
	if got := EstimatedToolCallsForContextWindow(272_000, 95); got != 185 {
		t.Fatalf("EstimatedToolCallsForContextWindow = %d, want 185", got)
	}
	if got := EstimatedToolCallsForContextWindow(272_000, 150); got != 194 {
		t.Fatalf("EstimatedToolCallsForContextWindow clamps percent = %d, want 194", got)
	}
}

func TestEstimatedToolCallsForContextWindowAvoidsOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if got := EstimatedToolCallsForContextWindow(maxInt, 100); got <= 0 {
		t.Fatalf("EstimatedToolCallsForContextWindow(maxInt) = %d, want positive", got)
	}
}

func TestEstimatedTokensPerToolCallMatchesCorpusFixture(t *testing.T) {
	if estimatedToolCallCorpusStartDate != "2026-04-12" {
		t.Fatalf("corpus start date = %q, want 2026-04-12", estimatedToolCallCorpusStartDate)
	}
	if estimatedToolCallCorpusCompactionModeFilter != "auto,handoff" {
		t.Fatalf("corpus mode filter = %q, want auto,handoff", estimatedToolCallCorpusCompactionModeFilter)
	}
	if estimatedToolCallCorpusSampleSize != 67 {
		t.Fatalf("corpus sample size = %d, want 67", estimatedToolCallCorpusSampleSize)
	}
	medianTokensPerCall := estimatedToolCallCorpusThresholdTokens / estimatedToolCallCorpusMedianDistance
	meanTokensPerCall := estimatedToolCallCorpusThresholdTokens * 100 / estimatedToolCallCorpusMeanDistanceCenticalls
	if medianTokensPerCall != 1_381 {
		t.Fatalf("median-derived tokens/call = %d, want 1381", medianTokensPerCall)
	}
	if meanTokensPerCall != 1_397 {
		t.Fatalf("mean-derived tokens/call = %d, want 1397", meanTokensPerCall)
	}
	if EstimatedTokensPerToolCall != 1_400 {
		t.Fatalf("EstimatedTokensPerToolCall = %d, want rounded corpus heuristic 1400", EstimatedTokensPerToolCall)
	}
}
