package app

import (
	"context"
	"sync"
)

type closeableSubscription interface {
	Close() error
}

type ownedSubscription[S closeableSubscription] struct {
	subscription S
	closeOnce    sync.Once
	closeErr     error
}

func newOwnedSubscription[S closeableSubscription](subscription S) *ownedSubscription[S] {
	return &ownedSubscription[S]{subscription: subscription}
}

func (s *ownedSubscription[S]) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.subscription.Close()
	})
	return s.closeErr
}

type joinedSubscriptionOwner[S closeableSubscription] struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	currentMu sync.Mutex
	current   *ownedSubscription[S]
}

func newJoinedSubscriptionOwner[S closeableSubscription](parent context.Context) *joinedSubscriptionOwner[S] {
	ctx, cancel := context.WithCancel(parent)
	return &joinedSubscriptionOwner[S]{
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

func (o *joinedSubscriptionOwner[S]) install(subscription *ownedSubscription[S]) bool {
	o.currentMu.Lock()
	defer o.currentMu.Unlock()
	if o.ctx.Err() != nil {
		return false
	}
	o.current = subscription
	return true
}

func (o *joinedSubscriptionOwner[S]) clear(subscription *ownedSubscription[S]) {
	o.currentMu.Lock()
	if o.current == subscription {
		o.current = nil
	}
	o.currentMu.Unlock()
}

func (o *joinedSubscriptionOwner[S]) finish() {
	close(o.done)
}

func (o *joinedSubscriptionOwner[S]) Close() {
	if o == nil {
		return
	}
	o.closeOnce.Do(func() {
		o.cancel()
		o.currentMu.Lock()
		current := o.current
		o.currentMu.Unlock()
		if current != nil {
			_ = current.Close()
		}
		<-o.done
	})
}

type subscriptionNextResult[T any] struct {
	value T
	err   error
}

func beginSubscriptionNext[T any](ctx context.Context, next func(context.Context) (T, error)) <-chan subscriptionNextResult[T] {
	result := make(chan subscriptionNextResult[T], 1)
	go func() {
		value, err := next(ctx)
		result <- subscriptionNextResult[T]{value: value, err: err}
	}()
	return result
}
