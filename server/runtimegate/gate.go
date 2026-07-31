package runtimegate

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrAborted = errors.New("runtime command start gate aborted")
	ErrSettled = errors.New("runtime start gate is already settled")
)

type state uint8

const (
	waiting state = iota + 1
	committed
	aborted
)

type Gate struct {
	mu    sync.Mutex
	state state
	done  chan struct{}
	err   error
}

func New() *Gate {
	return &Gate{state: waiting, done: make(chan struct{})}
}

func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return ErrAborted
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

func (g *Gate) Commit() error {
	if g == nil {
		return ErrSettled
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != waiting {
		return ErrSettled
	}
	g.state = committed
	close(g.done)
	return nil
}

func (g *Gate) Abort(cause error) error {
	if g == nil {
		return ErrSettled
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != waiting {
		return ErrSettled
	}
	g.state = aborted
	if cause == nil {
		g.err = ErrAborted
	} else {
		g.err = fmt.Errorf("%w: %v", ErrAborted, cause)
	}
	close(g.done)
	return nil
}
