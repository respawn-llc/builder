package runtimeactivity

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"core/shared/clientui"
)

const DefaultCoordinatorCacheLimit = 128

var defaultCoordinatorCache = NewCoordinatorCache(DefaultCoordinatorCacheLimit)

type ResponseSnapshot struct {
	Version             clientui.ReadModelVersion
	Activity            clientui.RuntimeActivity
	InputReconciliation clientui.RuntimeInputReconciliationSnapshot
}

type SnapshotInput struct {
	Resolver            ResolverSnapshot
	InputReconciliation clientui.RuntimeInputReconciliationSnapshot
}

type SnapshotBuilder func(clientui.ReadModelVersion) (SnapshotInput, error)

type CoordinatorCache struct {
	mu             sync.Mutex
	epoch          string
	limit          int
	nextGeneration uint64
	clock          uint64
	entries        map[string]*ReadModelCoordinator
}

func NewCoordinatorCache(limit int) *CoordinatorCache {
	if limit <= 0 {
		limit = DefaultCoordinatorCacheLimit
	}
	return &CoordinatorCache{
		epoch:          "process-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36),
		limit:          limit,
		nextGeneration: 1,
		entries:        make(map[string]*ReadModelCoordinator),
	}
}

type ReadModelCoordinator struct {
	mu         sync.Mutex
	sessionID  string
	epoch      string
	generation uint64
	sequence   uint64
	lastUsed   uint64
	pins       uint64
}

func (c *CoordinatorCache) Next(sessionID string) clientui.ReadModelVersion {
	return c.coordinator(sessionID).Next()
}

func (c *CoordinatorCache) Snapshot(sessionID string, resolver ResolverSnapshot) (ResponseSnapshot, error) {
	return c.WithSnapshot(sessionID, func(version clientui.ReadModelVersion) (SnapshotInput, error) {
		return SnapshotInput{
			Resolver:            resolver,
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
		}, nil
	})
}

func (c *CoordinatorCache) WithSnapshot(sessionID string, build SnapshotBuilder) (ResponseSnapshot, error) {
	if build == nil {
		return ResponseSnapshot{}, nil
	}
	return c.coordinator(sessionID).Snapshot(build)
}

func (c *CoordinatorCache) IsCurrent(sessionID string, version clientui.ReadModelVersion) bool {
	if c == nil {
		return false
	}
	coord := c.find(sessionID)
	if coord == nil {
		return false
	}
	return coord.IsCurrent(version)
}

func (c *CoordinatorCache) Pin(sessionID string) func() {
	coord := c.coordinator(sessionID)
	coord.mu.Lock()
	coord.pins++
	coord.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			coord.mu.Lock()
			if coord.pins > 0 {
				coord.pins--
			}
			coord.mu.Unlock()
		})
	}
}

func (c *CoordinatorCache) coordinator(sessionID string) *ReadModelCoordinator {
	if c == nil {
		c = defaultCoordinatorCache
	}
	key := strings.TrimSpace(sessionID)
	if key == "" {
		key = "unknown"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.entries[key]; existing != nil {
		c.clock++
		existing.lastUsed = c.clock
		return existing
	}
	c.clock++
	coord := &ReadModelCoordinator{
		sessionID:  key,
		epoch:      c.epoch,
		generation: c.nextGeneration,
		lastUsed:   c.clock,
	}
	c.nextGeneration++
	c.entries[key] = coord
	c.evictIfNeededLocked(key)
	return coord
}

func (c *CoordinatorCache) find(sessionID string) *ReadModelCoordinator {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		key = "unknown"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[key]
}

func (c *CoordinatorCache) evictIfNeededLocked(pinnedKey string) {
	for len(c.entries) > c.limit {
		oldestKey := ""
		oldestStamp := uint64(0)
		for key, coord := range c.entries {
			if key == pinnedKey {
				continue
			}
			coord.mu.Lock()
			pinned := coord.pins > 0
			coord.mu.Unlock()
			if pinned {
				continue
			}
			if oldestKey == "" || coord.lastUsed < oldestStamp {
				oldestKey = key
				oldestStamp = coord.lastUsed
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.entries, oldestKey)
	}
}

func (c *ReadModelCoordinator) Next() clientui.ReadModelVersion {
	if c == nil {
		return defaultCoordinatorCache.Next("unknown")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence++
	version, err := clientui.NewReadModelVersion(c.epoch, c.generation, c.sequence)
	if err != nil {
		panic(err)
	}
	return version
}

func (c *ReadModelCoordinator) Snapshot(build SnapshotBuilder) (ResponseSnapshot, error) {
	if c == nil {
		return defaultCoordinatorCache.WithSnapshot("unknown", build)
	}
	if build == nil {
		return ResponseSnapshot{}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence++
	version, err := clientui.NewReadModelVersion(c.epoch, c.generation, c.sequence)
	if err != nil {
		panic(err)
	}
	input, err := build(version)
	if err != nil {
		return ResponseSnapshot{}, err
	}
	activity, err := ResolveRuntimeActivity(input.Resolver)
	if err != nil {
		return ResponseSnapshot{}, err
	}
	reconciliation := input.InputReconciliation
	if reconciliation.Version.Validate() != nil {
		reconciliation = clientui.NewEmptyRuntimeInputReconciliationSnapshot(version)
	}
	if reconciliation.Version != version {
		return ResponseSnapshot{}, fmt.Errorf("input reconciliation version %+v does not match response snapshot version %+v", reconciliation.Version, version)
	}
	for _, record := range reconciliation.Operations {
		if record.Version != version {
			return ResponseSnapshot{}, fmt.Errorf("input reconciliation record version %+v does not match response snapshot version %+v", record.Version, version)
		}
	}
	return ResponseSnapshot{
		Version:             version,
		Activity:            activity,
		InputReconciliation: reconciliation,
	}, nil
}

func (c *ReadModelCoordinator) IsCurrent(version clientui.ReadModelVersion) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return version.Epoch == c.epoch && version.Generation == c.generation && version.Sequence <= c.sequence
}

func NextReadModelVersion(sessionID string) clientui.ReadModelVersion {
	return defaultCoordinatorCache.Next(sessionID)
}

func BuildResponseSnapshot(sessionID string, resolver ResolverSnapshot) (ResponseSnapshot, error) {
	return defaultCoordinatorCache.Snapshot(sessionID, resolver)
}

func BuildSnapshot(sessionID string, build SnapshotBuilder) (ResponseSnapshot, error) {
	return defaultCoordinatorCache.WithSnapshot(sessionID, build)
}
