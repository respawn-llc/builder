package processview

import (
	"context"
	"errors"
	"io"
	"testing"

	shelltool "core/server/tools/shell"
	"core/shared/serverapi"
)

type stubSubscriber struct {
	sub shelltool.OutputSubscription
	err error
}

func (s *stubSubscriber) SubscribeOutput(context.Context, string, int64) (shelltool.OutputSubscription, error) {
	return s.sub, s.err
}

type stubShellOutputSubscription struct {
	chunk shelltool.OutputChunk
	err   error
}

func (s *stubShellOutputSubscription) Next(context.Context) (shelltool.OutputChunk, error) {
	if s.err != nil {
		return shelltool.OutputChunk{}, s.err
	}
	chunk := s.chunk
	s.err = io.EOF
	return chunk, nil
}

func (s *stubShellOutputSubscription) Close() error { return nil }

func testSubscriptionNextError(t *testing.T, nextErr error, wantErr error) {
	t.Helper()
	svc := NewProcessOutputService(
		&stubSubscriber{sub: &stubShellOutputSubscription{err: nextErr}},
	)
	sub, err := svc.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: "proc-1"})
	if err != nil {
		t.Fatalf("SubscribeProcessOutput: %v", err)
	}
	if _, err := sub.Next(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Next error = %v, want %v", err, wantErr)
	}
}

func testSubscribeTimeFailure(t *testing.T, wantErr error) {
	t.Helper()
	svc := NewProcessOutputService(
		&stubSubscriber{err: errors.New("subscribe failed")},
	)
	if _, err := svc.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: "proc-1", OffsetBytes: 10}); !errors.Is(err, wantErr) {
		t.Fatalf("SubscribeProcessOutput error = %v, want %v", err, wantErr)
	}
}

func TestServiceSubscribesAndProjectsChunks(t *testing.T) {
	svc := NewProcessOutputService(
		&stubSubscriber{sub: &stubShellOutputSubscription{chunk: shelltool.OutputChunk{ProcessID: "proc-1", OffsetBytes: 10, NextOffsetBytes: 15, Text: "hello"}}},
	)
	sub, err := svc.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: "proc-1", OffsetBytes: 10})
	if err != nil {
		t.Fatalf("SubscribeProcessOutput: %v", err)
	}
	chunk, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if chunk.ProcessID != "proc-1" || chunk.OffsetBytes != 10 || chunk.NextOffsetBytes != 15 || chunk.Text != "hello" {
		t.Fatalf("unexpected chunk: %+v", chunk)
	}
}

func TestServiceValidatesRequest(t *testing.T) {
	if _, err := NewProcessOutputService(&stubSubscriber{}).SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestServiceRejectsUnavailableStream(t *testing.T) {
	svc := NewProcessOutputService(&stubSubscriber{err: serverapi.ErrStreamUnavailable})
	if _, err := svc.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: "proc-1"}); !errors.Is(err, serverapi.ErrStreamUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestServiceRejectsOffsetOutsideRetainedRange(t *testing.T) {
	svc := NewProcessOutputService(
		&stubSubscriber{err: serverapi.ErrStreamGap},
	)
	if _, err := svc.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: "proc-1", OffsetBytes: 6}); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("expected gap error, got %v", err)
	}
}

func TestServiceNormalizesSubscriptionNextFailures(t *testing.T) {
	testSubscriptionNextError(t, errors.New("disk read failed"), serverapi.ErrStreamFailed)
}

func TestServicePassesThroughSubscriptionEOF(t *testing.T) {
	testSubscriptionNextError(t, io.EOF, io.EOF)
}

func TestServicePassesThroughSubscriptionContextCanceled(t *testing.T) {
	testSubscriptionNextError(t, context.Canceled, context.Canceled)
}

func TestServiceNormalizesSubscribeTimeGenericFailure(t *testing.T) {
	testSubscribeTimeFailure(t, serverapi.ErrStreamFailed)
}
