package metadata

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// MutationLaneRegistry serializes in-process mutations for equal keys.
type MutationLaneRegistry[K comparable] struct {
	mu      sync.Mutex
	entries map[K]*mutationLaneEntry
}

type mutationLaneEntry struct {
	mu        sync.Mutex
	changed   chan struct{}
	readers   int
	exclusive bool
	refs      int
}

// MutationLaneLease owns one key's mutation lane until Release is called.
type MutationLaneLease[K comparable] struct {
	registry *MutationLaneRegistry[K]
	key      K
	entry    *mutationLaneEntry
	shared   bool
	released bool
}

func NewMutationLaneRegistry[K comparable]() *MutationLaneRegistry[K] {
	return &MutationLaneRegistry[K]{entries: make(map[K]*mutationLaneEntry)}
}

func (r *MutationLaneRegistry[K]) Acquire(ctx context.Context, key K) (*MutationLaneLease[K], error) {
	return r.acquire(ctx, key, false)
}

// AcquireShared allows operations for the same key to overlap while remaining
// mutually exclusive with ordinary Acquire callers.
func (r *MutationLaneRegistry[K]) AcquireShared(ctx context.Context, key K) (*MutationLaneLease[K], error) {
	return r.acquire(ctx, key, true)
}

func (r *MutationLaneRegistry[K]) acquire(ctx context.Context, key K, shared bool) (*MutationLaneLease[K], error) {
	if r == nil {
		return nil, errors.New("mutation lane registry is required")
	}
	if ctx == nil {
		return nil, errors.New("mutation lane context is required")
	}
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[K]*mutationLaneEntry)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &mutationLaneEntry{changed: make(chan struct{})}
		r.entries[key] = entry
	}
	entry.refs++
	r.mu.Unlock()

	if err := entry.acquire(ctx, shared); err != nil {
		r.releaseReference(key, entry)
		return nil, err
	}
	return &MutationLaneLease[K]{registry: r, key: key, entry: entry, shared: shared}, nil
}

func (l *MutationLaneLease[K]) Release() {
	if l == nil || l.registry == nil || l.entry == nil {
		panic("release mutation lane lease invariant violated: missing lease state")
	}
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	if l.released {
		panic(fmt.Sprintf("release mutation lane lease invariant violated: key=%v released twice", l.key))
	}
	l.released = true
	l.entry.release(l.shared)
	l.registry.releaseReferenceLocked(l.key, l.entry)
}

func (e *mutationLaneEntry) acquire(ctx context.Context, shared bool) error {
	for {
		e.mu.Lock()
		if (shared && !e.exclusive) || (!shared && !e.exclusive && e.readers == 0) {
			if shared {
				e.readers++
			} else {
				e.exclusive = true
			}
			e.mu.Unlock()
			return nil
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func (e *mutationLaneEntry) release(shared bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if shared {
		if e.readers <= 0 {
			panic("release shared mutation lane invariant violated")
		}
		e.readers--
	} else {
		if !e.exclusive {
			panic("release exclusive mutation lane invariant violated")
		}
		e.exclusive = false
	}
	close(e.changed)
	e.changed = make(chan struct{})
}

func (r *MutationLaneRegistry[K]) releaseReference(key K, entry *mutationLaneEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseReferenceLocked(key, entry)
}

func (r *MutationLaneRegistry[K]) releaseReferenceLocked(key K, entry *mutationLaneEntry) {
	if registered := r.entries[key]; registered != entry || entry.refs <= 0 {
		panic(fmt.Sprintf("release mutation lane reference invariant violated: key=%v refs=%d", key, entry.refs))
	}
	entry.refs--
	if entry.refs == 0 {
		delete(r.entries, key)
	}
}
