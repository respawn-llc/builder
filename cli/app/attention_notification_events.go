package app

import (
	"context"
	"errors"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type attentionNotificationSubscriber func(context.Context) (serverapi.AttentionNotificationSubscription, error)

func startAttentionNotificationEvents(ctx context.Context, sub serverapi.AttentionNotificationSubscription, subscribe attentionNotificationSubscriber, hook attentionNotificationHook) func() {
	if sub == nil || subscribe == nil || hook == nil {
		return func() {}
	}
	pollCtx, cancel := context.WithCancel(ctx)
	go func() {
		current := sub
		surfaced := make(map[string]struct{})
		defer func() { _ = current.Close() }()
		for {
			evt, err := current.Next(pollCtx)
			if err != nil {
				_ = current.Close()
				if errors.Is(err, context.Canceled) || pollCtx.Err() != nil {
					return
				}
				nextSub, err := resubscribeAttentionNotifications(pollCtx, subscribe)
				if err != nil {
					return
				}
				current = nextSub
				continue
			}
			applyAttentionNotificationEvent(evt, surfaced, hook)
		}
	}()
	return cancel
}

func resubscribeAttentionNotifications(ctx context.Context, subscribe attentionNotificationSubscriber) (serverapi.AttentionNotificationSubscription, error) {
	for {
		if !waitPromptActivityRetry(ctx) {
			return nil, ctx.Err()
		}
		sub, err := subscribe(ctx)
		if err == nil {
			return sub, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}
}

func applyAttentionNotificationEvent(evt clientui.AttentionNotificationEvent, surfaced map[string]struct{}, hook attentionNotificationHook) {
	if surfaced == nil || hook == nil {
		return
	}
	switch evt.Type {
	case clientui.AttentionNotificationEventPending:
		if evt.Pending == nil {
			return
		}
		id := attentionNotificationMapKey(evt.Pending.ID)
		if id == "" {
			return
		}
		if _, exists := surfaced[id]; exists {
			return
		}
		if !tuiSupportsAttentionNotification(*evt.Pending) {
			return
		}
		surfaced[id] = struct{}{}
		hook.OnAttentionNotification(evt)
	case clientui.AttentionNotificationEventResolved:
		if evt.ID == nil {
			return
		}
		delete(surfaced, attentionNotificationMapKey(*evt.ID))
	}
}

func tuiSupportsAttentionNotification(notification clientui.AttentionNotification) bool {
	switch notification.Kind {
	case clientui.AttentionNotificationKindQuestion, clientui.AttentionNotificationKindApproval:
		return true
	default:
		return false
	}
}

func attentionNotificationMapKey(id clientui.AttentionNotificationID) string {
	if id.Kind == "" && id.UUID == "" {
		return ""
	}
	return string(id.Kind) + "\x00" + id.UUID
}
