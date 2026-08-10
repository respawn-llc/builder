package requestmemo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultTTL        = 15 * time.Minute
	defaultMaxEntries = 1024
)

var (
	// ErrOwnerUnavailable is returned when request identity has no configured owner.
	ErrOwnerUnavailable = errors.New("request identity owner is unavailable")
	// ErrClientRequestIDReused is returned when a client_request_id is reused with
	// parameters that differ from the original request.
	ErrClientRequestIDReused = errors.New("client_request_id was reused with different parameters")
	// ErrCapacityUnavailable is returned when a new request identity cannot be retained.
	ErrCapacityUnavailable = errors.New("request identity capacity is unavailable")
)

type Memo[Req any, Resp any] struct {
	mu         sync.Mutex
	entries    map[string]*entry[Req, Resp]
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

type entry[Req any, Resp any] struct {
	req         Req
	resp        Resp
	err         error
	done        chan struct{}
	completedAt time.Time
	createdAt   time.Time
}

func New[Req any, Resp any]() *Memo[Req, Resp] {
	return &Memo[Req, Resp]{
		entries:    make(map[string]*entry[Req, Resp]),
		ttl:        defaultTTL,
		maxEntries: defaultMaxEntries,
		now:        time.Now,
	}
}

func (m *Memo[Req, Resp]) Do(ctx context.Context, requestID string, req Req, same func(Req, Req) bool, run func(context.Context) (Resp, error)) (Resp, error) {
	var zero Resp
	if m == nil {
		return zero, ErrOwnerUnavailable
	}
	for {
		m.mu.Lock()
		m.pruneLocked()
		if existing := m.entries[requestID]; existing != nil {
			if same != nil && !same(existing.req, req) {
				m.mu.Unlock()
				return zero, fmt.Errorf("client_request_id %q: %w", requestID, ErrClientRequestIDReused)
			}
			done := existing.done
			m.mu.Unlock()
			select {
			case <-done:
				if existing.err == nil {
					return existing.resp, existing.err
				}
				continue
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
		if !m.ensureCapacityForInsertLocked() {
			m.mu.Unlock()
			return zero, ErrCapacityUnavailable
		}
		now := m.now()
		e := &entry[Req, Resp]{req: req, done: make(chan struct{}), createdAt: now}
		m.entries[requestID] = e
		m.mu.Unlock()

		resp, err := run(ctx)

		m.mu.Lock()
		e.resp = resp
		e.err = err
		if err == nil {
			e.completedAt = m.now()
		} else {
			delete(m.entries, requestID)
		}
		close(e.done)
		m.mu.Unlock()
		return resp, err
	}
}

func (m *Memo[Req, Resp]) pruneLocked() {
	if m == nil || len(m.entries) == 0 {
		return
	}
	now := m.now()
	for key, item := range m.entries {
		if item == nil {
			delete(m.entries, key)
			continue
		}
		if !item.completedAt.IsZero() && now.Sub(item.completedAt) >= m.ttl {
			delete(m.entries, key)
		}
	}
}

func (m *Memo[Req, Resp]) ensureCapacityForInsertLocked() bool {
	if m == nil || m.maxEntries <= 0 || len(m.entries) < m.maxEntries {
		return true
	}
	oldestKey, found := oldestCompletedEntryKey(m.entries)
	if !found {
		return false
	}
	delete(m.entries, oldestKey)
	return len(m.entries) < m.maxEntries
}

func oldestCompletedEntryKey[Req any, Resp any](entries map[string]*entry[Req, Resp]) (string, bool) {
	oldestKey := ""
	var oldestTime time.Time
	found := false
	for key, item := range entries {
		if item == nil || item.completedAt.IsZero() {
			continue
		}
		if !found || item.createdAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = item.createdAt
			found = true
		}
	}
	return oldestKey, found
}
