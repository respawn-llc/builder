package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"core/shared/serverapi"
)

func TestTranscriptPromptAnswerRetryPolicyIsBounded(t *testing.T) {
	persistent := errors.New("persistent retryable failure")
	var (
		calls int
		waits []time.Duration
	)
	err := retryTranscriptPromptAnswer(
		context.Background(),
		transcriptPromptAnswerRetryDelays,
		func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
		func() error {
			calls++
			return persistent
		},
	)
	if !errors.Is(err, persistent) {
		t.Fatalf("retry error = %v, want persistent failure", err)
	}
	if calls != 6 {
		t.Fatalf("service calls = %d, want 6", calls)
	}
	wantWaits := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if !reflect.DeepEqual(waits, wantWaits) {
		t.Fatalf("retry waits = %v, want %v", waits, wantWaits)
	}
}

func TestTranscriptPromptAnswerRetryStopsImmediatelyForTerminalErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "canceled", err: context.Canceled},
		{name: "not found", err: serverapi.ErrPromptNotFound},
		{name: "already resolved", err: serverapi.ErrPromptAlreadyResolved},
		{name: "unsupported", err: serverapi.ErrPromptUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			waits := 0
			err := retryTranscriptPromptAnswer(
				context.Background(),
				transcriptPromptAnswerRetryDelays,
				func(context.Context, time.Duration) error {
					waits++
					return nil
				},
				func() error {
					calls++
					return test.err
				},
			)
			if !errors.Is(err, test.err) {
				t.Fatalf("retry error = %v, want %v", err, test.err)
			}
			if calls != 1 || waits != 0 {
				t.Fatalf("calls = %d waits = %d, want one call and no wait", calls, waits)
			}
		})
	}
}

func TestTranscriptPromptAnswerRetryCancellationStopsBlockedBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	firstCall := make(chan struct{})
	waitStarted := make(chan struct{})
	calls := 0
	result := make(chan error, 1)
	go func() {
		result <- retryTranscriptPromptAnswer(
			ctx,
			transcriptPromptAnswerRetryDelays,
			func(waitCtx context.Context, _ time.Duration) error {
				close(waitStarted)
				<-waitCtx.Done()
				return waitCtx.Err()
			},
			func() error {
				calls++
				if calls == 1 {
					close(firstCall)
				}
				return errors.New("retryable failure")
			},
		)
	}()

	<-firstCall
	<-waitStarted
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context canceled", err)
	}
	if calls != 1 {
		t.Fatalf("service calls = %d, want no call after canceled backoff", calls)
	}
}
