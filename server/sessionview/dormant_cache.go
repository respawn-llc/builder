package sessionview

import (
	"context"
	"strings"
	"sync"

	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
)

const dormantTranscriptCacheMaxEntries = 16

type dormantTranscriptCache struct {
	mu      sync.RWMutex
	entries map[string]dormantTranscriptCacheEntry
	build   func(context.Context, *session.Store) (dormantTranscriptCacheEntry, error)
	maxSize int
	clock   uint64
}

type dormantTranscriptCacheEntry struct {
	sessionDir                   string
	sessionID                    string
	revision                     int64
	lastCommittedAssistantAnswer string
	newestSegment                runtime.TranscriptSegmentPage
	activeRun                    *clientui.RunView
	lastUsed                     uint64
}

func newDormantTranscriptCacheWithLimit(limit int, build func(context.Context, *session.Store) (dormantTranscriptCacheEntry, error)) *dormantTranscriptCache {
	if build == nil {
		build = buildDormantTranscriptCacheEntry
	}
	if limit <= 0 {
		limit = dormantTranscriptCacheMaxEntries
	}
	return &dormantTranscriptCache{entries: make(map[string]dormantTranscriptCacheEntry), build: build, maxSize: limit}
}

func (c *dormantTranscriptCache) get(ctx context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
	if c == nil || store == nil {
		return dormantTranscriptCacheEntry{}, nil
	}
	meta := store.Meta()
	key := dormantTranscriptCacheKey(store.Dir(), meta.SessionID)
	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok && strings.TrimSpace(entry.sessionDir) == strings.TrimSpace(store.Dir()) &&
		strings.TrimSpace(entry.sessionID) == strings.TrimSpace(meta.SessionID) &&
		entry.revision == meta.LastSequence {
		entry.lastUsed = c.nextStampLocked()
		c.entries[key] = entry
		c.mu.Unlock()
		return entry, nil
	}
	c.mu.Unlock()
	built, err := c.build(ctx, store)
	if err != nil {
		return dormantTranscriptCacheEntry{}, err
	}
	c.mu.Lock()
	if existing, ok := c.entries[key]; ok &&
		strings.TrimSpace(existing.sessionDir) == strings.TrimSpace(store.Dir()) &&
		strings.TrimSpace(existing.sessionID) == strings.TrimSpace(meta.SessionID) &&
		existing.revision == meta.LastSequence {
		existing.lastUsed = c.nextStampLocked()
		c.entries[key] = existing
		c.mu.Unlock()
		return existing, nil
	}
	built.lastUsed = c.nextStampLocked()
	c.entries[key] = built
	c.evictIfNeededLocked()
	c.mu.Unlock()
	return built, nil
}

func (c *dormantTranscriptCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
	c.clock = 0
}

func (c *dormantTranscriptCache) nextStampLocked() uint64 {
	c.clock++
	return c.clock
}

func (c *dormantTranscriptCache) evictIfNeededLocked() {
	if c == nil || c.maxSize <= 0 || len(c.entries) <= c.maxSize {
		return
	}
	oldestKey := ""
	oldestStamp := uint64(0)
	for key, entry := range c.entries {
		if oldestKey == "" || entry.lastUsed < oldestStamp {
			oldestKey = key
			oldestStamp = entry.lastUsed
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func dormantTranscriptCacheKey(sessionDir, sessionID string) string {
	return strings.TrimSpace(sessionDir) + "::" + strings.TrimSpace(sessionID)
}

func buildDormantTranscriptCacheEntry(ctx context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
	return buildDormantTranscriptCacheEntryWithMode(ctx, store, config.CacheWarningModeDefault)
}

func buildDormantTranscriptCacheEntryWithMode(_ context.Context, store *session.Store, cacheWarningMode config.CacheWarningMode) (dormantTranscriptCacheEntry, error) {
	meta := store.Meta()
	segment, err := runtime.TranscriptSegmentPageFromStore(store, 0, cacheWarningMode)
	if err != nil {
		return dormantTranscriptCacheEntry{}, err
	}
	var activeRun *clientui.RunView
	latestRun, err := store.LatestRun()
	if err != nil {
		return dormantTranscriptCacheEntry{}, err
	}
	if latestRun != nil && latestRun.Status == session.RunStatusRunning {
		activeRun = runtimeview.RunViewFromSessionRecord(meta.SessionID, latestRun)
	}
	return dormantTranscriptCacheEntry{
		sessionDir:                   store.Dir(),
		sessionID:                    meta.SessionID,
		revision:                     meta.LastSequence,
		lastCommittedAssistantAnswer: segment.LastCommittedAssistantFinalAnswer,
		newestSegment:                segment,
		activeRun:                    activeRun,
	}, nil
}

func (e dormantTranscriptCacheEntry) mainView(meta session.Meta, freshness clientui.ConversationFreshness) clientui.RuntimeMainView {
	status := clientui.RuntimeStatus{
		ConversationFreshness:             freshness,
		ParentSessionID:                   meta.ParentSessionID,
		LastCommittedAssistantFinalAnswer: e.lastCommittedAssistantAnswer,
		Goal:                              runtimeview.GoalFromSessionState(meta.Goal, false),
	}
	if meta.WorkflowSession != nil {
		status.WorkflowSession = &clientui.WorkflowSessionStatus{
			RunID:      strings.TrimSpace(meta.WorkflowSession.RunID),
			TaskID:     strings.TrimSpace(meta.WorkflowSession.TaskID),
			WorkflowID: strings.TrimSpace(meta.WorkflowSession.WorkflowID),
		}
	}
	return clientui.RuntimeMainView{
		Status: status,
		Session: clientui.RuntimeSessionView{
			SessionID:             meta.SessionID,
			SessionName:           meta.Name,
			ConversationFreshness: freshness,
			Transcript: clientui.TranscriptMetadata{
				Revision: meta.LastSequence,
			},
		},
		ActiveRun: e.activeRun,
	}
}

func (e dormantTranscriptCacheEntry) newestSegmentPage(meta session.Meta, freshness clientui.ConversationFreshness) clientui.TranscriptPage {
	return runtimeview.TranscriptPageFromSegment(meta.SessionID, meta.Name, freshness, meta.LastSequence, e.newestSegment)
}
