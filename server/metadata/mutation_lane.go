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
	token chan struct{}
	refs  int
}

// MutationLaneLease owns one key's mutation lane until Release is called.
type MutationLaneLease[K comparable] struct {
	registry *MutationLaneRegistry[K]
	key      K
	entry    *mutationLaneEntry
	released bool
}

func NewMutationLaneRegistry[K comparable]() *MutationLaneRegistry[K] {
	return &MutationLaneRegistry[K]{entries: make(map[K]*mutationLaneEntry)}
}

func (r *MutationLaneRegistry[K]) Acquire(ctx context.Context, key K) (*MutationLaneLease[K], error) {
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
		entry = &mutationLaneEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		r.entries[key] = entry
	}
	entry.refs++
	r.mu.Unlock()

	select {
	case <-ctx.Done():
		r.releaseReference(key, entry)
		return nil, ctx.Err()
	case <-entry.token:
	}
	return &MutationLaneLease[K]{registry: r, key: key, entry: entry}, nil
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
	select {
	case l.entry.token <- struct{}{}:
	default:
		panic(fmt.Sprintf("release mutation lane lease invariant violated: key=%v token already available", l.key))
	}
	l.registry.releaseReferenceLocked(l.key, l.entry)
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
