package session

import (
	"context"
)

type PersistedStoreSnapshot struct {
	SessionDir string
	Meta       Meta
}

type PersistenceObserver interface {
	ObservePersistedStore(ctx context.Context, snapshot PersistedStoreSnapshot) error
}
