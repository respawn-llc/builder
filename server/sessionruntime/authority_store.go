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
	authority  *Authority
	sessionIDs []runtimeids.SessionID
	reason     SessionStartBlockReason

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

func (a *Authority) BlockSessionStarts(ctx context.Context, sessionIDs []runtimeids.SessionID, reason SessionStartBlockReason) (SessionStartBlockRelease, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if len(sessionIDs) == 0 {
		return nil, errors.New("session ids are required")
	}
	if reason == 0 {
		return nil, errors.New("session start block reason is required")
	}
	release := &sessionStartBlockRelease{authority: a, reason: reason}
	for _, sessionID := range sessionIDs {
		if sessionID.IsZero() {
			return nil, errors.Join(errors.New("session id is required"), release.Close(context.Background()))
		}
		gate := a.gateFor(sessionID)
		gate.mu.Lock()
		if err := context.Cause(ctx); err != nil {
			gate.mu.Unlock()
			return nil, errors.Join(err, release.Close(context.Background()))
		}
		if gate.blocks == nil {
			gate.blocks = make(map[SessionStartBlockReason]int)
		}
		gate.blocks[reason]++
		release.sessionIDs = append(release.sessionIDs, sessionID)
		gate.mu.Unlock()
	}
	return release, nil
}

func (r *sessionStartBlockRelease) Close(context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return nil
	}
	for index := len(r.sessionIDs) - 1; index >= 0; index-- {
		sessionID := r.sessionIDs[index]
		gate := r.authority.gateFor(sessionID)
		gate.mu.Lock()
		if gate.blocks[r.reason] <= 0 {
			panic(fmt.Sprintf("session start block %d for session %s underflow", r.reason, sessionID))
		}
		gate.blocks[r.reason]--
		if gate.blocks[r.reason] == 0 {
			delete(gate.blocks, r.reason)
		}
		gate.mu.Unlock()
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
