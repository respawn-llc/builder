package session

import (
	"time"
)

const defaultPersistenceObserverTimeout = 2 * time.Second

type StoreOption func(*storeOptions)

type storeOptions struct {
	observer           PersistenceObserver
	reconciler         EventLogReconciliationObserver
	resolver           PersistedSessionResolver
	durabilityObserver DurabilityObserver
	observerTimeout    time.Duration
	now                func() time.Time
}

func WithPersistenceObserver(observer PersistenceObserver) StoreOption {
	return func(options *storeOptions) {
		options.observer = observer
		if reconciler, ok := observer.(EventLogReconciliationObserver); ok {
			options.reconciler = reconciler
		}
	}
}

func WithPersistedSessionResolver(resolver PersistedSessionResolver) StoreOption {
	return func(options *storeOptions) {
		options.resolver = resolver
	}
}

func WithDurabilityObserver(observer DurabilityObserver) StoreOption {
	return func(options *storeOptions) {
		options.durabilityObserver = observer
	}
}

func WithClock(now func() time.Time) StoreOption {
	return func(options *storeOptions) {
		options.now = now
	}
}

func normalizeStoreOptions(options ...StoreOption) storeOptions {
	result := storeOptions{
		observerTimeout: defaultPersistenceObserverTimeout,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(&result)
	}
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
