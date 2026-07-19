package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/session"
	"core/shared/runtimeids"
)

type SessionStartBlockReason uint8

const (
	SessionStartBlockMaintenance SessionStartBlockReason = iota + 1
)

type SessionStartBlockRelease interface {
	Close(context.Context) error
}

type sessionAdmissionGate struct {
	mu     sync.Mutex
	blocks map[SessionStartBlockReason]int
}

type sessionStartBlockRelease struct {
	authority *Authority
	sessionID runtimeids.SessionID
	reason    SessionStartBlockReason

	mu       sync.Mutex
	released bool
}

func (a *Authority) gateFor(sessionID runtimeids.SessionID) *sessionAdmissionGate {
	a.mu.Lock()
	defer a.mu.Unlock()
	gate := a.gates[sessionID]
	if gate == nil {
		gate = &sessionAdmissionGate{}
		a.gates[sessionID] = gate
	}
	return gate
}

func (a *Authority) BlockSessionStarts(ctx context.Context, sessionID runtimeids.SessionID, reason SessionStartBlockReason) (SessionStartBlockRelease, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if sessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	if reason == 0 {
		return nil, errors.New("session start block reason is required")
	}
	gate := a.gateFor(sessionID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if gate.blocks == nil {
		gate.blocks = make(map[SessionStartBlockReason]int)
	}
	gate.blocks[reason]++
	return &sessionStartBlockRelease{authority: a, sessionID: sessionID, reason: reason}, nil
}

func (r *sessionStartBlockRelease) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return nil
	}
	gate := r.authority.gateFor(r.sessionID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.blocks[r.reason] <= 0 {
		panic(fmt.Sprintf("session start block %d for session %s underflow", r.reason, r.sessionID))
	}
	gate.blocks[r.reason]--
	if gate.blocks[r.reason] == 0 {
		delete(gate.blocks, r.reason)
	}
	r.released = true
	return nil
}

func (a *Authority) WithSessionStore(ctx context.Context, descriptor session.SessionDescriptor, callback func(context.Context, *session.Store) error) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	sessionID := descriptor.SessionID()
	if sessionID.IsZero() {
		return errors.New("session id is required")
	}
	if callback == nil {
		return errors.New("session store callback is required")
	}
	gate := a.gateFor(sessionID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource != nil {
		return resource.withStore(ctx, callback)
	}
	store, err := session.MaterializeSessionDescriptor(a.options.persistenceRoot, descriptor, a.options.storeOptions...)
	if err != nil {
		return err
	}
	return callback(ctx, store)
}
