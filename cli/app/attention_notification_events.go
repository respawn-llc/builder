package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

var attentionNotificationResubscribeDelay = 250 * time.Millisecond

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
		if !waitAttentionNotificationRetry(ctx) {
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

func waitAttentionNotificationRetry(ctx context.Context) bool {
	timer := time.NewTimer(attentionNotificationResubscribeDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
		id := strings.TrimSpace(evt.Pending.ID)
		if id == "" {
			return
		}
		if _, exists := surfaced[id]; exists {
			return
		}
		surfaced[id] = struct{}{}
		hook.OnAttentionNotification(evt)
	case clientui.AttentionNotificationEventResolved:
		delete(surfaced, strings.TrimSpace(evt.ID))
	}
}
