package testsetup

import (
	"context"
	"sync"
)

// StartBarrier provides deterministic synchronization for tests that need to
// hold an admission callback in progress.
type StartBarrier struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func NewStartBarrier() *StartBarrier {
	return &StartBarrier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *StartBarrier) Entered() <-chan struct{} {
	return b.entered
}

func (b *StartBarrier) ArriveAndWait(ctx context.Context) error {
	b.enteredOnce.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (b *StartBarrier) Unblock() {
	b.releaseOnce.Do(func() { close(b.release) })
}

type StartResult[T any] struct {
	Value T
	Err   error
}

func Start[T any](start func() (T, error)) <-chan StartResult[T] {
	started := make(chan StartResult[T], 1)
	go func() {
		value, err := start()
		started <- StartResult[T]{Value: value, Err: err}
	}()
	return started
}
