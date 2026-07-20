package session

import (
	"fmt"
	"sync"
)

const (
	migrationSourceBufferBytes       = 64 << 10
	migrationCopyBufferBytes         = 64 << 10
	migrationInlineValueBudgetBytes  = 8 << 20
	migrationEncoderMergeBudgetBytes = 16 * migrationCopyBufferBytes
	migrationMaxOpenSpoolFiles       = 16
	migrationMaxJSONNesting          = 10_000
)

type migrationResourceSnapshot struct {
	LiveInlineBytes       int64
	MaxLiveInlineBytes    int64
	SourceDecoderBytes    int64
	MaxSourceDecoderBytes int64
	EncoderMergeBytes     int64
	MaxEncoderMergeBytes  int64
	OpenSpoolFiles        int
	MaxOpenSpoolFiles     int
	CurrentSpoolBytes     int64
	PeakSpoolBytes        int64
}

type migrationResourceLedger struct {
	mu       sync.Mutex
	current  migrationResourceSnapshot
	maximums migrationResourceSnapshot
}

func newMigrationResourceLedger() *migrationResourceLedger {
	return &migrationResourceLedger{}
}

func (l *migrationResourceLedger) acquireSourceDecoder(size int64) (func(), error) {
	if l == nil {
		return nil, fmt.Errorf("migration resource ledger is required")
	}
	if size <= 0 || size > migrationSourceBufferBytes {
		return nil, fmt.Errorf(
			"migration source decoder lease must be positive and at most %d, got %d",
			migrationSourceBufferBytes,
			size,
		)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current.SourceDecoderBytes != 0 {
		return nil, fmt.Errorf("migration source decoder lease is already held")
	}
	l.current.SourceDecoderBytes = size
	if size > l.maximums.MaxSourceDecoderBytes {
		l.maximums.MaxSourceDecoderBytes = size
	}
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		l.current.SourceDecoderBytes -= size
	}, nil
}

func (l *migrationResourceLedger) tryAcquireInline(
	size int64,
	budget int64,
) (func(), bool, error) {
	if l == nil {
		return nil, false, fmt.Errorf("migration resource ledger is required")
	}
	if size < 0 {
		return nil, false, fmt.Errorf("migration inline lease size must not be negative: %d", size)
	}
	if budget < 0 || budget > migrationInlineValueBudgetBytes {
		return nil, false, fmt.Errorf(
			"migration inline budget must be within [0,%d], got %d",
			migrationInlineValueBudgetBytes,
			budget,
		)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if size > budget-l.current.LiveInlineBytes {
		return nil, false, nil
	}
	l.current.LiveInlineBytes += size
	if l.current.LiveInlineBytes > l.maximums.MaxLiveInlineBytes {
		l.maximums.MaxLiveInlineBytes = l.current.LiveInlineBytes
	}
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		l.current.LiveInlineBytes -= size
	}, true, nil
}

func (l *migrationResourceLedger) acquireEncoderMerge(size int64) (func(), error) {
	if l == nil {
		return nil, fmt.Errorf("migration resource ledger is required")
	}
	if size <= 0 {
		return nil, fmt.Errorf("migration encoder lease must be positive: %d", size)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if size > migrationEncoderMergeBudgetBytes-l.current.EncoderMergeBytes {
		return nil, fmt.Errorf(
			"migration encoder/merge budget exceeded: current=%d requested=%d limit=%d",
			l.current.EncoderMergeBytes,
			size,
			migrationEncoderMergeBudgetBytes,
		)
	}
	l.current.EncoderMergeBytes += size
	if l.current.EncoderMergeBytes > l.maximums.MaxEncoderMergeBytes {
		l.maximums.MaxEncoderMergeBytes = l.current.EncoderMergeBytes
	}
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		l.current.EncoderMergeBytes -= size
	}, nil
}

func (l *migrationResourceLedger) acquireSpoolFile() (func(), error) {
	if l == nil {
		return nil, fmt.Errorf("migration resource ledger is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current.OpenSpoolFiles >= migrationMaxOpenSpoolFiles {
		return nil, fmt.Errorf(
			"migration spool file budget exceeded: current=%d limit=%d",
			l.current.OpenSpoolFiles,
			migrationMaxOpenSpoolFiles,
		)
	}
	l.current.OpenSpoolFiles++
	if l.current.OpenSpoolFiles > l.maximums.MaxOpenSpoolFiles {
		l.maximums.MaxOpenSpoolFiles = l.current.OpenSpoolFiles
	}
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		l.current.OpenSpoolFiles--
	}, nil
}

func (l *migrationResourceLedger) spoolGrew(size int64) error {
	if size < 0 {
		return fmt.Errorf("migration spool growth must not be negative: %d", size)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.current.CurrentSpoolBytes += size
	if l.current.CurrentSpoolBytes > l.maximums.PeakSpoolBytes {
		l.maximums.PeakSpoolBytes = l.current.CurrentSpoolBytes
	}
	return nil
}

func (l *migrationResourceLedger) spoolRemoved(size int64) error {
	if size < 0 {
		return fmt.Errorf("migration spool removal must not be negative: %d", size)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if size > l.current.CurrentSpoolBytes {
		return fmt.Errorf(
			"migration spool byte count underflow: current=%d removed=%d",
			l.current.CurrentSpoolBytes,
			size,
		)
	}
	l.current.CurrentSpoolBytes -= size
	return nil
}

func (l *migrationResourceLedger) snapshot() migrationResourceSnapshot {
	if l == nil {
		return migrationResourceSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.current
	out.MaxLiveInlineBytes = l.maximums.MaxLiveInlineBytes
	out.MaxSourceDecoderBytes = l.maximums.MaxSourceDecoderBytes
	out.MaxEncoderMergeBytes = l.maximums.MaxEncoderMergeBytes
	out.MaxOpenSpoolFiles = l.maximums.MaxOpenSpoolFiles
	out.PeakSpoolBytes = l.maximums.PeakSpoolBytes
	return out
}
