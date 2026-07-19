package app

import (
	"context"
	"crypto/sha256"
	"strings"
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
}

func startAttentionEventStream(ctx context.Context, subscription serverapi.AttentionNotificationSubscription) attentionEventStream {
	out := make(chan attentionStreamOutcome, attentionEventStreamOutputCapacity)
	stream := attentionEventStream{events: out}
	if subscription == nil {
		close(out)
		return stream
	}
	go func() {
		defer func() {
			if err := subscription.Close(); err != nil && ctx.Err() == nil {
				emitAttentionStreamOutcome(ctx, out, attentionStreamDiscontinuity{reason: attentionStreamDiscontinuitySubscriptionCloseFailure})
			}
			close(out)
		}()
		for {
			outcome, err := normalizeNextAttentionEvent(ctx, subscription)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				emitAttentionStreamOutcome(ctx, out, attentionStreamDiscontinuity{reason: attentionStreamDiscontinuitySubscriptionLoss})
				return
			}
			if !emitAttentionStreamOutcome(ctx, out, outcome) {
				return
			}
		}
	}()
	return stream
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
