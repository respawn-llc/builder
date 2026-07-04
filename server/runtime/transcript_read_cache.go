package runtime

import "sync"

type recentTailReadCache struct {
	mu        sync.Mutex
	revision  int64
	tailLimit int
	valid     bool
	window    TranscriptWindowSnapshot
}

func (c *recentTailReadCache) get(revision int64, tailLimit int) (TranscriptWindowSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid || c.revision != revision || c.tailLimit != tailLimit {
		return TranscriptWindowSnapshot{}, false
	}
	return cloneTranscriptWindow(c.window), true
}

func (c *recentTailReadCache) store(revision int64, tailLimit int, window TranscriptWindowSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revision = revision
	c.tailLimit = tailLimit
	c.window = cloneTranscriptWindow(window)
	c.valid = true
}

func cloneTranscriptWindow(window TranscriptWindowSnapshot) TranscriptWindowSnapshot {
	cloned := window
	cloned.Snapshot.Entries = clonePersistedChatEntries(window.Snapshot.Entries)
	return cloned
}
