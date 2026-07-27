// Package mutationlane serializes in-process mutations for individual keys.
package mutationlane

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Registry serializes leases for equal keys while allowing different keys to proceed independently.
type Registry[K comparable] struct {
	mu      sync.Mutex
	entries map[K]*entry
}

type entry struct {
	token chan struct{}
	refs  int
}

// Lease owns one key's mutation lane until Release is called.
type Lease[K comparable] struct {
	registry *Registry[K]
	key      K
	entry    *entry
	released bool
}

// NewRegistry constructs an empty keyed mutation-lane registry.
func NewRegistry[K comparable]() *Registry[K] {
	return &Registry[K]{entries: make(map[K]*entry)}
}

// Acquire waits for exclusive access to key until ctx is canceled.
func (r *Registry[K]) Acquire(ctx context.Context, key K) (*Lease[K], error) {
	if r == nil {
		return nil, errors.New("mutation lane registry is required")
	}
	if ctx == nil {
		return nil, errors.New("mutation lane context is required")
	}

	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[K]*entry)
	}
	current := r.entries[key]
	if current == nil {
		current = &entry{token: make(chan struct{}, 1)}
		current.token <- struct{}{}
		r.entries[key] = current
	}
	current.refs++
	r.mu.Unlock()

	select {
	case <-ctx.Done():
		r.releaseReference(key, current)
		return nil, ctx.Err()
	case <-current.token:
	}

	return &Lease[K]{
		registry: r,
		key:      key,
		entry:    current,
	}, nil
}

// Release releases this lease. Releasing a lease more than once is an invariant violation.
func (l *Lease[K]) Release() {
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

func (r *Registry[K]) releaseReference(key K, current *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseReferenceLocked(key, current)
}

func (r *Registry[K]) releaseReferenceLocked(key K, current *entry) {
	registered := r.entries[key]
	if registered != current || current.refs <= 0 {
		panic(fmt.Sprintf(
			"release mutation lane reference invariant violated: key=%v refs=%d registered_same=%t",
			key,
			current.refs,
			registered == current,
		))
	}
	current.refs--
	if current.refs == 0 {
		delete(r.entries, key)
	}
}
