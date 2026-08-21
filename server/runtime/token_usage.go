package runtime

import (
	"sync"
)

type usageEstimateBaseline struct {
	inputTokens             int
	estimatedProviderTokens int
}

type tokenUsageTracker struct {
	mu sync.Mutex

	usageBaseline usageEstimateBaseline
}

func newTokenUsageTracker() *tokenUsageTracker {
	return &tokenUsageTracker{}
}

func (t *tokenUsageTracker) storeUsageBaseline(inputTokens, estimatedProviderTokens int) {
	if t == nil {
		return
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	if estimatedProviderTokens < 0 {
		estimatedProviderTokens = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usageBaseline = usageEstimateBaseline{
		inputTokens:             inputTokens,
		estimatedProviderTokens: estimatedProviderTokens,
	}
}

func (t *tokenUsageTracker) estimateCurrentInputTokens(currentEstimatedProviderTokens int) (int, bool) {
	if t == nil {
		return 0, false
	}
	if currentEstimatedProviderTokens < 0 {
		currentEstimatedProviderTokens = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	baseline := t.usageBaseline
	if baseline.inputTokens <= 0 {
		if currentEstimatedProviderTokens <= 0 {
			return 0, false
		}
		return currentEstimatedProviderTokens, true
	}
	delta := currentEstimatedProviderTokens - baseline.estimatedProviderTokens
	if delta <= 0 {
		return baseline.inputTokens, true
	}
	return baseline.inputTokens + delta, true
}
