package runtimecommand

import "context"

type OrderedAcceptance[T any] struct {
	Target  Target
	Payload T
}

func EnqueueAcceptance[T any, R any](
	ctx context.Context,
	authority *Authority,
	acceptance OrderedAcceptance[T],
	apply func(Turn, T) (R, error),
) (Future[R], error) {
	if err := acceptance.Target.Validate(); err != nil {
		return Future[R]{}, err
	}
	if apply == nil {
		return Future[R]{}, ErrCommandHandlerNeeded
	}
	return Enqueue(ctx, authority, acceptance.Target, func(turn Turn) (R, error) {
		return apply(turn, acceptance.Payload)
	})
}
