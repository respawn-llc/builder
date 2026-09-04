package workflowexecution

import (
	"context"
	"sync"
)

type completionSignal[T any] struct {
	once  sync.Once
	done  chan struct{}
	value T
	err   error
}

func newCompletionSignal[T any]() completionSignal[T] {
	return completionSignal[T]{done: make(chan struct{})}
}

func (s *completionSignal[T]) resolve(value T, err error) {
	s.once.Do(func() {
		s.value = value
		s.err = err
		close(s.done)
	})
}

func (s *completionSignal[T]) wait(ctx context.Context) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		return s.value, s.err
	default:
	}
	select {
	case <-s.done:
		return s.value, s.err
	case <-ctx.Done():
		select {
		case <-s.done:
			return s.value, s.err
		default:
			return zero, context.Cause(ctx)
		}
	}
}
