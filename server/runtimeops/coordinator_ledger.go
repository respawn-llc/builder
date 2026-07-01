package runtimeops

import (
	"time"

	"core/shared/clientui"
)

func (l *sessionLedger) pruneLocked(limit int, ttl time.Duration, now time.Time) {
	if limit <= 0 {
		limit = defaultCoordinatorLimit
	}
	if ttl <= 0 {
		ttl = defaultCoordinatorTTL
	}
	retained := l.terminalOrder[:0]
	for _, key := range l.terminalOrder {
		completedAt, ok := l.terminal[key]
		if !ok {
			continue
		}
		if !completedAt.IsZero() && now.Sub(completedAt) >= ttl {
			record := l.records[key]
			delete(l.records, key)
			delete(l.terminal, key)
			if entry := l.operations[key]; entry != nil && entry.completed && (entry.successful || entry.committed) {
				delete(l.operations, key)
			}
			delete(l.failedReqs, key)
			l.recordEvictedLocked(key, record.OperationRef, now)
			continue
		}
		retained = append(retained, key)
	}
	l.terminalOrder = retained
	for len(l.terminalOrder) > limit {
		key := l.terminalOrder[0]
		l.terminalOrder = l.terminalOrder[1:]
		record := l.records[key]
		delete(l.records, key)
		delete(l.terminal, key)
		if entry := l.operations[key]; entry != nil && entry.completed && (entry.successful || entry.committed) {
			delete(l.operations, key)
		}
		delete(l.failedReqs, key)
		l.recordEvictedLocked(key, record.OperationRef, now)
	}
	for key, createdAt := range l.tombstoneAt {
		if !createdAt.IsZero() && now.Sub(createdAt) >= ttl {
			delete(l.tombstoneAt, key)
			delete(l.tombstones, key)
			delete(l.operations, key)
			delete(l.failedReqs, key)
			if record, ok := l.records[key]; ok {
				if _, terminal := l.terminal[key]; !terminal {
					delete(l.records, key)
					l.recordEvictedLocked(key, record.OperationRef, now)
				}
			}
		}
	}
	l.pruneEvictedLocked(limit, ttl, now)
}

func (l *sessionLedger) nonEvictableLocked(key string) bool {
	if _, ok := l.tombstones[key]; ok {
		return true
	}
	if entry := l.operations[key]; entry != nil && !entry.completed {
		return true
	}
	return false
}

func (l *sessionLedger) operationHasCommittedSideEffectLocked(key string) bool {
	if l == nil {
		return false
	}
	record, ok := l.records[key]
	if !ok {
		return false
	}
	switch record.State {
	case clientui.RuntimeInputReconciliationCommitted, clientui.RuntimeInputReconciliationSubmitted:
		return true
	default:
		return false
	}
}

func (l *sessionLedger) markTerminalEvictableLocked(key string, now time.Time) {
	if _, tombstone := l.tombstones[key]; tombstone {
		return
	}
	record, ok := l.records[key]
	if !ok || record.State == clientui.RuntimeInputReconciliationAccepted {
		return
	}
	if _, exists := l.terminal[key]; !exists {
		l.terminalOrder = append(l.terminalOrder, key)
	}
	l.terminal[key] = now
}

func (l *sessionLedger) recordEvictedLocked(key string, ref clientui.RuntimeOperationRef, now time.Time) {
	if _, exists := l.evicted[key]; !exists {
		l.evictedOrder = append(l.evictedOrder, key)
	}
	l.evicted[key] = ref
	l.evictedAt[key] = now
}

func (l *sessionLedger) pruneEvictedLocked(limit int, ttl time.Duration, now time.Time) {
	retained := l.evictedOrder[:0]
	for _, key := range l.evictedOrder {
		evictedAt, ok := l.evictedAt[key]
		if !ok {
			continue
		}
		if !evictedAt.IsZero() && now.Sub(evictedAt) >= ttl {
			delete(l.evictedAt, key)
			delete(l.evicted, key)
			continue
		}
		retained = append(retained, key)
	}
	l.evictedOrder = retained
	for len(l.evictedOrder) > limit {
		key := l.evictedOrder[0]
		l.evictedOrder = l.evictedOrder[1:]
		delete(l.evictedAt, key)
		delete(l.evicted, key)
	}
}
