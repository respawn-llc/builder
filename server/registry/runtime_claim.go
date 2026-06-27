package registry

import (
	"context"
	"strings"

	"core/server/runtime"
)

type RuntimeClaim struct {
	registry *RuntimeRegistry
	id       string
	entry    *runtimeEntry
}

type ClaimJoinOutcome int

const (
	ClaimStale ClaimJoinOutcome = iota
	ClaimClosing
	ClaimFailed
	ClaimJoined
)

type RuntimeReleaseDecision int

const (
	RuntimeReleaseStale RuntimeReleaseDecision = iota
	RuntimeReleaseClosing
	RuntimeReleaseNotOwner
	RuntimeReleaseDroppedRef
	RuntimeReleaseIdleCheck
	RuntimeReleaseClose
)

func (r *RuntimeRegistry) AcquireRuntimeClaim(sessionID string, ownerID string) (*RuntimeClaim, bool, bool) {
	if r == nil {
		return nil, false, false
	}
	id := strings.TrimSpace(sessionID)
	entry, reused, closing := r.directory.acquireOrCreateBuilding(id)
	if entry == nil {
		return nil, false, false
	}
	if !reused && !closing {
		entry.addOwner(ownerID)
	}
	return &RuntimeClaim{registry: r, id: id, entry: entry}, reused, closing
}

func (r *RuntimeRegistry) ClaimFreshRuntime(ctx context.Context, sessionID string, ownerID string, beforeReplace func(*runtime.Engine) error) (*RuntimeClaim, error) {
	if r == nil {
		return nil, nil
	}
	id := strings.TrimSpace(sessionID)
	for {
		entry, existing := r.directory.installBuildingIfAbsent(id)
		if entry != nil {
			entry.addOwner(ownerID)
			return &RuntimeClaim{registry: r, id: id, entry: entry}, nil
		}
		if _, err := existing.awaitReady(ctx); err != nil {
			return nil, err
		}
		if beforeReplace != nil {
			if err := beforeReplace(existing.engineRef()); err != nil {
				return nil, err
			}
		}
		if _, err := r.closeEntry(ctx, id, existing.engineRef(), nil); err != nil {
			return nil, err
		}
	}
}

func (r *RuntimeRegistry) RuntimeClaimFor(sessionID string) *RuntimeClaim {
	if r == nil {
		return nil
	}
	id := strings.TrimSpace(sessionID)
	entry := r.directory.Entry(id)
	if entry == nil {
		return nil
	}
	return &RuntimeClaim{registry: r, id: id, entry: entry}
}

