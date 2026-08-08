package runtime

import "sync"

type boundaryAgenda struct {
	mu     sync.Mutex
	closed bool
}

func newBoundaryAgenda() *boundaryAgenda {
	return &boundaryAgenda{}
}

func (a *boundaryAgenda) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
}
