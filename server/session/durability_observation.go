package session

import "time"

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
