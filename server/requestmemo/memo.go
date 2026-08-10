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
	// ErrAdmissionUnresolved is returned when admitted-work preparation returns
	// without either rejecting, completing, or admitting the reserved work.
	ErrAdmissionUnresolved = errors.New("request identity admission was not resolved")
	// ErrAdmissionResolved is returned when an admitted-work lifecycle is
	// resolved more than once.
	ErrAdmissionResolved = errors.New("request identity admission was already resolved")
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
	state       entryState
}

type entryState uint8

const (
	entryReserved entryState = iota
	entryAdmitted
	entryCompleted
	entryRejected
)

// Admission resolves one newly reserved request identity before represented
// work begins. Reject deletes the identity for retry, Complete retains a
// terminal no-work outcome, and Admit transfers completion to AdmittedWork.
type Admission[Resp any] struct {
	reject   func(error) error
	complete func(Resp, error) error
	admit    func() (*AdmittedWork[Resp], error)
}

// Reject releases a reservation because no represented work or side effect
// began.
func (a *Admission[Resp]) Reject(err error) error {
	if a == nil || a.reject == nil {
		return ErrOwnerUnavailable
	}
	return a.reject(err)
}

// Complete retains a terminal outcome for work that did not require a
// detached admitted-work owner.
func (a *Admission[Resp]) Complete(resp Resp, err error) error {
	if a == nil || a.complete == nil {
		return ErrOwnerUnavailable
	}
	return a.complete(resp, err)
}

// Admit transfers the reserved identity to a server-owned work lifecycle.
// The returned owner must complete the work exactly once.
func (a *Admission[Resp]) Admit() (*AdmittedWork[Resp], error) {
	if a == nil || a.admit == nil {
		return nil, ErrOwnerUnavailable
	}
	return a.admit()
}

// AdmittedWork owns one admitted operation until its terminal outcome is
// completed and retained.
type AdmittedWork[Resp any] struct {
	complete func(Resp, error) error
}

// Complete retains the terminal admitted-work outcome and wakes every waiter.
func (w *AdmittedWork[Resp]) Complete(resp Resp, err error) error {
	if w == nil || w.complete == nil {
		return ErrOwnerUnavailable
	}
	return w.complete(resp, err)
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
		e, owner, err := m.reserve(requestID, req, same)
		if err != nil {
			return zero, err
		}
		if !owner {
			resp, err, retry := waitForEntry(ctx, e, true)
			if retry {
				continue
			}
			return resp, err
		}

		resp, err := run(ctx)
		if err == nil {
			_ = m.complete(e, entryReserved, resp, nil)
		} else {
			_ = m.rejectReserved(requestID, e, err)
		}
		return resp, err
	}
}

// DoAdmitted reserves one request identity, invokes preparation only for its
// first owner, and makes every caller a context-cancelable waiter. Preparation
// must resolve Admission before returning.
func (m *Memo[Req, Resp]) DoAdmitted(
	ctx context.Context,
	requestID string,
	req Req,
	same func(Req, Req) bool,
	prepare func(context.Context, *Admission[Resp]),
) (Resp, error) {
	var zero Resp
	if m == nil {
		return zero, ErrOwnerUnavailable
	}
	for {
		e, owner, err := m.reserve(requestID, req, same)
		if err != nil {
			return zero, err
		}
		if owner {
			admission := &Admission[Resp]{
				reject: func(err error) error {
					return m.rejectReserved(requestID, e, err)
				},
				complete: func(resp Resp, err error) error {
					return m.complete(e, entryReserved, resp, err)
				},
				admit: func() (*AdmittedWork[Resp], error) {
					return m.admitReserved(e)
				},
			}
			prepare(ctx, admission)
			if err := m.rejectReserved(requestID, e, ErrAdmissionUnresolved); err != nil && !errors.Is(err, ErrAdmissionResolved) {
				return zero, err
			}
			resp, err, _ := waitForEntry(ctx, e, false)
			return resp, err
		}
		resp, err, retry := waitForEntry(ctx, e, true)
		if retry {
			continue
		}
		return resp, err
	}
}

func (m *Memo[Req, Resp]) reserve(requestID string, req Req, same func(Req, Req) bool) (*entry[Req, Resp], bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if existing := m.entries[requestID]; existing != nil {
		if same != nil && !same(existing.req, req) {
			return nil, false, fmt.Errorf("client_request_id %q: %w", requestID, ErrClientRequestIDReused)
		}
		return existing, false, nil
	}
	if !m.ensureCapacityForInsertLocked() {
		return nil, false, ErrCapacityUnavailable
	}
	now := m.now()
	e := &entry[Req, Resp]{
		req:       req,
		done:      make(chan struct{}),
		createdAt: now,
		state:     entryReserved,
	}
	m.entries[requestID] = e
	return e, true, nil
}

func waitForEntry[Req any, Resp any](
	ctx context.Context,
	e *entry[Req, Resp],
	retryRejected bool,
) (Resp, error, bool) {
	var zero Resp
	select {
	case <-e.done:
		if e.state == entryRejected {
			return zero, e.err, retryRejected
		}
		return e.resp, e.err, false
	case <-ctx.Done():
		return zero, ctx.Err(), false
	}
}

func (m *Memo[Req, Resp]) rejectReserved(requestID string, e *entry[Req, Resp], err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.state != entryReserved {
		return ErrAdmissionResolved
	}
	e.state = entryRejected
	e.err = err
	if m.entries[requestID] == e {
		delete(m.entries, requestID)
	}
	close(e.done)
	return nil
}

func (m *Memo[Req, Resp]) complete(
	e *entry[Req, Resp],
	expected entryState,
	resp Resp,
	err error,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.state != expected {
		return ErrAdmissionResolved
	}
	m.completeLocked(e, resp, err)
	return nil
}

func (m *Memo[Req, Resp]) admitReserved(e *entry[Req, Resp]) (*AdmittedWork[Resp], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.state != entryReserved {
		return nil, ErrAdmissionResolved
	}
	e.state = entryAdmitted
	return &AdmittedWork[Resp]{
		complete: func(resp Resp, err error) error {
			return m.complete(e, entryAdmitted, resp, err)
		},
	}, nil
}

func (m *Memo[Req, Resp]) completeLocked(e *entry[Req, Resp], resp Resp, err error) {
	e.resp = resp
	e.err = err
	e.state = entryCompleted
	e.completedAt = m.now()
	close(e.done)
}

func (m *Memo[Req, Resp]) pruneLocked() {
	if len(m.entries) == 0 {
		return
	}
	now := m.now()
	for key, item := range m.entries {
		if item.state == entryCompleted && now.Sub(item.completedAt) >= m.ttl {
			delete(m.entries, key)
		}
	}
}

func (m *Memo[Req, Resp]) ensureCapacityForInsertLocked() bool {
	if len(m.entries) < m.maxEntries {
		return true
	}
	oldestKey := oldestCompletedEntryKey(m.entries)
	if oldestKey == nil {
		return false
	}
	delete(m.entries, *oldestKey)
	return len(m.entries) < m.maxEntries
}

func oldestCompletedEntryKey[Req any, Resp any](entries map[string]*entry[Req, Resp]) *string {
	var oldest *entry[Req, Resp]
	var oldestKey *string
	for key, item := range entries {
		if item.state != entryCompleted {
			continue
		}
		if oldest == nil || item.createdAt.Before(oldest.createdAt) {
			keyCopy := key
			oldestKey = &keyCopy
			oldest = item
		}
	}
	return oldestKey
}
