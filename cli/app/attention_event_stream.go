package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/serverapi"
	"core/shared/textutil"
)

const attentionEventStreamOutputCapacity = 1
const attentionWorkflowTaskIDMaxBytes = textutil.MarkdownSummaryLimitBytes
const attentionFactMaxRetainedBytes = sha256.Size + textutil.MarkdownSummaryLimitBytes + attentionWorkflowTaskIDMaxBytes

type attentionFactKind uint8

const (
	attentionFactKindQuestion attentionFactKind = iota + 1
	attentionFactKindApproval
)

type attentionNotificationKey [sha256.Size]byte

type attentionFact struct {
	notificationKey  attentionNotificationKey
	kind             attentionFactKind
	occurredAt       time.Time
	summary          string
	summaryTruncated bool
	workflowTaskID   *lifecyclecontract.WorkflowTaskID
}

func (*attentionFact) attentionStreamOutcome() {}

type attentionStreamControlKind uint8

const (
	attentionStreamControlResolved attentionStreamControlKind = iota + 1
	attentionStreamControlSnapshotComplete
)

type attentionStreamControl struct {
	kind attentionStreamControlKind
}

func (attentionStreamControl) attentionStreamOutcome() {}

type attentionStreamDiscontinuityReason uint8

const (
	attentionStreamDiscontinuityInvalidEvent attentionStreamDiscontinuityReason = iota + 1
	attentionStreamDiscontinuityUnsupportedKind
	attentionStreamDiscontinuityInvalidSummary
	attentionStreamDiscontinuityInvalidTaskID
	attentionStreamDiscontinuitySubscriptionLoss
	attentionStreamDiscontinuitySubscriptionCloseFailure
)

type attentionStreamDiscontinuity struct {
	reason attentionStreamDiscontinuityReason
}

func (attentionStreamDiscontinuity) attentionStreamOutcome() {}

type attentionStreamOutcome interface {
	attentionStreamOutcome()
}

type attentionEventStream struct {
	events <-chan attentionStreamOutcome
	reopen *coalescingAttentionReopen
	owner  *joinedSubscriptionOwner[serverapi.AttentionNotificationSubscription]
}

type attentionSessionSubscriber func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)

type coalescingAttentionReopen struct {
	requested atomic.Bool
	signal    chan struct{}
}

func newCoalescingAttentionReopen() *coalescingAttentionReopen {
	return &coalescingAttentionReopen{signal: make(chan struct{}, 1)}
}

func (r *coalescingAttentionReopen) Request() {
	if r == nil || !r.requested.CompareAndSwap(false, true) {
		return
	}
	select {
	case r.signal <- struct{}{}:
	default:
	}
}

func (r *coalescingAttentionReopen) AllowNextRequest() {
	if r != nil {
		r.requested.Store(false)
	}
}

func startAttentionEventStream(ctx context.Context, sessionID string, subscribe attentionSessionSubscriber) *attentionEventStream {
	out := make(chan attentionStreamOutcome, attentionEventStreamOutputCapacity)
	reopen := newCoalescingAttentionReopen()
	owner := newJoinedSubscriptionOwner[serverapi.AttentionNotificationSubscription](ctx)
	stream := &attentionEventStream{
		events: out,
		reopen: reopen,
		owner:  owner,
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
			subscription, err := resubscribeAttention(owner.ctx, sessionID, subscribe)
			if err != nil {
				return
			}
			owned := newOwnedSubscription(subscription)
			if !owner.install(owned) {
				_ = owned.Close()
				return
			}
			reopenStream, stop := pumpAttentionSubscription(owner.ctx, owned, out, reopen)
			owner.clear(owned)
			if stop {
				return
			}
			if !reopenStream {
				return
			}
		}
	}()
	return stream
}

func (s *attentionEventStream) RequestReopen() {
	if s == nil {
		return
	}
	s.reopen.Request()
}

func (s *attentionEventStream) Close() {
	if s == nil {
		return
	}
	s.owner.Close()
}

func pumpAttentionSubscription(
	ctx context.Context,
	subscription *ownedSubscription[serverapi.AttentionNotificationSubscription],
	out chan<- attentionStreamOutcome,
	reopen *coalescingAttentionReopen,
) (reopenStream bool, stop bool) {
	closeReported := false
	closeSubscription := func(report bool) {
		err := subscription.Close()
		if report && err != nil && !closeReported && ctx.Err() == nil {
			closeReported = true
			emitAttentionStreamOutcome(ctx, out, attentionStreamDiscontinuity{reason: attentionStreamDiscontinuitySubscriptionCloseFailure})
		}
	}
	defer func() {
		closeSubscription(true)
	}()
	for {
		nextCtx, cancel := context.WithCancel(ctx)
		next := beginSubscriptionNext(nextCtx, func(ctx context.Context) (attentionStreamOutcome, error) {
			return normalizeNextAttentionEvent(ctx, subscription.subscription)
		})
		select {
		case <-ctx.Done():
			cancel()
			closeSubscription(false)
			<-next
			return false, true
		case <-reopen.signal:
			cancel()
			closeSubscription(true)
			<-next
			return true, false
		case result := <-next:
			cancel()
			if result.err != nil {
				if (errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded)) && ctx.Err() != nil {
					return false, true
				}
				reopen.AllowNextRequest()
				if !emitAttentionStreamOutcome(ctx, out, attentionStreamDiscontinuity{reason: attentionStreamDiscontinuitySubscriptionLoss}) {
					return false, true
				}
				closeSubscription(true)
				return waitForAttentionReopen(ctx, reopen)
			}
			outcome := result.value
			if control, ok := outcome.(attentionStreamControl); ok && control.kind == attentionStreamControlSnapshotComplete {
				reopen.AllowNextRequest()
			}
			if _, discontinuity := outcome.(attentionStreamDiscontinuity); discontinuity {
				reopen.AllowNextRequest()
			}
			if !emitAttentionStreamOutcome(ctx, out, outcome) {
				return false, true
			}
			if _, discontinuity := outcome.(attentionStreamDiscontinuity); discontinuity {
				closeSubscription(true)
				return waitForAttentionReopen(ctx, reopen)
			}
		}
	}
}

