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
	Events             <-chan ongoingTranscriptEvent
	RequestRehydration func()
	Stop               func()
}

type sessionTranscriptSubscriber func(context.Context, serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error)

func startSessionTranscriptEvents(ctx context.Context, sessionID string, subscribe sessionTranscriptSubscriber) ongoingTranscriptEventStream {
	out := make(chan ongoingTranscriptEvent, 64)
	requests := make(chan struct{}, 1)
	if subscribe == nil {
		close(out)
		return ongoingTranscriptEventStream{Events: out, RequestRehydration: func() {}, Stop: func() {}}
	}
	pollCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(out)
		for {
			sub, err := resubscribeSessionTranscript(pollCtx, sessionID, subscribe)
			if err != nil {
				return
			}
			reopen, stop := pumpSessionTranscriptSubscription(pollCtx, sub, out, requests)
			if stop {
				return
			}
			if !reopen {
				return
			}
		}
	}()
	requestRehydration := func() {
		select {
		case requests <- struct{}{}:
		default:
		}
	}
	return ongoingTranscriptEventStream{Events: out, RequestRehydration: requestRehydration, Stop: cancel}
}

type transcriptNextResult struct {
	message clientui.TranscriptMessage
	err     error
}

func pumpSessionTranscriptSubscription(ctx context.Context, sub serverapi.TranscriptSubscription, out chan<- ongoingTranscriptEvent, requests <-chan struct{}) (reopen bool, stop bool) {
	subClosed := false
	closeSub := func() {
		if subClosed {
			return
		}
		_ = sub.Close()
		subClosed = true
	}
	defer closeSub()
	for {
		nextCtx, cancel := context.WithCancel(ctx)
		next := make(chan transcriptNextResult, 1)
		go func() {
			message, err := sub.Next(nextCtx)
			next <- transcriptNextResult{message: message, err: err}
		}()
		select {
		case <-ctx.Done():
			cancel()
			return false, true
		case <-requests:
			cancel()
			return true, false
		case result := <-next:
			cancel()
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) && ctx.Err() != nil {
					return false, true
				}
				closeSub()
				emitSessionTranscriptLoss(ctx, out, result.err)
				return waitForTranscriptRehydrationRequest(ctx, requests)
			}
			select {
			case <-ctx.Done():
				return false, true
			case out <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, Message: result.message}:
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
		emitWait := waitSessionActivityRetry(ctx)
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
