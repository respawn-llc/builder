package runtimecommand

import "context"

type futureResult[T any] struct {
	value T
	err   error
}

type Future[T any] struct {
	done   <-chan struct{}
	result *futureResult[T]
}

func (f Future[T]) Await(ctx context.Context) (T, error) {
	select {
	case <-f.done:
		if f.result == nil {
			var zero T
			return zero, nil
		}
		return f.result.value, f.result.err
	case <-ctx.Done():
		var zero T
		return zero, context.Cause(ctx)
	}
}

func (f Future[T]) Done() <-chan struct{} {
	return f.done
}
