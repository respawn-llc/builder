package runtime

import (
	"sort"
	"sync"

	"core/server/llm"
)

const (
	preciseTokenCountCacheSize       = 16
	preciseRefreshStartPercent       = 50
	preciseRefreshFullPercent        = 90
	significantGrowthHistoryLimit    = 8
	significantGrowthTurnsPerRefresh = 3
	fallbackPreciseRefreshGapDivisor = 3
)

type tokenUsageMutation uint8

const (
	tokenUsageMutationPlain tokenUsageMutation = iota
	tokenUsageMutationSignificant
	tokenUsageMutationHardReset
)

func tokenUsageMutationForMessage(msg llm.Message) tokenUsageMutation {
	if msg.Role == llm.RoleTool {
		return tokenUsageMutationSignificant
	}
	if msg.Role == llm.RoleDeveloper {
		return tokenUsageMutationSignificant
	}
	if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
		return tokenUsageMutationSignificant
	}
	return tokenUsageMutationPlain
}

type preciseTokenSnapshot struct {
	requestKey  string
	inputTokens int
}

type usageEstimateBaseline struct {
	inputTokens             int
	estimatedProviderTokens int
}

type tokenUsageTracker struct {
	mu sync.Mutex

	current                  preciseTokenSnapshot
	usageBaseline            usageEstimateBaseline
	lastPreciseInputTokens   int
	forceCurrentPreciseCheck bool
	pendingSignificantGrowth bool
	recentSignificantGrowth  []int
	cache                    preciseTokenCountCache
}

func newTokenUsageTracker() *tokenUsageTracker {
	return &tokenUsageTracker{cache: newPreciseTokenCountCache(preciseTokenCountCacheSize)}
}

func (t *tokenUsageTracker) invalidateCurrent(mutation tokenUsageMutation) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current.inputTokens > t.lastPreciseInputTokens {
		t.lastPreciseInputTokens = t.current.inputTokens
	}
	t.current = preciseTokenSnapshot{}
	switch mutation {
	case tokenUsageMutationSignificant:
		t.forceCurrentPreciseCheck = true
		t.pendingSignificantGrowth = true
	case tokenUsageMutationHardReset:
		t.usageBaseline = usageEstimateBaseline{}
		t.lastPreciseInputTokens = 0
		t.forceCurrentPreciseCheck = false
		t.pendingSignificantGrowth = false
		t.recentSignificantGrowth = nil
	}
}

