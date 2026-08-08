package app

import (
	"context"
	"errors"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/serverapi"
)

const transcriptSubscriptionRetryDelay = 250 * time.Millisecond

type ongoingTranscriptEventKind string

const (
	ongoingTranscriptEventMessage ongoingTranscriptEventKind = "message"
	ongoingTranscriptEventLoss    ongoingTranscriptEventKind = "loss"
	ongoingTranscriptEventFailure ongoingTranscriptEventKind = "failure"
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
type sessionTranscriptReactivator func(context.Context) error

func startSessionTranscriptEvents(
	ctx context.Context,
	sessionID string,
	subscribe sessionTranscriptSubscriber,
	reactivate sessionTranscriptReactivator,
	observers ...func(clientui.TranscriptMessage),
) ongoingTranscriptEventStream {
	out := make(chan ongoingTranscriptEvent, 64)
	requests := make(chan struct{}, 1)
	if subscribe == nil {
		close(out)
		return ongoingTranscriptEventStream{Events: out, RequestRehydration: func() {}, Stop: func() {}}
	}
	pollCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(out)
		connectionFailureReported := false
		reactivationRequired := false
		for {
			if reactivationRequired && reactivate != nil {
				if err := reactivate(pollCtx); err != nil {
					if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && pollCtx.Err() != nil {
						return
					}
					if runtimeattach.IsRuntimeTimeoutError(err) {
						if waitForTranscriptSubscriptionRetry(pollCtx) {
							continue
						}
						return
					}
					if runtimeattach.IsRuntimeConnectionError(err) {
						if !connectionFailureReported {
							emitSessionTranscriptFailure(pollCtx, out, err)
							connectionFailureReported = true
						}
						if waitForTranscriptSubscriptionRetry(pollCtx) {
							continue
						}
						return
					}
					emitSessionTranscriptFailure(pollCtx, out, err)
					return
				}
				reactivationRequired = false
			}
			sub, err := subscribeSessionTranscript(pollCtx, sessionID, subscribe)
			if err != nil {
				if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && pollCtx.Err() != nil {
					return
				}
				if runtimeattach.IsRuntimeConnectionError(err) {
					reactivationRequired = true
					if !connectionFailureReported {
						emitSessionTranscriptFailure(pollCtx, out, err)
						connectionFailureReported = true
					}
					if waitForTranscriptSubscriptionRetry(pollCtx) {
						continue
					}
					return
				}
				emitSessionTranscriptFailure(pollCtx, out, err)
				return
			}
			connectionFailureReported = false
			reopen, stop, lossErr := pumpSessionTranscriptSubscription(pollCtx, sub, out, requests, observers...)
			if stop {
				return
			}
			if !reopen {
				return
			}
			reactivationRequired = runtimeattach.IsRuntimeConnectionError(lossErr)
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

func pumpSessionTranscriptSubscription(
	ctx context.Context,
	sub serverapi.TranscriptSubscription,
	out chan<- ongoingTranscriptEvent,
	requests <-chan struct{},
	observers ...func(clientui.TranscriptMessage),
) (reopen bool, stop bool, lossErr error) {
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
			return false, true, nil
		case <-requests:
			cancel()
			return true, false, nil
		case result := <-next:
			cancel()
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) && ctx.Err() != nil {
					return false, true, nil
				}
				closeSub()
				emitSessionTranscriptLoss(ctx, out, result.err)
				reopen, stop := waitForTranscriptRehydrationRequest(ctx, requests)
				return reopen, stop, result.err
			}
			for _, observe := range observers {
				if observe != nil {
					observe(result.message)
				}
			}
			select {
			case <-ctx.Done():
				return false, true, nil
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

func subscribeSessionTranscript(ctx context.Context, sessionID string, subscribe sessionTranscriptSubscriber) (serverapi.TranscriptSubscription, error) {
	return subscribe(ctx, serverapi.TranscriptSubscribeRequest{SessionID: sessionID})
}

func waitForTranscriptSubscriptionRetry(ctx context.Context) bool {
	timer := time.NewTimer(transcriptSubscriptionRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func emitSessionTranscriptLoss(ctx context.Context, out chan<- ongoingTranscriptEvent, err error) {
	select {
	case <-ctx.Done():
	case out <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss, Err: err}:
	}
}

func emitSessionTranscriptFailure(ctx context.Context, out chan<- ongoingTranscriptEvent, err error) {
	select {
	case <-ctx.Done():
	case out <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventFailure, Err: err}:
	}
}
