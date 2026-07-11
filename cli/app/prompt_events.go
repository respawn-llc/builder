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

type promptActivitySubscriber func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error)

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

func startPendingPromptEvents(ctx context.Context, sub serverapi.PromptActivitySubscription, subscribe promptActivitySubscriber, control client.PromptControlClient, notificationFallback attentionNotificationHook) (<-chan askEvent, func()) {
	emitter := newPromptEventEmitter(16)
	out := (<-chan askEvent)(emitter.out)
	if sub == nil || subscribe == nil || control == nil {
		emitter.close()
		return out, func() {}
	}
	pollCtx, cancel := context.WithCancel(ctx)
	var pendingMu sync.Mutex
	pendingPromptIDs := make(map[string]struct{})
	var lastReadModelVersion clientui.ReadModelVersion
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
					nextSub, replayed, err := resubscribePromptActivity(pollCtx, subscribe, lastReadModelVersion)
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
					if !emitter.emit(pollCtx, askEvent{resolvedPromptID: strings.TrimSpace(promptID)}) {
						_ = current.Close()
						return
					}
				}
				for _, pendingEvt := range pendingEvents {
					askEvt := pendingPromptEvent(pollCtx, pendingEvt, control, requeue)
					notifyPromptActivityFallback(notificationFallback, pendingEvt, clientui.AttentionNotificationSourceSnapshot)
					if !emitter.emit(pollCtx, askEvt) {
						_ = current.Close()
						return
					}
				}
				continue
			}
			if strings.TrimSpace(evt.PromptID) == "" {
				lastReadModelVersion = newestPromptReadModelVersion(lastReadModelVersion, evt.ReadModelVersion)
				continue
			}
			lastReadModelVersion = newestPromptReadModelVersion(lastReadModelVersion, evt.ReadModelVersion)
			switch evt.Type {
			case clientui.PendingPromptEventResolved:
				pendingMu.Lock()
				delete(pendingPromptIDs, evt.PromptID)
				pendingMu.Unlock()
				if !emitter.emit(pollCtx, askEvent{resolvedPromptID: strings.TrimSpace(evt.PromptID)}) {
					_ = current.Close()
					return
				}
				continue
			case clientui.PendingPromptEventPending:
				pendingMu.Lock()
				isSnapshotPending := snapshotMode
				if snapshotMode {
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
			notifyPromptActivityFallback(notificationFallback, evt, clientui.AttentionNotificationSourceLive)
			if !emitter.emit(pollCtx, askEvt) {
				_ = current.Close()
				return
			}
		}
	}()
	return out, cancel
}

func notifyPromptActivityFallback(hook attentionNotificationHook, evt clientui.PendingPromptEvent, source clientui.AttentionNotificationSource) {
	if hook == nil || evt.Type != clientui.PendingPromptEventPending || strings.TrimSpace(evt.PromptID) == "" {
		return
	}
	occurredAt := evt.CreatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	kind := clientui.AttentionNotificationKindQuestion
	if evt.Approval {
		kind = clientui.AttentionNotificationKindApproval
	}
	notification := clientui.AttentionNotification{
		ID: clientui.AttentionNotificationID{
			Kind: kind,
			UUID: strings.TrimSpace(evt.PromptID),
		},
		Kind:       kind,
		OccurredAt: occurredAt,
		Revision:   1,
		Target: clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetSessionPrompt,
			SessionID: strings.TrimSpace(evt.SessionID),
		},
	}
	if evt.Approval {
		notification.Approval = &clientui.AttentionNotificationApprovalState{
			Message: strings.TrimSpace(evt.Question),
		}
	} else {
		notification.Question = &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{strings.TrimSpace(evt.PromptID)},
			MaterializedAskIDs:      []string{strings.TrimSpace(evt.PromptID)},
			CurrentUnresolvedAskIDs: []string{strings.TrimSpace(evt.PromptID)},
			Preview:                 strings.TrimSpace(evt.Question),
			DisplayCount:            1,
			MaterializedCount:       1,
		}
	}
	hook.OnAttentionNotification(clientui.AttentionNotificationEvent{
		Type:    clientui.AttentionNotificationEventPending,
		Source:  source,
		Pending: &notification,
	})
}

func resubscribePromptActivity(ctx context.Context, subscribe promptActivitySubscriber, afterVersion clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, bool, error) {
	for {
		if !waitPromptActivityRetry(ctx) {
			return nil, false, ctx.Err()
		}
		sub, err := subscribe(ctx, afterVersion)
		if err == nil {
			return sub, true, nil
		}
		if errors.Is(err, serverapi.ErrStreamGap) && afterVersion != (clientui.ReadModelVersion{}) {
			sub, err := subscribe(ctx, clientui.ReadModelVersion{})
			if err == nil {
				return sub, false, nil
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
	}
}

func newestPromptReadModelVersion(current clientui.ReadModelVersion, incoming clientui.ReadModelVersion) clientui.ReadModelVersion {
	if incoming == (clientui.ReadModelVersion{}) {
		return current
	}
	if current == (clientui.ReadModelVersion{}) || incoming.NewerThan(current) {
		return incoming
	}
	return current
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

func shouldRetryPromptAnswerError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, serverapi.ErrPromptNotFound) || errors.Is(err, serverapi.ErrPromptAlreadyResolved) || errors.Is(err, serverapi.ErrPromptUnsupported) {
		return false
	}
	return true
}
