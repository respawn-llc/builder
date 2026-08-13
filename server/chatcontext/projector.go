package chatcontext

import "core/shared/serverapi"

type Policy struct {
	ContextWindowTokens       int64
	EffectiveConfiguredWindow int64
	AutomaticThresholdTokens  int64
	CompactionMode            serverapi.ChatContextCompactionMode
}

type ProjectionInput struct {
	Policy                   Policy
	UsedTokens               int64
	AutoCompactionEnabled    bool
	CompletedCompactionCount int64
	CompactionRunning        bool
	ManualCompactEligible    bool
}

func Project(input ProjectionInput) serverapi.ChatContext {
	window := input.Policy.ContextWindowTokens
	if window <= 0 {
		window = input.Policy.EffectiveConfiguredWindow
	}
	used := max(input.UsedTokens, 0)
	threshold := min(max(input.Policy.AutomaticThresholdTokens, 0), window)
	count := max(input.CompletedCompactionCount, 0)
	mode := input.Policy.CompactionMode

	return serverapi.ChatContext{
		ContextWindowTokens:      window,
		UsedTokens:               used,
		RemainingTokens:          window - used,
		AutomaticThresholdTokens: threshold,
		AutoCompactionEnabled:    input.AutoCompactionEnabled,
		CompactionMode:           mode,
		CompletedCompactionCount: count,
		CompactionRunning:        input.CompactionRunning,
		ManualCompactAvailable: mode != serverapi.ChatContextCompactionModeDisabled &&
			!input.CompactionRunning &&
			input.ManualCompactEligible,
	}
}
