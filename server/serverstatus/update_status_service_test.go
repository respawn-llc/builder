package serverstatus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpdateStatusServiceIsLazyAndCachesCompletedAttempt(t *testing.T) {
	source := &countingReleaseSource{metadata: releaseMetadata{Version: "1.2.0"}}
	service := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })

	if calls := source.calls.Load(); calls != 0 {
		t.Fatalf("release calls after construction = %d, want 0", calls)
	}
	first, err := service.status(context.Background())
	if err != nil {
		t.Fatalf("first Status: %v", err)
	}
	second, err := service.status(context.Background())
	if err != nil {
		t.Fatalf("second Status: %v", err)
	}
	if first.kind != updateStatusAvailable || second.kind != updateStatusAvailable {
		t.Fatalf("result kinds = %d/%d, want available", first.kind, second.kind)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls = %d, want 1", calls)
	}
}

func TestUpdateStatusServiceCachesFailedAttempt(t *testing.T) {
	source := &countingReleaseSource{err: &releaseHTTPStatusError{StatusCode: 503, Status: "503 Service Unavailable"}}
	service := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })

	for range 2 {
		result, err := service.status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if result.kind != updateStatusCheckFailed {
			t.Fatalf("kind = %d, want check_failed", result.kind)
		}
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls = %d, want 1", calls)
	}
}

func TestUpdateStatusServiceBoundsReleaseAttempt(t *testing.T) {
	source := &blockingReleaseSource{}
	service := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })

	ctx, cancel := context.WithTimeout(context.Background(), updateStatusTimeout+time.Second)
	defer cancel()
	result, err := service.status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result.kind != updateStatusCheckUnavailable {
		t.Fatalf("kind = %d, want check_unavailable", result.kind)
	}
}

func TestUpdateStatusServiceRefreshesCompletedAttemptAtOneHour(t *testing.T) {
	now := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	source := &sequenceReleaseSource{results: []releaseMetadata{{Version: "1.1.0"}, {Version: "1.2.0"}}}
	service := newUpdateStatusService("1.1.0", false, source, func() time.Time { return now })
	t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })

	first, err := service.status(context.Background())
	if err != nil {
		t.Fatalf("first Status: %v", err)
	}
	if first.kind != updateStatusCurrent {
		t.Fatalf("first kind = %d, want current", first.kind)
	}
	now = now.Add(updateStatusFreshness - time.Nanosecond)
	beforeExpiry, err := service.status(context.Background())
	if err != nil {
		t.Fatalf("Status before expiry: %v", err)
	}
	if beforeExpiry.kind != updateStatusCurrent {
		t.Fatalf("before-expiry kind = %d, want current", beforeExpiry.kind)
	}
	now = now.Add(time.Nanosecond)
	afterExpiry, err := service.status(context.Background())
	if err != nil {
		t.Fatalf("Status at expiry: %v", err)
	}
	if afterExpiry.kind != updateStatusAvailable {
		t.Fatalf("at-expiry kind = %d, want available", afterExpiry.kind)
	}
	if calls := source.calls.Load(); calls != 2 {
		t.Fatalf("release calls = %d, want 2", calls)
	}
}

func TestUpdateStatusServiceCoalescesConcurrentRequests(t *testing.T) {
	source := newControlledReleaseSource(releaseMetadata{Version: "1.2.0"}, nil)
	service := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })

	results := make(chan updateStatusResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			result, err := service.status(context.Background())
			results <- result
			errs <- err
		}()
	}
	<-source.started
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls while shared check is pending = %d, want 1", calls)
	}
	close(source.release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Status: %v", err)
		}
		if result := <-results; result.kind != updateStatusAvailable {
			t.Fatalf("kind = %d, want available", result.kind)
		}
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls = %d, want 1", calls)
	}
}

func TestUpdateStatusCallerCancellationIsLocal(t *testing.T) {
	source := newControlledReleaseSource(releaseMetadata{Version: "1.2.0"}, nil)
	service := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, err := service.status(ctx)
		cancelled <- err
	}()
	<-source.started
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Status error = %v, want context canceled", err)
	}

	completed := make(chan updateStatusResult, 1)
	completionErrors := make(chan error, 1)
	go func() {
		result, err := service.status(context.Background())
		completed <- result
		completionErrors <- err
	}()
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls after caller cancellation = %d, want 1", calls)
	}
	close(source.release)
	if err := <-completionErrors; err != nil {
		t.Fatalf("Status after cancellation: %v", err)
	}
	if result := <-completed; result.kind != updateStatusAvailable {
		t.Fatalf("result kind = %d, want available", result.kind)
	}
}