func waitForAttentionReopen(ctx context.Context, reopen *coalescingAttentionReopen) (reopenStream bool, stop bool) {
	select {
	case <-ctx.Done():
		return false, true
	case <-reopen.signal:
		return true, false
	}
}

func resubscribeAttention(ctx context.Context, sessionID string, subscribe attentionSessionSubscriber) (serverapi.AttentionNotificationSubscription, error) {
	request := serverapi.AttentionSessionNotificationSubscribeRequest{
		SessionID:                    sessionID,
		IncludePendingPromptSnapshot: true,
	}
	for {
		subscription, err := subscribe(ctx, request)
		if err == nil && subscription != nil {
			return subscription, nil
		}
		if err == nil {
			err = errors.New("attention notification subscription is required")
		}
		if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() != nil {
			return nil, err
		}
		if !waitSubscriptionRetry(ctx) {
			return nil, ctx.Err()
		}
	}
}

func normalizeNextAttentionEvent(ctx context.Context, subscription serverapi.AttentionNotificationSubscription) (attentionStreamOutcome, error) {
	event, err := subscription.Next(ctx)
	if err != nil {
		return nil, err
	}
	outcome := normalizeAttentionEvent(event)
	event = clientui.AttentionNotificationEvent{}
	return outcome, nil
}

func emitAttentionStreamOutcome(ctx context.Context, out chan<- attentionStreamOutcome, outcome attentionStreamOutcome) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- outcome:
		return true
	}
}

func normalizeAttentionEvent(event clientui.AttentionNotificationEvent) attentionStreamOutcome {
	if err := serverapi.ValidateAttentionNotificationEvent(event); err != nil {
		return attentionStreamDiscontinuity{reason: attentionStreamDiscontinuityInvalidEvent}
	}
	switch event.Type {
	case clientui.AttentionNotificationEventPending:
		return normalizePendingAttentionNotification(*event.Pending)
	case clientui.AttentionNotificationEventResolved:
		return attentionStreamControl{kind: attentionStreamControlResolved}
	case clientui.AttentionNotificationEventSnapshotComplete:
		return attentionStreamControl{kind: attentionStreamControlSnapshotComplete}
	default:
		return attentionStreamDiscontinuity{reason: attentionStreamDiscontinuityInvalidEvent}
	}
}

func normalizePendingAttentionNotification(notification clientui.AttentionNotification) attentionStreamOutcome {
	kind, summary, reason := normalizedAttentionKindAndSummary(notification)
	if reason != 0 {
		return attentionStreamDiscontinuity{reason: reason}
	}
	limitedSummary, truncated, err := textutil.LimitUTF8Bytes(summary, textutil.MarkdownSummaryLimitBytes)
	if err != nil {
		return attentionStreamDiscontinuity{reason: attentionStreamDiscontinuityInvalidSummary}
	}
	taskID, reason := normalizedAttentionTaskID(notification.Target)
	if reason != 0 {
		return attentionStreamDiscontinuity{reason: reason}
	}
	return &attentionFact{
		notificationKey:  attentionKeyForNotificationID(notification.ID),
		kind:             kind,
		occurredAt:       notification.OccurredAt,
		summary:          strings.Clone(limitedSummary),
		summaryTruncated: truncated,
		workflowTaskID:   taskID,
	}
}

func normalizedAttentionKindAndSummary(notification clientui.AttentionNotification) (attentionFactKind, string, attentionStreamDiscontinuityReason) {
	switch notification.Kind {
	case clientui.AttentionNotificationKindQuestion:
		if notification.Question == nil {
			return 0, "", attentionStreamDiscontinuityInvalidEvent
		}
		return attentionFactKindQuestion, notification.Question.Preview, 0
	case clientui.AttentionNotificationKindApproval:
		if notification.Approval == nil {
			return 0, "", attentionStreamDiscontinuityInvalidEvent
		}
		return attentionFactKindApproval, notification.Approval.Message, 0
	default:
		return 0, "", attentionStreamDiscontinuityUnsupportedKind
	}
}

func normalizedAttentionTaskID(target clientui.AttentionNotificationTarget) (*lifecyclecontract.WorkflowTaskID, attentionStreamDiscontinuityReason) {
	switch target.Kind {
	case clientui.AttentionNotificationTargetSessionPrompt:
		return nil, 0
	case clientui.AttentionNotificationTargetWorkflowTask:
		taskID, truncated, err := textutil.LimitUTF8Bytes(target.TaskID, attentionWorkflowTaskIDMaxBytes)
		if err != nil || truncated {
			return nil, attentionStreamDiscontinuityInvalidTaskID
		}
		parsed, err := lifecyclecontract.ParseWorkflowTaskID(strings.Clone(taskID))
		if err != nil {
			return nil, attentionStreamDiscontinuityInvalidTaskID
		}
		return &parsed, 0
	default:
		return nil, attentionStreamDiscontinuityInvalidEvent
	}
}

func attentionKeyForNotificationID(id clientui.AttentionNotificationID) attentionNotificationKey {
	digest := sha256.New()
	_, _ = digest.Write([]byte(id.Kind))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(id.UUID))
	var key attentionNotificationKey
	copy(key[:], digest.Sum(nil))
	return key
}
