package runtimecommand

import (
	"context"
	"errors"
)

type PendingWorkAppender[T any] interface {
	AppendPendingWork(Turn, T) error
}

func EnqueuePendingWorkAcceptance[T any](
	ctx context.Context,
	authority *Authority,
	acceptance OrderedAcceptance[T],
	appender PendingWorkAppender[T],
) (Future[struct{}], error) {
	if appender == nil {
		return Future[struct{}]{}, errors.New("runtime command pending work appender is required")
	}
	return EnqueueAcceptance(ctx, authority, acceptance, func(turn Turn, payload T) (struct{}, error) {
		return struct{}{}, appender.AppendPendingWork(turn, payload)
	})
}
