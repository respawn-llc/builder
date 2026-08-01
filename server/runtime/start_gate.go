package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrStartGateAborted = errors.New("runtime command start gate aborted")
	ErrStartGateSettled = errors.New("runtime command start gate is already settled")
)

type StartGate struct {
	mu    sync.Mutex
	state uint8
	done  chan struct{}
	err   error
}

func NewStartGate() *StartGate {
	return &StartGate{state: 1, done: make(chan struct{})}
}

func (g *StartGate) Wait(ctx context.Context) error {
	if g == nil {
		return ErrStartGateAborted
	}
	select {
	case <-g.done:
		g.mu.Lock()
		err := g.err
		g.mu.Unlock()
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (g *StartGate) Commit() error {
	if g == nil {
		return ErrStartGateSettled
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != 1 {
		return ErrStartGateSettled
	}
	g.state = 2
	close(g.done)
	return nil
}

func (g *StartGate) Abort(cause error) error {
	if g == nil {
		return ErrStartGateSettled
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != 1 {
		return ErrStartGateSettled
	}
	g.state = 3
	if cause == nil {
		g.err = ErrStartGateAborted
	} else {
		g.err = fmt.Errorf("%w: %v", ErrStartGateAborted, cause)
	}
	close(g.done)
	return nil
}
