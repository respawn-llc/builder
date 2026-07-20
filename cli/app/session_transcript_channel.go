package app

import (
	"context"
	"errors"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type ongoingTranscriptEventKind string

const (
	ongoingTranscriptEventMessage ongoingTranscriptEventKind = "message"
	ongoingTranscriptEventLoss    ongoingTranscriptEventKind = "loss"
)

type ongoingTranscriptEvent struct {
	Kind    ongoingTranscriptEventKind
	Message clientui.TranscriptMessage
	Err     error
}

type ongoingTranscriptEventStream struct {
	Events   <-chan ongoingTranscriptEvent
	requests chan struct{}
	owner    *joinedSubscriptionOwner[serverapi.TranscriptSubscription]
}

type sessionTranscriptSubscriber func(context.Context, serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error)

func startSessionTranscriptEvents(ctx context.Context, sessionID string, subscribe sessionTranscriptSubscriber) *ongoingTranscriptEventStream {
	out := make(chan ongoingTranscriptEvent, 64)
	requests := make(chan struct{}, 1)
	owner := newJoinedSubscriptionOwner[serverapi.TranscriptSubscription](ctx)
	stream := &ongoingTranscriptEventStream{
		Events:   out,
		requests: requests,
		owner:    owner,
	}
	if subscribe == nil {
		close(out)
		owner.finish()
		return stream
	}
	go func() {
		defer owner.finish()
		defer close(out)
		for {
			sub, err := resubscribeSessionTranscript(owner.ctx, sessionID, subscribe)
			if err != nil {
				return
			}
			owned := newOwnedSubscription(sub)
			if !owner.install(owned) {
				_ = owned.Close()
				return
			}
			reopen, stop := pumpSessionTranscriptSubscription(owner.ctx, owned, out, requests)
			owner.clear(owned)
			if stop {
				return
			}
			if !reopen {
				return
			}
		}
	}()
	return stream
}

func (s *ongoingTranscriptEventStream) RequestRehydration() {
	if s == nil || s.requests == nil {
		return
	}
	select {
	case s.requests <- struct{}{}:
	default:
	}
}

func (s *ongoingTranscriptEventStream) Close() {
	if s == nil {
		return
	}
	s.owner.Close()
}

func pumpSessionTranscriptSubscription(ctx context.Context, sub *ownedSubscription[serverapi.TranscriptSubscription], out chan<- ongoingTranscriptEvent, requests <-chan struct{}) (reopen bool, stop bool) {
	defer func() { _ = sub.Close() }()
	for {
		nextCtx, cancel := context.WithCancel(ctx)
		next := beginSubscriptionNext(nextCtx, sub.subscription.Next)
		select {
		case <-ctx.Done():
			cancel()
			_ = sub.Close()
			<-next
			return false, true
		case <-requests:
			cancel()
			_ = sub.Close()
			<-next
			return true, false
		case result := <-next:
			cancel()
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) && ctx.Err() != nil {
					return false, true
				}
				_ = sub.Close()
				emitSessionTranscriptLoss(ctx, out, result.err)
				return waitForTranscriptRehydrationRequest(ctx, requests)
			}
			select {
			case <-ctx.Done():
				return false, true
			case out <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, Message: result.value}:
			}
		}
	}
}

func waitForTranscriptRehydrationRequest(ctx context.Context, requests <-chan struct{}) (reopen bool, stop bool) {
	select {
	case <-ctx.Done():
		return false, true
	case <-requests:
		return true, false
	}
}

func resubscribeSessionTranscript(ctx context.Context, sessionID string, subscribe sessionTranscriptSubscriber) (serverapi.TranscriptSubscription, error) {
	for {
		sub, err := subscribe(ctx, serverapi.TranscriptSubscribeRequest{SessionID: sessionID})
		if err == nil {
			return sub, nil
		}
		if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() != nil {
			return nil, err
		}
		emitWait := waitSubscriptionRetry(ctx)
		if !emitWait {
			return nil, ctx.Err()
		}
	}
}

func emitSessionTranscriptLoss(ctx context.Context, out chan<- ongoingTranscriptEvent, err error) {
	select {
	case <-ctx.Done():
	case out <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss, Err: err}:
	}
}
