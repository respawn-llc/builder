package app

import (
	"context"
	"errors"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/transcriptdiag"
)

var sessionActivityResubscribeDelay = 250 * time.Millisecond

type sessionActivitySubscriber func(context.Context, uint64) (serverapi.SessionActivitySubscription, error)

func startSessionActivityEvents(ctx context.Context, sub serverapi.SessionActivitySubscription, subscribe sessionActivitySubscriber, diagnosticsEnabled func() bool, logDiag func(string)) (<-chan clientui.Event, func()) {
	out := make(chan clientui.Event, 64)
	if sub == nil || subscribe == nil {
		close(out)
		return out, func() {}
	}
	pollCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(out)
		current := sub
		var lastSequence uint64
		for {
			evt, err := current.Next(pollCtx)
			if err != nil {
				if sessionActivityDiagnosticsEnabled(diagnosticsEnabled) && logDiag != nil {
					logDiag(transcriptdiag.FormatLine("transcript.diag.client.activity_gap", map[string]string{
						"path": "recovery",
						"err":  err.Error(),
					}))
				}
				_ = current.Close()
				if errors.Is(err, context.Canceled) && pollCtx.Err() != nil {
					return
				}
				current, err = resubscribeSessionActivity(pollCtx, subscribe, lastSequence)
				if err != nil {
					if errors.Is(err, serverapi.ErrStreamGap) {
						emitSessionActivityGap(pollCtx, out)
						lastSequence = 0
						current, err = resubscribeSessionActivity(pollCtx, subscribe, lastSequence)
						if err == nil {
							continue
						}
					}
					return
				}
				if lastSequence == 0 {
					emitSessionActivityGap(pollCtx, out)
				}
				continue
			}
			if evt.Sequence > lastSequence {
				lastSequence = evt.Sequence
			}
			if sessionActivityDiagnosticsEnabled(diagnosticsEnabled) && logDiag != nil {
				fields := map[string]string{
					"path":         "live_event",
					"kind":         string(evt.Kind),
					"step_id":      evt.StepID,
					"event_digest": transcriptdiag.EventDigest(evt),
				}
				logDiag(transcriptdiag.FormatLine("transcript.diag.client.recv_activity", fields))
			}
			select {
			case <-pollCtx.Done():
				_ = current.Close()
				return
			case out <- evt:
			}
		}
	}()
	return out, cancel
}

func emitSessionActivityGap(ctx context.Context, out chan<- clientui.Event) {
	select {
	case <-ctx.Done():
	case out <- clientui.Event{Kind: clientui.EventStreamGap, RecoveryCause: clientui.TranscriptRecoveryCauseStreamGap}:
	}
}

func sessionActivityDiagnosticsEnabled(enabled func() bool) bool {
	return enabled != nil && enabled()
}

func resubscribeSessionActivity(ctx context.Context, subscribe sessionActivitySubscriber, afterSequence uint64) (serverapi.SessionActivitySubscription, error) {
	for {
		if !waitSessionActivityRetry(ctx) {
			return nil, ctx.Err()
		}
		sub, err := subscribe(ctx, afterSequence)
		if err == nil {
			return sub, nil
		}
		// After a restart the broker may reject an old cursor, but cursor 0
		// asks for the fresh live stream and must keep retrying instead of
		// closing the TUI runtime event channel.
		if errors.Is(err, serverapi.ErrStreamGap) && afterSequence > 0 {
			return nil, err
		}
		if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() != nil {
			return nil, err
		}
	}
}

func waitSessionActivityRetry(ctx context.Context) bool {
	timer := time.NewTimer(sessionActivityResubscribeDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
