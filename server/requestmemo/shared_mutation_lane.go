package requestmemo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"golang.org/x/sync/semaphore"
)

// SharedMutationLaneRegistry coordinates concurrent readers with exclusive
// writers for equal keys.
type SharedMutationLaneRegistry[K comparable] struct {
	mu      sync.Mutex
	entries map[K]*sharedMutationLaneEntry
}

type sharedMutationLaneEntry struct {
	semaphore *semaphore.Weighted
	refs      int
}

type SharedMutationLaneLease[K comparable] struct {
	registry *SharedMutationLaneRegistry[K]
	key      K
	entry    *sharedMutationLaneEntry
	weight   int64
	released bool
}

func NewSharedMutationLaneRegistry[K comparable]() *SharedMutationLaneRegistry[K] {
	return &SharedMutationLaneRegistry[K]{entries: make(map[K]*sharedMutationLaneEntry)}
}

func (r *SharedMutationLaneRegistry[K]) AcquireShared(ctx context.Context, key K) (*SharedMutationLaneLease[K], error) {
	return r.acquire(ctx, key, 1)
}

func (r *SharedMutationLaneRegistry[K]) AcquireExclusive(ctx context.Context, key K) (*SharedMutationLaneLease[K], error) {
	return r.acquire(ctx, key, math.MaxInt64)
}

func (r *SharedMutationLaneRegistry[K]) acquire(ctx context.Context, key K, weight int64) (*SharedMutationLaneLease[K], error) {
	if r == nil {
		return nil, errors.New("shared mutation lane registry is required")
	}
	if ctx == nil {
		return nil, errors.New("shared mutation lane context is required")
	}
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[K]*sharedMutationLaneEntry)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &sharedMutationLaneEntry{semaphore: semaphore.NewWeighted(math.MaxInt64)}
		r.entries[key] = entry
	}
	entry.refs++
	r.mu.Unlock()
	if err := entry.semaphore.Acquire(ctx, weight); err != nil {
		r.releaseReference(key, entry)
		return nil, err
	}
	return &SharedMutationLaneLease[K]{registry: r, key: key, entry: entry, weight: weight}, nil
}

func (l *SharedMutationLaneLease[K]) Release() {
	if l == nil || l.registry == nil || l.entry == nil {
		panic("release shared mutation lane lease invariant violated: missing lease state")
	}
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	if l.released {
		panic(fmt.Sprintf("release shared mutation lane lease invariant violated: key=%v released twice", l.key))
	}
	l.released = true
	l.entry.semaphore.Release(l.weight)
	l.registry.releaseReferenceLocked(l.key, l.entry)
}

func (r *SharedMutationLaneRegistry[K]) releaseReference(key K, entry *sharedMutationLaneEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseReferenceLocked(key, entry)
}

func (r *SharedMutationLaneRegistry[K]) releaseReferenceLocked(key K, entry *sharedMutationLaneEntry) {
	if registered := r.entries[key]; registered != entry || entry.refs <= 0 {
		panic(fmt.Sprintf("release shared mutation lane reference invariant violated: key=%v refs=%d", key, entry.refs))
	}
	entry.refs--
	if entry.refs == 0 {
		delete(r.entries, key)
	}
}
