package core

import (
	"sync"

	"core/server/metadata"
)

type MetadataFatalAuthority struct {
	once   sync.Once
	mu     sync.RWMutex
	first  *metadata.ClassifiedFailure
	signal chan struct{}
}

func NewMetadataFatalAuthority() *MetadataFatalAuthority {
	return &MetadataFatalAuthority{signal: make(chan struct{})}
}

func (a *MetadataFatalAuthority) ReportMetadataFatal(failure *metadata.ClassifiedFailure) bool {
	if a == nil || failure == nil || failure.Class != metadata.FailureCritical {
		return false
	}
	accepted := false
	a.once.Do(func() {
		a.mu.Lock()
		a.first = failure
		a.mu.Unlock()
		close(a.signal)
		accepted = true
	})
	return accepted
}

func (a *MetadataFatalAuthority) MetadataFatal() *metadata.ClassifiedFailure {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.first
}

func (a *MetadataFatalAuthority) Done() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.signal
}