func (t *tokenUsageTracker) store(requestKey string, inputTokens int, current bool) {
	if t == nil || requestKey == "" || inputTokens <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cache.put(requestKey, inputTokens)
	if !current {
		return
	}
	if t.pendingSignificantGrowth && t.lastPreciseInputTokens > 0 && inputTokens > t.lastPreciseInputTokens {
		t.appendSignificantGrowthSample(inputTokens - t.lastPreciseInputTokens)
	}
	t.current = preciseTokenSnapshot{requestKey: requestKey, inputTokens: inputTokens}
	t.lastPreciseInputTokens = inputTokens
	t.forceCurrentPreciseCheck = false
	t.pendingSignificantGrowth = false
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

func (t *tokenUsageTracker) lookup(requestKey string) (int, bool) {
	if t == nil || requestKey == "" {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cache.get(requestKey)
}

func (t *tokenUsageTracker) lookupCurrent(requestKey string) (int, bool) {
	if t == nil {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current.requestKey == "" || t.current.inputTokens <= 0 {
		return 0, false
	}
	if requestKey != "" && t.current.requestKey != requestKey {
		return 0, false
	}
	return t.current.inputTokens, true
}

func (t *tokenUsageTracker) currentCheckpointDue(estimatedInputTokens, threshold int, critical bool) bool {
	if t == nil || threshold <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentCheckpointDueLocked(estimatedInputTokens, threshold, critical)
}

func (t *tokenUsageTracker) currentCheckpointDueLocked(estimatedInputTokens, threshold int, critical bool) bool {
	if estimatedInputTokens >= preciseRefreshAlwaysThreshold(threshold) {
		return true
	}
	if critical && t.forceCurrentPreciseCheck {
		return true
	}
	checkpoint := firstPreciseRefreshCheckpoint(threshold)
	if estimatedInputTokens < checkpoint {
		return false
	}
	lastPrecise := t.lastPreciseInputTokens
	if t.current.inputTokens > lastPrecise {
		lastPrecise = t.current.inputTokens
	}
	if lastPrecise < checkpoint {
		return true
	}
	return estimatedInputTokens >= nextPreciseRefreshCheckpoint(lastPrecise, threshold, t.recentSignificantGrowth)
}

func (t *tokenUsageTracker) appendSignificantGrowthSample(delta int) {
	if delta <= 0 {
		return
	}
	t.recentSignificantGrowth = append(t.recentSignificantGrowth, delta)
	if len(t.recentSignificantGrowth) <= significantGrowthHistoryLimit {
		return
	}
	offset := len(t.recentSignificantGrowth) - significantGrowthHistoryLimit
	copy(t.recentSignificantGrowth, t.recentSignificantGrowth[offset:])
	t.recentSignificantGrowth = t.recentSignificantGrowth[:significantGrowthHistoryLimit]
}

func firstPreciseRefreshCheckpoint(threshold int) int {
	if threshold <= 0 {
		return 0
	}
	checkpoint := (threshold * preciseRefreshStartPercent) / 100
	if checkpoint < 1 {
		return 1
	}
	return checkpoint
}

func preciseRefreshAlwaysThreshold(threshold int) int {
	if threshold <= 0 {
		return 0
	}
	checkpoint := (threshold * preciseRefreshFullPercent) / 100
	if checkpoint < 1 {
		return 1
	}
	if checkpoint > threshold {
		return threshold
	}
	return checkpoint
}

func nextPreciseRefreshCheckpoint(lastPreciseInputTokens, threshold int, recentSignificantGrowth []int) int {
	if threshold <= 0 {
		return 0
	}
	checkpoint := firstPreciseRefreshCheckpoint(threshold)
	if lastPreciseInputTokens < checkpoint {
		return checkpoint
	}
	alwaysPrecise := preciseRefreshAlwaysThreshold(threshold)
	if lastPreciseInputTokens >= alwaysPrecise {
		return alwaysPrecise
	}
	remaining := alwaysPrecise - lastPreciseInputTokens
	if remaining <= 1 {
		return alwaysPrecise
	}
	step := fallbackPreciseRefreshStep(remaining)
	if predictedGrowth, ok := predictedSignificantGrowthTokens(recentSignificantGrowth); ok {
		adaptiveStep := predictedGrowth * significantGrowthTurnsPerRefresh
		remainingCap := remaining / 2
		if remainingCap < 1 {
			remainingCap = 1
		}
		if adaptiveStep < 1 {
			adaptiveStep = 1
		}
		if adaptiveStep > remainingCap {
			adaptiveStep = remainingCap
		}
		step = adaptiveStep
	}
	next := lastPreciseInputTokens + step
	if next <= lastPreciseInputTokens {
		next = lastPreciseInputTokens + 1
	}
	if next > alwaysPrecise {
		return alwaysPrecise
	}
	return next
}

func fallbackPreciseRefreshStep(remaining int) int {
	if remaining <= 1 {
		return 1
	}
	step := remaining / fallbackPreciseRefreshGapDivisor
	if step < 1 {
		return 1
	}
	return step
}

func predictedSignificantGrowthTokens(samples []int) (int, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	sorted := append([]int(nil), samples...)
	sort.Ints(sorted)
	idx := (len(sorted) * 3) / 4
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	if sorted[idx] <= 0 {
		return 0, false
	}
	return sorted[idx], true
}

type preciseTokenCountCache struct {
	limit   int
	entries map[string]int
	order   []string
}

func newPreciseTokenCountCache(limit int) preciseTokenCountCache {
	if limit < 1 {
		limit = 1
	}
	return preciseTokenCountCache{
		limit:   limit,
		entries: make(map[string]int, limit),
		order:   make([]string, 0, limit),
	}
}

func (c *preciseTokenCountCache) get(key string) (int, bool) {
	if c == nil || key == "" {
		return 0, false
	}
	count, ok := c.entries[key]
	if !ok || count <= 0 {
		return 0, false
	}
	c.touch(key)
	return count, true
}

func (c *preciseTokenCountCache) put(key string, count int) {
	if c == nil || key == "" || count <= 0 {
		return
	}
	if _, ok := c.entries[key]; ok {
		c.entries[key] = count
		c.touch(key)
		return
	}
	if len(c.order) >= c.limit {
		evict := c.order[0]
		delete(c.entries, evict)
		c.order = c.order[1:]
	}
	c.entries[key] = count
	c.order = append(c.order, key)
}

func (c *preciseTokenCountCache) touch(key string) {
	if c == nil || len(c.order) <= 1 {
		return
	}
	idx := -1
	for i := range c.order {
		if c.order[i] == key {
			idx = i
			break
		}
	}
	if idx < 0 || idx == len(c.order)-1 {
		return
	}
	copy(c.order[idx:], c.order[idx+1:])
	c.order[len(c.order)-1] = key
}
