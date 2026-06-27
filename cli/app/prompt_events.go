package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

var promptActivityResubscribeDelay = 250 * time.Millisecond

type promptActivitySubscriber func(context.Context, uint64) (serverapi.PromptActivitySubscription, error)

type promptEventEmitter struct {
	mu     sync.RWMutex
	closed bool
	out    chan askEvent
}

func newPromptEventEmitter(size int) *promptEventEmitter {
	return &promptEventEmitter{out: make(chan askEvent, size)}
}

func (e *promptEventEmitter) emit(ctx context.Context, evt askEvent) bool {
	if e == nil {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case e.out <- evt:
		return true
	}
}

func (e *promptEventEmitter) close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	close(e.out)
}

func startPendingPromptEvents(ctx context.Context, sub serverapi.PromptActivitySubscription, subscribe promptActivitySubscriber, control client.PromptControlClient) (<-chan askEvent, func()) {
	emitter := newPromptEventEmitter(16)
	out := (<-chan askEvent)(emitter.out)
	if sub == nil || subscribe == nil || control == nil {
		emitter.close()
		return out, func() {}
	}
	pollCtx, cancel := context.WithCancel(ctx)
	var pendingMu sync.Mutex
	pendingPromptIDs := make(map[string]struct{})
	var lastSequence uint64
	var snapshotMode bool
	snapshotPromptIDs := make(map[string]struct{})
	snapshotPendingEvents := make([]clientui.PendingPromptEvent, 0)
	isPromptPending := func(promptID string) bool {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		_, exists := pendingPromptIDs[promptID]
		return exists
	}
	var requeue func(clientui.PendingPromptEvent)
	requeue = func(item clientui.PendingPromptEvent) {
		if pollCtx.Err() != nil {
			return
		}
		if !isPromptPending(item.PromptID) {
			return
		}
		_ = emitter.emit(pollCtx, pendingPromptEvent(pollCtx, item, control, requeue))
	}
	go func() {
		defer emitter.close()
		current := sub
		for {
			evt, err := current.Next(pollCtx)
			if err != nil {
				_ = current.Close()
				if errors.Is(err, context.Canceled) {
					return
				}
				for {
					nextSub, replayed, err := resubscribePromptActivity(pollCtx, subscribe, lastSequence)
					if err != nil {
						return
					}
					if !replayed {
						pendingMu.Lock()
						snapshotMode = true
						snapshotPromptIDs = make(map[string]struct{})
						snapshotPendingEvents = snapshotPendingEvents[:0]
						pendingMu.Unlock()
					}
					current = nextSub
					break
				}
				continue
			}
			if evt.Type == clientui.PendingPromptEventSnapshot {
				pendingMu.Lock()
				resolved := make([]string, 0)
				pendingEvents := make([]clientui.PendingPromptEvent, 0)
				if snapshotMode {
					for promptID := range pendingPromptIDs {
						if _, ok := snapshotPromptIDs[promptID]; ok {
							continue
						}
						delete(pendingPromptIDs, promptID)
						resolved = append(resolved, promptID)
					}
					pendingEvents = append(pendingEvents, snapshotPendingEvents...)
					snapshotMode = false
					snapshotPromptIDs = make(map[string]struct{})
					snapshotPendingEvents = snapshotPendingEvents[:0]
				}
				pendingMu.Unlock()
				for _, promptID := range resolved {
					if !emitter.emit(pollCtx, resolvedPromptEvent(promptID)) {
						_ = current.Close()
						return
					}
				}
				for _, pendingEvt := range pendingEvents {
					askEvt := pendingPromptEvent(pollCtx, pendingEvt, control, requeue)
					if !emitter.emit(pollCtx, askEvt) {
						_ = current.Close()
						return
					}
				}
				continue
			}
			if strings.TrimSpace(evt.PromptID) == "" {
				if evt.Sequence > lastSequence {
					lastSequence = evt.Sequence
				}
				continue
			}
			if evt.Sequence > lastSequence {
				lastSequence = evt.Sequence
			}
			switch evt.Type {
			case clientui.PendingPromptEventResolved:
				pendingMu.Lock()
				delete(pendingPromptIDs, evt.PromptID)
				pendingMu.Unlock()
				if !emitter.emit(pollCtx, resolvedPromptEvent(evt.PromptID)) {
					_ = current.Close()
					return
				}
				continue
			case clientui.PendingPromptEventPending:
				pendingMu.Lock()
				isSnapshotPending := snapshotMode && evt.Sequence == 0
				if snapshotMode && evt.Sequence == 0 {
					snapshotPromptIDs[evt.PromptID] = struct{}{}
				}
				if _, exists := pendingPromptIDs[evt.PromptID]; exists {
					pendingMu.Unlock()
					continue
				}
				pendingPromptIDs[evt.PromptID] = struct{}{}
				if isSnapshotPending {
					snapshotPendingEvents = append(snapshotPendingEvents, evt)
					pendingMu.Unlock()
					continue
				}
				pendingMu.Unlock()
			default:
				continue
			}
			askEvt := pendingPromptEvent(pollCtx, evt, control, requeue)
			if !emitter.emit(pollCtx, askEvt) {
				_ = current.Close()
				return
			}
		}
	}()
	return out, cancel
}

