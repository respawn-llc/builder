package session

import (
	"context"
	"time"
)

type PersistedStoreSnapshot struct {
	SessionDir string
	Meta       Meta
}

type PersistenceObserver interface {
	ObservePersistedStore(ctx context.Context, snapshot PersistedStoreSnapshot) error
}

type PersistedEventLogReconciliation struct {
	SessionID               string
	LastSequence            int64
	ConversationEstablished bool
	UpdatedAt               time.Time
}

type EventLogReconciliationObserver interface {
	ObserveEventLogReconciliation(ctx context.Context, reconciliation PersistedEventLogReconciliation) error
}
