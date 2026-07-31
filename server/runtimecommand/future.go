package runtimecommand

import "context"

type futureResult[T any] struct {
	value T
	err   error
}

type Future[T any] struct {
	done <-chan futureResult[T]
}

func (f Future[T]) Await(ctx context.Context) (T, error) {
	select {
	case result := <-f.done:
		return result.value, result.err
	case <-ctx.Done():
		var zero T
		return zero, context.Cause(ctx)
	}
}

func (f Future[T]) Done() <-chan futureResult[T] {
	return f.done
}
