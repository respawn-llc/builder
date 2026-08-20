package registry

import (
	"context"
	"strings"

	"core/server/session"
)

type GlobalPersistenceSessionResolver struct {
	persistenceRoot string
	storeOptions    []session.StoreOption
	persisted       session.PersistedSessionResolver
}

func NewGlobalPersistenceSessionResolver(persistenceRoot string, persisted session.PersistedSessionResolver, storeOptions ...session.StoreOption) GlobalPersistenceSessionResolver {
	return GlobalPersistenceSessionResolver{persistenceRoot: strings.TrimSpace(persistenceRoot), persisted: persisted, storeOptions: append([]session.StoreOption(nil), storeOptions...)}
}

func (r GlobalPersistenceSessionResolver) ResolvePersistedSession(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	return r.persisted.ResolvePersistedSession(ctx, sessionID)
}

func (r GlobalPersistenceSessionResolver) ResolveSessionStore(_ context.Context, sessionID string) (*session.Store, error) {
	return session.OpenByID(r.persistenceRoot, sessionID, r.storeOptions...)
}
