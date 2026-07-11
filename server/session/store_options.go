package session

import (
	"strings"
	"time"
)

type EventLogFSyncPolicy string

const (
	EventLogFSyncNever    EventLogFSyncPolicy = "never"
	EventLogFSyncAlways   EventLogFSyncPolicy = "always"
	EventLogFSyncPeriodic EventLogFSyncPolicy = "periodic"
)

const (
	defaultEventLogFSyncPolicy         = EventLogFSyncPeriodic
	defaultEventLogFSyncIntervalWrites = 16
	defaultPersistenceObserverTimeout  = 2 * time.Second
)

type StoreOption func(*storeOptions)

type storeOptions struct {
	eventLog        eventLogOptions
	observer        PersistenceObserver
	resolver        PersistedSessionResolver
	filelessMeta    bool
	filelessEvents  bool
	observerTimeout time.Duration
	now             func() time.Time
}

type eventLogOptions struct {
	fsyncPolicy         EventLogFSyncPolicy
	fsyncIntervalWrites int
}

func WithEventLogFSyncPolicy(policy EventLogFSyncPolicy) StoreOption {
	return func(options *storeOptions) {
		options.eventLog.fsyncPolicy = policy
	}
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

func WithFilelessMetadataPersistence() StoreOption {
	return func(options *storeOptions) {
		options.filelessMeta = true
	}
}

// WithFilelessEventPersistence applies events and their metadata transitions to
// the opened store's in-memory state without writing events.jsonl or
// session.json. It is for read-only diagnostic execution that must exercise
// normal event-driven state transitions.
func WithFilelessEventPersistence() StoreOption {
	return func(options *storeOptions) {
		options.filelessEvents = true
		options.filelessMeta = true
	}
}

func WithClock(now func() time.Time) StoreOption {
	return func(options *storeOptions) {
		options.now = now
	}
}

func normalizeStoreOptions(options ...StoreOption) storeOptions {
	result := storeOptions{
		eventLog: eventLogOptions{
			fsyncPolicy:         defaultEventLogFSyncPolicy,
			fsyncIntervalWrites: defaultEventLogFSyncIntervalWrites,
		},
		observerTimeout: defaultPersistenceObserverTimeout,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(&result)
	}
	result.eventLog = normalizeEventLogOptions(result.eventLog)
	if result.observerTimeout <= 0 {
		result.observerTimeout = defaultPersistenceObserverTimeout
	}
	if result.now == nil {
		result.now = func() time.Time {
			return time.Now().UTC()
		}
	}
	return result
}

func normalizeEventLogOptions(options eventLogOptions) eventLogOptions {
	switch EventLogFSyncPolicy(strings.ToLower(strings.TrimSpace(string(options.fsyncPolicy)))) {
	case EventLogFSyncNever:
		options.fsyncPolicy = EventLogFSyncNever
	case EventLogFSyncAlways:
		options.fsyncPolicy = EventLogFSyncAlways
	case EventLogFSyncPeriodic:
		options.fsyncPolicy = EventLogFSyncPeriodic
	default:
		options.fsyncPolicy = defaultEventLogFSyncPolicy
	}
	if options.fsyncIntervalWrites <= 0 {
		options.fsyncIntervalWrites = defaultEventLogFSyncIntervalWrites
	}
	return options
}
