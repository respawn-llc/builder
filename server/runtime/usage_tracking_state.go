package runtime

import (
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
)

type usageTrackingState struct {
	mu                     sync.Mutex
	lastUsage              llm.Usage
	totalInputTokens       int
	totalCachedInputTokens int
}

func newUsageTrackingState() *usageTrackingState {
	return &usageTrackingState{}
}

func (s *usageTrackingState) Last() llm.Usage {
	if s == nil {
		return llm.Usage{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsage
}

func (s *usageTrackingState) Next(usage llm.Usage) (llm.Usage, int, int) {
	normalizedUsage := normalizeUsageForTrackingState(usage)
	if s == nil {
		totalInputTokens, totalCachedInputTokens := nextUsageTrackingTotals(0, 0, normalizedUsage)
		return normalizedUsage, totalInputTokens, totalCachedInputTokens
	}
	s.mu.Lock()
	totalInputTokens := s.totalInputTokens
	totalCachedInputTokens := s.totalCachedInputTokens
	s.mu.Unlock()
	totalInputTokens, totalCachedInputTokens = nextUsageTrackingTotals(totalInputTokens, totalCachedInputTokens, normalizedUsage)
	return normalizedUsage, totalInputTokens, totalCachedInputTokens
}

func (s *usageTrackingState) Apply(usage llm.Usage, totalInputTokens, totalCachedInputTokens int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastUsage = usage
	s.totalInputTokens = totalInputTokens
	s.totalCachedInputTokens = totalCachedInputTokens
	s.mu.Unlock()
}

func (s *usageTrackingState) CacheHitSnapshot() (int, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.totalInputTokens <= 0 {
		return 0, false
	}
	cachedTokens := s.totalCachedInputTokens
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > s.totalInputTokens {
		cachedTokens = s.totalInputTokens
	}
	pct := (cachedTokens * 100) / s.totalInputTokens
	if pct < 0 {
		return 0, false
	}
	if pct > 100 {
		return 100, true
	}
	return pct, true
}

func normalizeUsageForTrackingState(usage llm.Usage) llm.Usage {
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.WindowTokens < 0 {
		usage.WindowTokens = 0
	}
	usage.CachedInputTokens = textutil.Pointer(usage.CachedInputTokens)
	if usage.CachedInputTokens != nil && *usage.CachedInputTokens < 0 {
		*usage.CachedInputTokens = 0
	}
	if usage.CachedInputTokens != nil && *usage.CachedInputTokens > usage.InputTokens {
		*usage.CachedInputTokens = usage.InputTokens
	}
	return usage
}

func normalizePersistedUsageTrackingState(state session.UsageState) session.UsageState {
	if state.InputTokens < 0 {
		state.InputTokens = 0
	}
	if state.OutputTokens < 0 {
		state.OutputTokens = 0
	}
	if state.WindowTokens < 0 {
		state.WindowTokens = 0
	}
	if state.CachedInputTokens < 0 {
		state.CachedInputTokens = 0
	}
	if state.CachedInputTokens > state.InputTokens {
		state.CachedInputTokens = state.InputTokens
	}
	if state.EstimatedProviderTokens < 0 {
		state.EstimatedProviderTokens = 0
	}
	if state.TotalInputTokens < 0 {
		state.TotalInputTokens = 0
	}
	if state.TotalCachedInputTokens < 0 {
		state.TotalCachedInputTokens = 0
	}
	if state.TotalCachedInputTokens > state.TotalInputTokens {
		state.TotalCachedInputTokens = state.TotalInputTokens
	}
	return state
}

func nextUsageTrackingTotals(totalInputTokens, totalCachedInputTokens int, usage llm.Usage) (int, int) {
	if totalInputTokens < 0 {
		totalInputTokens = 0
	}
	if totalCachedInputTokens < 0 {
		totalCachedInputTokens = 0
	}
	if usage.CachedInputTokens != nil && usage.InputTokens > 0 {
		totalInputTokens += usage.InputTokens
		totalCachedInputTokens += *usage.CachedInputTokens
		if totalCachedInputTokens > totalInputTokens {
			totalCachedInputTokens = totalInputTokens
		}
	}
	return totalInputTokens, totalCachedInputTokens
}
