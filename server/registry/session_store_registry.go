package registry

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"core/server/session"
)

type SessionStoreRegistry struct {
	mu     sync.RWMutex
	stores map[string]*session.Store
}

func NewSessionStoreRegistry() *SessionStoreRegistry {
	return &SessionStoreRegistry{stores: make(map[string]*session.Store)}
}

func (r *SessionStoreRegistry) RegisterStore(store *session.Store) {
	if r == nil || store == nil {
		return
	}
	sessionID := strings.TrimSpace(store.Meta().SessionID)
	if sessionID == "" {
		return
	}
	r.mu.Lock()
	r.stores[sessionID] = store
	r.mu.Unlock()
}

func (r *SessionStoreRegistry) UnregisterStore(sessionID string) {
	if r == nil {
		return
	}
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return
	}
	r.mu.Lock()
	delete(r.stores, trimmed)
	r.mu.Unlock()
}

func (r *SessionStoreRegistry) ResolveStore(_ context.Context, sessionID string) (*session.Store, error) {
	if r == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return nil, fmt.Errorf("session id is required")
	}
	r.mu.RLock()
	store := r.stores[trimmed]
	r.mu.RUnlock()
	if store == nil {
		return nil, nil
	}
	return store, nil
}