func (r *RuntimeRegistry) AcquiredRuntimeClosed(sessionID string, engine *runtime.Engine) (<-chan struct{}, bool) {
	if r == nil {
		return nil, false
	}
	d := r.directory
	d.mu.RLock()
	defer d.mu.RUnlock()
	entry := d.entries[strings.TrimSpace(sessionID)]
	if entry == nil {
		return nil, false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.closing || (engine != nil && entry.engine != engine) {
		return nil, false
	}
	return entry.closed, true
}

func (c *RuntimeClaim) AwaitReady(ctx context.Context) (*runtime.Engine, error) {
	if c == nil {
		return nil, nil
	}
	return c.entry.awaitReady(ctx)
}

func (c *RuntimeClaim) AwaitClosed(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.entry.awaitClosed(ctx)
}

func (c *RuntimeClaim) Resolve(engine *runtime.Engine, rebind func(string) error, teardown func()) {
	if c == nil {
		return
	}
	c.entry.resolveBuild(engine, rebind, teardown, nil)
}

func (c *RuntimeClaim) Fail(err error) {
	if c == nil {
		return
	}
	c.entry.resolveBuild(nil, nil, nil, err)
	d := c.registry.directory
	d.mu.Lock()
	if d.entries[c.id] == c.entry {
		delete(d.entries, c.id)
	}
	d.mu.Unlock()
}

func (c *RuntimeClaim) IsCurrent() bool {
	if c == nil {
		return false
	}
	d := c.registry.directory
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.entries[c.id] == c.entry
}

func (c *RuntimeClaim) Closing() bool {
	if c == nil {
		return false
	}
	return c.entry.isClosing()
}

func (c *RuntimeClaim) Engine() *runtime.Engine {
	if c == nil {
		return nil
	}
	return c.entry.engineRef()
}

func (c *RuntimeClaim) ActivationErr() error {
	if c == nil {
		return nil
	}
	c.entry.mu.Lock()
	defer c.entry.mu.Unlock()
	return c.entry.buildErr
}

func (c *RuntimeClaim) Rebind(workdir string) error {
	if c == nil {
		return nil
	}
	return c.entry.rebindWorkdir(workdir)
}

func (c *RuntimeClaim) OwnerCount() int {
	if c == nil {
		return 0
	}
	return c.entry.ownerCount()
}

func (c *RuntimeClaim) JoinAsOwner(ownerID string) (ClaimJoinOutcome, error) {
	if c == nil {
		return ClaimStale, nil
	}
	d := c.registry.directory
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.entries[c.id] != c.entry {
		return ClaimStale, nil
	}
	e := c.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return ClaimClosing, nil
	}
	if e.buildErr != nil {
		return ClaimFailed, e.buildErr
	}
	if trimmed := strings.TrimSpace(ownerID); trimmed != "" {
		if e.ownerIDs == nil {
			e.ownerIDs = make(map[string]struct{})
		}
		if _, exists := e.ownerIDs[trimmed]; !exists {
			e.ownerIDs[trimmed] = struct{}{}
			e.ownerRefs++
		}
		return ClaimJoined, nil
	}
	e.ownerRefs++
	return ClaimJoined, nil
}

func (c *RuntimeClaim) DropOwner(ownerID string) int {
	if c == nil {
		return 0
	}
	d := c.registry.directory
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.entries[c.id] != c.entry {
		return 0
	}
	return c.entry.dropOwner(ownerID)
}

func (c *RuntimeClaim) BeginRelease(ownerID string, dropOwner bool, onlyIfIdle bool) (RuntimeReleaseDecision, int) {
	if c == nil {
		return RuntimeReleaseStale, 0
	}
	d := c.registry.directory
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.entries[c.id] != c.entry {
		return RuntimeReleaseStale, 0
	}
	e := c.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return RuntimeReleaseClosing, 0
	}
	if trimmed := strings.TrimSpace(ownerID); trimmed != "" {
		if _, owns := e.ownerIDs[trimmed]; !owns {
			return RuntimeReleaseNotOwner, 0
		}
	}
	if !onlyIfIdle {
		return RuntimeReleaseClose, e.ownerRefs
	}
	if dropOwner && e.ownerRefs > 1 {
		e.ownerRefs--
		if trimmed := strings.TrimSpace(ownerID); trimmed != "" {
			delete(e.ownerIDs, trimmed)
		}
		return RuntimeReleaseDroppedRef, e.ownerRefs
	}
	return RuntimeReleaseIdleCheck, e.ownerRefs
}

func (c *RuntimeClaim) Close(ctx context.Context, drain func(context.Context) error) (bool, error) {
	if c == nil {
		return false, nil
	}
	return c.registry.closeEntry(ctx, c.id, c.entry.engineRef(), drain)
}

func (c *RuntimeClaim) CloseIfIdle(ctx context.Context, expectedRefs int, drain func(context.Context) error) (bool, error) {
	if c == nil {
		return false, nil
	}
	drainRef, ok := c.beginIdleClose(expectedRefs)
	if !ok {
		return false, nil
	}
	return c.registry.finishClose(ctx, c.id, c.entry.engineRef(), c.entry, drainRef, drain)
}

func (c *RuntimeClaim) beginIdleClose(expectedRefs int) (*runtimeCloseDrainRef, bool) {
	d := c.registry.directory
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.entries[c.id] != c.entry {
		return nil, false
	}
	e := c.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing || e.closeDraining || e.ownerRefs != expectedRefs {
		return nil, false
	}
	e.closing = true
	e.closeDraining = true
	e.inFlight++
	e.cond.Broadcast()
	return &runtimeCloseDrainRef{entry: e}, true
}
