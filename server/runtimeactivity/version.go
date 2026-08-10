package runtimeactivity

import (
	"core/shared/clientui"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const readModelCoordinatorLimit = 128

var readModelCoordinators = newCoordinatorCache(readModelCoordinatorLimit)

type ResponseSnapshot struct {
	Version  clientui.ReadModelVersion
	Activity clientui.RuntimeActivity
}

type SnapshotInput struct {
	Resolver ResolverSnapshot
}

type SnapshotBuilder func() (SnapshotInput, error)

type coordinatorCache struct {
	mu             sync.Mutex
	epoch          string
	limit          int
	nextGeneration uint64
	clock          uint64
	entries        map[string]*readModelCoordinator
}

type readModelCoordinator struct {
	mu         sync.Mutex
	epoch      string
	generation uint64
	sequence   uint64
	lastUsed   uint64
}

func newCoordinatorCache(limit int) *coordinatorCache {
	return &coordinatorCache{
		epoch:          "process-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36),
		limit:          limit,
		nextGeneration: 1,
		entries:        make(map[string]*readModelCoordinator),
	}
}

func (c *coordinatorCache) coordinator(sessionID string) *readModelCoordinator {
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
	coordinator := &readModelCoordinator{
		epoch:      c.epoch,
		generation: c.nextGeneration,
		lastUsed:   c.clock,
	}
	c.nextGeneration++
	c.entries[key] = coordinator
	for len(c.entries) > c.limit {
		oldestKey := ""
		oldestStamp := uint64(0)
		for candidate, entry := range c.entries {
			if candidate == key {
				continue
			}
			if oldestKey == "" || entry.lastUsed < oldestStamp {
				oldestKey = candidate
				oldestStamp = entry.lastUsed
			}
		}
		if oldestKey == "" {
			break
		}
		delete(c.entries, oldestKey)
	}
	return coordinator
}

func (c *readModelCoordinator) next() clientui.ReadModelVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextLocked()
}

func (c *readModelCoordinator) nextLocked() clientui.ReadModelVersion {
	c.sequence++
	if c.sequence == 0 {
		panic("runtime read-model version sequence overflow")
	}
	version, err := clientui.NewReadModelVersion(
		c.epoch,
		c.generation,
		c.sequence,
	)
	if err != nil {
		panic(err)
	}
	return version
}

func (c *readModelCoordinator) buildFeedSnapshot(
	build SnapshotBuilder,
) (clientui.RuntimeReadModelUpdate, error) {
	if build == nil {
		return clientui.RuntimeReadModelUpdate{}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	version := c.nextLocked()
	input, err := build()
	if err != nil {
		return clientui.RuntimeReadModelUpdate{}, err
	}
	activity, err := resolveRuntimeFeedActivity(input.Resolver)
	if err != nil {
		return clientui.RuntimeReadModelUpdate{}, err
	}
	update := clientui.RuntimeReadModelUpdate{
		Version:  version,
		Activity: activity,
	}
	if err := update.Validate(); err != nil {
		return clientui.RuntimeReadModelUpdate{}, fmt.Errorf("validate runtime feed read-model update: %w", err)
	}
	return update, nil
}

func NextReadModelVersion(sessionID string) clientui.ReadModelVersion {
	return readModelCoordinators.coordinator(sessionID).next()
}

func BuildSnapshot(sessionID string, build SnapshotBuilder) (ResponseSnapshot, error) {
	update, err := BuildFeedSnapshot(sessionID, build)
	if err != nil {
		return ResponseSnapshot{}, err
	}
	if build == nil {
		return ResponseSnapshot{}, nil
	}
	return responseSnapshot(update), nil
}

func BuildFeedSnapshot(
	sessionID string,
	build SnapshotBuilder,
) (clientui.RuntimeReadModelUpdate, error) {
	return readModelCoordinators.coordinator(sessionID).buildFeedSnapshot(build)
}

func responseSnapshot(update clientui.RuntimeReadModelUpdate) ResponseSnapshot {
	if err := update.Validate(); err != nil {
		panic(fmt.Sprintf("project invalid runtime read-model update: %+v: %v", update, err))
	}
	return ResponseSnapshot{
		Version:  update.Version,
		Activity: update.Activity,
	}
}
