package session

import (
	"context"
	"time"

	"core/shared/runtimeids"
)

type EventLogAppendObservation struct {
	RecordCount int
	Latency     time.Duration
	Succeeded   bool
}

type EventLogSyncObservation struct {
	Latency   time.Duration
	Succeeded bool
}

type DurabilityObserver interface {
	ObserveEventLogAppend(EventLogAppendObservation)
	ObserveEventLogSync(EventLogSyncObservation)
}

type AppendProjection struct {
	SessionID                       runtimeids.SessionID
	FirstSequence                   int64
	LastSequence                    int64
	AppendedAt                      time.Time
	FirstPromptPreview              *string
	ConversationEstablished         bool
	GeneratedRecoveredWarningIssued bool
	activeWorkflowAssignment        *MessageRecord
	activeWorkflowAssignmentState   *ActiveWorkflowAssignmentState
}

type AppendProjector func(context.Context, AppendProjection) error

type StoreOption func(*storeOptions)

type storeOptions struct {
	observer           PersistenceObserver
	resolver           PersistedSessionResolver
	contextFactWriter  SessionContextFactWriter
	durabilityObserver DurabilityObserver
	appendProjector    AppendProjector
	now                func() time.Time
}

func WithPersistenceObserver(observer PersistenceObserver) StoreOption {
	return func(options *storeOptions) {
		options.observer = observer
	}
}

func WithPersistedSessionResolver(resolver PersistedSessionResolver) StoreOption {
	return func(options *storeOptions) {
		options.resolver = resolver
	}
}

func WithSessionContextFactWriter(writer SessionContextFactWriter) StoreOption {
	return func(options *storeOptions) {
		options.contextFactWriter = writer
	}
}

func WithDurabilityObserver(observer DurabilityObserver) StoreOption {
	return func(options *storeOptions) {
		options.durabilityObserver = observer
	}
}

func WithAppendProjector(projector AppendProjector) StoreOption {
	return func(options *storeOptions) {
		options.appendProjector = projector
	}
}

func WithClock(now func() time.Time) StoreOption {
	return func(options *storeOptions) {
		options.now = now
	}
}

func normalizeStoreOptions(options ...StoreOption) storeOptions {
	result := storeOptions{}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(&result)
	}
	if result.now == nil {
		result.now = func() time.Time {
			return time.Now().UTC()
		}
	}
	return result
}