func resubscribePromptActivity(ctx context.Context, subscribe promptActivitySubscriber, afterSequence uint64) (serverapi.PromptActivitySubscription, bool, error) {
	for {
		if !waitPromptActivityRetry(ctx) {
			return nil, false, ctx.Err()
		}
		sub, err := subscribe(ctx, afterSequence)
		if err == nil {
			return sub, true, nil
		}
		if errors.Is(err, serverapi.ErrStreamGap) && afterSequence > 0 {
			sub, err := subscribe(ctx, 0)
			if err == nil {
				return sub, false, nil
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
	}
}

func waitPromptActivityRetry(ctx context.Context) bool {
	timer := time.NewTimer(promptActivityResubscribeDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func pendingPromptEvent(ctx context.Context, item clientui.PendingPromptEvent, control client.PromptControlClient, retry func(clientui.PendingPromptEvent)) askEvent {
	req := item
	req.Suggestions = append([]string(nil), item.Suggestions...)
	req.ApprovalOptions = append([]clientui.ApprovalOption(nil), item.ApprovalOptions...)
	reply := make(chan askReply, 1)
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	go func() {
		var (
			result askReply
			ok     bool
		)
		select {
		case <-promptCtx.Done():
			return
		case result, ok = <-reply:
			if !ok {
				return
			}
		}
		if item.Approval {
			answerReq := serverapi.ApprovalAnswerRequest{ClientRequestID: uuid.NewString(), SessionID: item.SessionID, ApprovalID: item.PromptID}
			if result.err != nil {
				answerReq.ErrorMessage = result.err.Error()
			} else if result.response.Approval != nil {
				answerReq.Decision = clientui.ApprovalDecision(result.response.Approval.Decision)
				answerReq.Commentary = result.response.Approval.Commentary
			} else {
				answerReq.ErrorMessage = errors.New("approval response is required").Error()
			}
			if err := control.AnswerApproval(promptCtx, answerReq); err != nil {
				if retry != nil && shouldRetryPromptAnswerError(err) {
					retry(item)
				}
			}
			return
		}
		answerReq := serverapi.AskAnswerRequest{ClientRequestID: uuid.NewString(), SessionID: item.SessionID, AskID: item.PromptID}
		if result.err != nil {
			answerReq.ErrorMessage = result.err.Error()
		} else {
			answerReq.Answer = result.response.Answer
			answerReq.SelectedOptionNumber = result.response.SelectedOptionNumber
			answerReq.FreeformAnswer = result.response.FreeformAnswer
		}
		if err := control.AnswerAsk(promptCtx, answerReq); err != nil {
			if retry != nil && shouldRetryPromptAnswerError(err) {
				retry(item)
			}
		}
	}()
	return askEvent{req: req, reply: reply, cancel: cancelPrompt}
}

func resolvedPromptEvent(promptID string) askEvent {
	return askEvent{resolvedPromptID: strings.TrimSpace(promptID)}
}

func shouldRetryPromptAnswerError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, serverapi.ErrPromptNotFound) || errors.Is(err, serverapi.ErrPromptAlreadyResolved) || errors.Is(err, serverapi.ErrPromptUnsupported) {
		return false
	}
	return true
}
