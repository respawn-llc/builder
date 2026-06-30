package llm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type streamingModelClient interface {
	Client
	StreamClient
	StreamEventsClient
	CompactionClient
	ProviderCapabilitiesClient
	RequestInputTokenCountClient
	RequestInputTokenCountSupportClient
	ModelContextWindowClient
}

type idleWatchdogClient struct {
	streamingModelClient
	idle time.Duration
}

func newIdleWatchdogClient(inner streamingModelClient, idle time.Duration) *idleWatchdogClient {
	return &idleWatchdogClient{streamingModelClient: inner, idle: idle}
}

func (c *idleWatchdogClient) GenerateStream(ctx context.Context, request Request, onDelta func(text string)) (Response, error) {
	var callback func(AssistantDelta)
	if onDelta != nil {
		callback = func(delta AssistantDelta) {
			onDelta(delta.Text)
		}
	}
	return c.GenerateStreamWithEvents(ctx, request, StreamCallbacks{OnAssistantDelta: callback})
}

func (c *idleWatchdogClient) GenerateStreamWithEvents(ctx context.Context, request Request, callbacks StreamCallbacks) (Response, error) {
	watchdog := newStreamIdleWatchdog(ctx, c.idle)
	defer watchdog.stop()

	previousActivity := callbacks.OnStreamActivity
	callbacks.OnStreamActivity = func() {
		watchdog.ping()
		if previousActivity != nil {
			previousActivity()
		}
	}

	resp, err := c.streamingModelClient.GenerateStreamWithEvents(watchdog.ctx, request, callbacks)
	if err != nil && errors.Is(context.Cause(watchdog.ctx), ErrModelStreamStalled) {
		return Response{}, fmt.Errorf("model stream stalled: %w", ErrModelStreamStalled)
	}
	return resp, err
}

type streamIdleWatchdog struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	pings  chan struct{}
	done   chan struct{}
}

func newStreamIdleWatchdog(parent context.Context, idle time.Duration) *streamIdleWatchdog {
	ctx, cancel := context.WithCancelCause(parent)
	w := &streamIdleWatchdog{ctx: ctx, cancel: cancel}
	if idle <= 0 {
		return w
	}
	w.pings = make(chan struct{}, 1)
	w.done = make(chan struct{})
	go w.run(idle)
	return w
}

func (w *streamIdleWatchdog) run(idle time.Duration) {
	defer close(w.done)
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.pings:
			timer.Stop()
			timer.Reset(idle)
		case <-timer.C:
			w.cancel(ErrModelStreamStalled)
			return
		}
	}
}

func (w *streamIdleWatchdog) ping() {
	if w.pings == nil {
		return
	}
	select {
	case w.pings <- struct{}{}:
	default:
	}
}

func (w *streamIdleWatchdog) stop() {
	w.cancel(nil)
	if w.done != nil {
		<-w.done
	}
}