func TestUpdateStatusServiceCloseWakesPendingCallers(t *testing.T) {
	source := newControlledReleaseSource(releaseMetadata{Version: "1.2.0"}, nil)
	service := newUpdateStatusService("1.1.0", false, source, time.Now)

	statusErrors := make(chan error, 1)
	go func() {
		_, err := service.status(context.Background())
		statusErrors <- err
	}()
	<-source.started
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-statusErrors; !errors.Is(err, ErrUpdateStatusServiceClosed) {
		t.Fatalf("pending Status error = %v, want ErrUpdateStatusServiceClosed", err)
	}
	if _, err := service.status(context.Background()); !errors.Is(err, ErrUpdateStatusServiceClosed) {
		t.Fatalf("post-close Status error = %v, want ErrUpdateStatusServiceClosed", err)
	}
}

func TestUpdateStatusServiceClassifiesReleaseSourceFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want updateStatusKind
	}{
		{name: "transport", err: &releaseTransportError{Cause: errors.New("offline")}, want: updateStatusCheckUnavailable},
		{name: "timeout", err: context.DeadlineExceeded, want: updateStatusCheckUnavailable},
		{name: "http", err: &releaseHTTPStatusError{StatusCode: 503, Status: "503 Service Unavailable"}, want: updateStatusCheckFailed},
		{name: "metadata", err: &releaseMetadataError{Cause: errors.New("invalid release metadata")}, want: updateStatusCheckFailed},
		{name: "internal", err: errors.New("unexpected release source failure"), want: updateStatusCheckFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newUpdateStatusService("1.1.0", false, &countingReleaseSource{err: test.err}, time.Now)
			t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })
			result, err := service.status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if result.kind != test.want {
				t.Fatalf("kind = %d, want %d", result.kind, test.want)
			}
			hasFailure := result.cause != ""
			if hasFailure != (test.want == updateStatusCheckFailed) {
				t.Fatalf("failure present = %v, want %v", hasFailure, test.want == updateStatusCheckFailed)
			}
		})
	}
}

func TestUpdateStatusServiceReportsDevelopmentAndInvalidVersions(t *testing.T) {
	t.Run("development build", func(t *testing.T) {
		service := newUpdateStatusService("dev", false, &countingReleaseSource{metadata: releaseMetadata{Version: "1.2.0"}}, time.Now)
		t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })
		result, err := service.status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if result.kind != updateStatusAvailable || result.current != "0.0.0" || result.latest != "1.2.0" {
			t.Fatalf("development result = %#v", result)
		}
	})

	t.Run("overflowed configured version", func(t *testing.T) {
		source := &countingReleaseSource{metadata: releaseMetadata{Version: "1.2.0"}}
		service := newUpdateStatusService("18446744073709551616.0.0", false, source, time.Now)
		t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })
		result, err := service.status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if result.kind != updateStatusCheckFailed {
			t.Fatalf("kind = %d, want check_failed", result.kind)
		}
		if calls := source.calls.Load(); calls != 0 {
			t.Fatalf("release calls = %d, want 0", calls)
		}
	})

	t.Run("overflowed release version", func(t *testing.T) {
		service := newUpdateStatusService("1.1.0", false, &countingReleaseSource{metadata: releaseMetadata{Version: "1.18446744073709551616.0"}}, time.Now)
		t.Cleanup(func() { requireUpdateStatusServiceClosed(t, service) })
		result, err := service.status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if result.kind != updateStatusCheckFailed {
			t.Fatalf("kind = %d, want check_failed", result.kind)
		}
	})
}

type countingReleaseSource struct {
	calls    atomic.Int32
	metadata releaseMetadata
	err      error
}

func (s *countingReleaseSource) LatestRelease(context.Context) (releaseMetadata, error) {
	s.calls.Add(1)
	return s.metadata, s.err
}

type sequenceReleaseSource struct {
	calls   atomic.Int32
	mu      sync.Mutex
	results []releaseMetadata
}

func (s *sequenceReleaseSource) LatestRelease(context.Context) (releaseMetadata, error) {
	call := s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.results[int(call)-1], nil
}

type blockingReleaseSource struct{}

func (*blockingReleaseSource) LatestRelease(ctx context.Context) (releaseMetadata, error) {
	<-ctx.Done()
	return releaseMetadata{}, ctx.Err()
}

type controlledReleaseSource struct {
	calls    atomic.Int32
	started  chan struct{}
	release  chan struct{}
	metadata releaseMetadata
	err      error
	once     sync.Once
}

func newControlledReleaseSource(metadata releaseMetadata, err error) *controlledReleaseSource {
	return &controlledReleaseSource{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		metadata: metadata,
		err:      err,
	}
}

func (s *controlledReleaseSource) LatestRelease(ctx context.Context) (releaseMetadata, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.metadata, s.err
	case <-ctx.Done():
		return releaseMetadata{}, ctx.Err()
	}
}

func requireUpdateStatusServiceClosed(t *testing.T, service *UpdateStatusService) {
	t.Helper()
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
