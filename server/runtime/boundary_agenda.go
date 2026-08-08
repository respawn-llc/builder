package runtime

import "sync"

type boundaryAgenda struct {
	mu     sync.Mutex
	closed bool
}

func newBoundaryAgenda() *boundaryAgenda {
	return &boundaryAgenda{}
}

func (a *boundaryAgenda) close() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	a.closed = true
	return true
}

func (a *boundaryAgenda) isClosed() bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}
