package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"core/server/session"
	"core/shared/runtimeids"
)

type SessionStartBlockReason uint8

var ErrSessionStartAdmissionBusy = errors.New("session start admission is busy")

const (
	SessionStartBlockMaintenance SessionStartBlockReason = iota + 1
)

type SessionStartBlockRelease interface {
	AuthorizeMaintenance(context.Context) context.Context
	Close(context.Context) error
}

type sessionAdmissionBlock struct {
	reason SessionStartBlockReason
}

type sessionAdmissionGate struct {
	mu     contextMutex
	blocks map[*sessionAdmissionBlock]struct{}
}

type contextMutex struct {
	token chan struct{}
}

func newContextMutex() contextMutex {
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return contextMutex{token: token}
}

func (m *contextMutex) Lock(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.token:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (m *contextMutex) TryLock() bool {
	select {
	case <-m.token:
		return true
	default:
		return false
	}
}

func (m *contextMutex) Unlock() {
	select {
	case m.token <- struct{}{}:
	default:
		panic("context mutex unlock without lock")
	}
}

type sessionStartBlockRelease struct {
	authority  *Authority
	sessionIDs []runtimeids.SessionID
	block      *sessionAdmissionBlock

	mu       sync.Mutex
	released bool
}

type sessionMaintenanceAuthorizationContextKey struct{}

type sessionMaintenanceAuthorization struct {
	block  *sessionAdmissionBlock
	parent *sessionMaintenanceAuthorization
}

func (a *Authority) gateFor(sessionID runtimeids.SessionID) *sessionAdmissionGate {
	a.mu.Lock()
	defer a.mu.Unlock()
	gate := a.gates[sessionID]
	if gate == nil {
		gate = &sessionAdmissionGate{mu: newContextMutex()}
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
	normalizedSessionIDs, err := normalizeSessionStartBlockSessionIDs(sessionIDs)
	if err != nil {
		return nil, err
	}
	release, err := a.newSessionStartBlockRelease(reason)
	if err != nil {
		return nil, err
	}
	for _, sessionID := range normalizedSessionIDs {
		gate := a.gateFor(sessionID)
		if err := gate.mu.Lock(ctx); err != nil {
			return nil, errors.Join(err, release.Close(context.Background()))
		}
		if gate.blocks == nil {
			gate.blocks = make(map[*sessionAdmissionBlock]struct{})
		}
		gate.blocks[release.block] = struct{}{}
		release.sessionIDs = append(release.sessionIDs, sessionID)
		gate.mu.Unlock()
	}
	return release, nil
}

func (a *Authority) TryBlockSessionStarts(ctx context.Context, sessionIDs []runtimeids.SessionID, reason SessionStartBlockReason) (SessionStartBlockRelease, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	normalizedSessionIDs, err := normalizeSessionStartBlockSessionIDs(sessionIDs)
	if err != nil {
		return nil, err
	}
	release, err := a.newSessionStartBlockRelease(reason)
	if err != nil {
		return nil, err
	}
	type lockedGate struct {
		sessionID runtimeids.SessionID
		gate      *sessionAdmissionGate
	}
	locked := make([]lockedGate, 0, len(normalizedSessionIDs))
	unlock := func() {
		for index := len(locked) - 1; index >= 0; index-- {
			locked[index].gate.mu.Unlock()
		}
	}
	for _, sessionID := range normalizedSessionIDs {
		gate := a.gateFor(sessionID)
		if !gate.mu.TryLock() {
			unlock()
			return nil, sessionStartAdmissionBusyError(sessionID)
		}
		locked = append(locked, lockedGate{sessionID: sessionID, gate: gate})
	}
	defer unlock()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	for _, entry := range locked {
		if len(entry.gate.blocks) != 0 {
			return nil, sessionStartAdmissionBusyError(entry.sessionID)
		}
	}
	for _, entry := range locked {
		if entry.gate.blocks == nil {
			entry.gate.blocks = make(map[*sessionAdmissionBlock]struct{})
		}
		entry.gate.blocks[release.block] = struct{}{}
		release.sessionIDs = append(release.sessionIDs, entry.sessionID)
	}
	return release, nil
}

func (a *Authority) newSessionStartBlockRelease(reason SessionStartBlockReason) (*sessionStartBlockRelease, error) {
	if reason == 0 {
		return nil, errors.New("session start block reason is required")
	}
	return &sessionStartBlockRelease{
		authority: a,
		block:     &sessionAdmissionBlock{reason: reason},
	}, nil
}

func normalizeSessionStartBlockSessionIDs(sessionIDs []runtimeids.SessionID) ([]runtimeids.SessionID, error) {
	if len(sessionIDs) == 0 {
		return nil, errors.New("session ids are required")
	}
	seen := make(map[runtimeids.SessionID]struct{}, len(sessionIDs))
	normalized := make([]runtimeids.SessionID, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID.IsZero() {
			return nil, errors.New("session id is required")
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		normalized = append(normalized, sessionID)
	}
	sort.Slice(normalized, func(i int, j int) bool {
		return normalized[i].String() < normalized[j].String()
	})
	return normalized, nil
}

func sessionStartAdmissionBusyError(sessionID runtimeids.SessionID) error {
	return errors.Join(ErrSessionStartAdmissionBusy, fmt.Errorf("session %s admission is busy", sessionID))
}

func (r *sessionStartBlockRelease) AuthorizeMaintenance(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.block == nil {
		return ctx
	}
	parent, _ := ctx.Value(sessionMaintenanceAuthorizationContextKey{}).(*sessionMaintenanceAuthorization)
	return context.WithValue(ctx, sessionMaintenanceAuthorizationContextKey{}, &sessionMaintenanceAuthorization{
		block:  r.block,
		parent: parent,
	})
}

func (r *sessionStartBlockRelease) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return nil
	}
	for len(r.sessionIDs) > 0 {
		index := len(r.sessionIDs) - 1
		sessionID := r.sessionIDs[index]
		gate := r.authority.gateFor(sessionID)
		if err := gate.mu.Lock(ctx); err != nil {
			return err
		}
		if _, exists := gate.blocks[r.block]; !exists {
			panic(fmt.Sprintf("session start block %d for session %s underflow", r.block.reason, sessionID))
		}
		delete(gate.blocks, r.block)
		gate.mu.Unlock()
		r.sessionIDs = r.sessionIDs[:index]
	}
	r.released = true
	return nil
}

func (g *sessionAdmissionGate) unauthorizedMaintenanceBlock(ctx context.Context) *sessionAdmissionBlock {
	for block := range g.blocks {
		if !maintenanceAuthorized(ctx, block) {
			return block
		}
	}
	return nil
}

func maintenanceAuthorized(ctx context.Context, block *sessionAdmissionBlock) bool {
	if ctx == nil || block == nil {
		return false
	}
	authorization, _ := ctx.Value(sessionMaintenanceAuthorizationContextKey{}).(*sessionMaintenanceAuthorization)
	for authorization != nil {
		if authorization.block == block {
			return true
		}
		authorization = authorization.parent
	}
	return false
}

func sessionStartsBlockedError(sessionID runtimeids.SessionID) error {
	return errors.Join(ErrSessionStartsBlocked, fmt.Errorf("session %s starts are blocked", sessionID))
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
	if err := gate.mu.Lock(ctx); err != nil {
		return err
	}
	defer gate.mu.Unlock()
	if err := context.Cause(ctx); err != nil {
		return err
	}
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

type DormantSessionStoreAdmission struct {
	RuntimeAvailable bool
}

func (a *Authority) WithDormantSessionStore(
	ctx context.Context,
	descriptor session.SessionDescriptor,
	callback func(context.Context, *session.Store) error,
) (DormantSessionStoreAdmission, error) {
	if a == nil {
		return DormantSessionStoreAdmission{}, errors.New("session runtime authority is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return DormantSessionStoreAdmission{}, err
	}
	sessionID := descriptor.SessionID()
	if sessionID.IsZero() {
		return DormantSessionStoreAdmission{}, errors.New("session id is required")
	}
	if callback == nil {
		return DormantSessionStoreAdmission{}, errors.New("session store callback is required")
	}
	gate := a.gateFor(sessionID)
	if err := gate.mu.Lock(ctx); err != nil {
		return DormantSessionStoreAdmission{}, err
	}
	defer gate.mu.Unlock()
	if len(gate.blocks) != 0 {
		return DormantSessionStoreAdmission{}, sessionStartsBlockedError(sessionID)
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return DormantSessionStoreAdmission{}, ErrAuthorityClosed
	}
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource != nil {
		return DormantSessionStoreAdmission{RuntimeAvailable: true}, nil
	}
	store, err := session.MaterializeSessionDescriptor(a.options.persistenceRoot, descriptor, a.options.storeOptions...)
	if err != nil {
		return DormantSessionStoreAdmission{}, err
	}
	if err := callback(ctx, store); err != nil {
		return DormantSessionStoreAdmission{}, err
	}
	return DormantSessionStoreAdmission{}, nil
}
