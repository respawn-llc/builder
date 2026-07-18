package serverstatus

import (
	"context"
	"errors"
	"math"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/invariant"
	"core/shared/serverapi"
)

func TestUpdateStatusServiceDoesNotCheckBeforeFirstRequest(t *testing.T) {
	source := &countingReleaseSource{metadata: ReleaseMetadata{Version: "1.2.0"}}
	service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
	t.Cleanup(func() { closeUpdateStatusService(t, service) })

	if calls := source.calls.Load(); calls != 0 {
		t.Fatalf("release calls after construction = %d, want 0", calls)
	}
}

func TestUpdateStatusServiceClassifiesCompletedChecks(t *testing.T) {
	transportFailure := &ReleaseTransportError{Cause: errors.New("network unavailable")}
	tests := []struct {
		name           string
		currentVersion string
		metadata       ReleaseMetadata
		sourceError    error
		wantKind       serverapi.UpdateStatusResultKind
		wantCurrent    string
		wantLatest     string
		wantCause      bool
		wantCalls      int32
	}{
		{
			name:           "current",
			currentVersion: "1.2.0",
			metadata:       ReleaseMetadata{Version: "1.2.0"},
			wantKind:       serverapi.UpdateStatusCurrent,
			wantCurrent:    "1.2.0",
			wantLatest:     "1.2.0",
			wantCalls:      1,
		},
		{
			name:           "available",
			currentVersion: "1.1.0",
			metadata:       ReleaseMetadata{Version: "1.2.0"},
			wantKind:       serverapi.UpdateStatusAvailable,
			wantCurrent:    "1.1.0",
			wantLatest:     "1.2.0",
			wantCalls:      1,
		},
		{
			name:           "development build",
			currentVersion: "dev",
			metadata:       ReleaseMetadata{Version: "1.2.0"},
			wantKind:       serverapi.UpdateStatusAvailable,
			wantCurrent:    "0.0.0",
			wantLatest:     "1.2.0",
			wantCalls:      1,
		},
		{
			name:           "malformed current version",
			currentVersion: "release",
			metadata:       ReleaseMetadata{Version: "1.2.0"},
			wantKind:       serverapi.UpdateStatusCheckFailed,
			wantCause:      true,
			wantCalls:      0,
		},
		{
			name:           "http status",
			currentVersion: "1.1.0",
			sourceError:    &ReleaseHTTPStatusError{StatusCode: 403, Status: "403 Forbidden"},
			wantKind:       serverapi.UpdateStatusCheckFailed,
			wantCause:      true,
			wantCalls:      1,
		},
		{
			name:           "invalid release metadata",
			currentVersion: "1.1.0",
			sourceError:    &ReleaseMetadataError{Cause: errors.New("latest release tag is invalid")},
			wantKind:       serverapi.UpdateStatusCheckFailed,
			wantCause:      true,
			wantCalls:      1,
		},
		{
			name:           "outbound network failure",
			currentVersion: "1.1.0",
			sourceError:    transportFailure,
			wantKind:       serverapi.UpdateStatusCheckUnavailable,
			wantCalls:      1,
		},
		{
			name:           "bounded timeout",
			currentVersion: "1.1.0",
			sourceError:    context.DeadlineExceeded,
			wantKind:       serverapi.UpdateStatusCheckUnavailable,
			wantCalls:      1,
		},
		{
			name:           "invalid latest version from alternate source",
			currentVersion: "1.1.0",
			metadata:       ReleaseMetadata{Version: "latest"},
			wantKind:       serverapi.UpdateStatusCheckFailed,
			wantCause:      true,
			wantCalls:      1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &countingReleaseSource{metadata: test.metadata, err: test.sourceError}
			service := NewUpdateStatusService(test.currentVersion, Dependencies{ReleaseSource: source})
			t.Cleanup(func() { closeUpdateStatusService(t, service) })

			result, err := service.Status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if result.Kind() != test.wantKind {
				t.Fatalf("kind = %q, want %q", result.Kind(), test.wantKind)
			}
			if current, latest, ok := result.Versions(); ok {
				if current != test.wantCurrent || latest != test.wantLatest {
					t.Fatalf("versions = %q/%q, want %q/%q", current, latest, test.wantCurrent, test.wantLatest)
				}
			} else if test.wantCurrent != "" || test.wantLatest != "" {
				t.Fatal("result omitted expected versions")
			}
			_, causePresent := result.FailureCause()
			if causePresent != test.wantCause {
				t.Fatalf("failure cause present = %v, want %v", causePresent, test.wantCause)
			}
			if calls := source.calls.Load(); calls != test.wantCalls {
				t.Fatalf("release calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestUpdateStatusServiceUsesBoundedAttemptTimeout(t *testing.T) {
	source := &blockingUntilContextReleaseSource{started: make(chan struct{})}
	service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
	t.Cleanup(func() { closeUpdateStatusService(t, service) })

	result, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result.Kind() != serverapi.UpdateStatusCheckUnavailable {
		t.Fatalf("kind = %q, want check_unavailable", result.Kind())
	}
	if !source.deadlineObserved.Load() {
		t.Fatal("release source did not receive a bounded context deadline")
	}
}

func TestUpdateStatusCacheFreshnessBoundary(t *testing.T) {
	completedAt := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	if !isFreshUpdateStatusCache(completedAt, completedAt) {
		t.Fatal("cache was stale at completion time")
	}
	if !isFreshUpdateStatusCache(completedAt.Add(time.Hour-time.Nanosecond), completedAt) {
		t.Fatal("cache was stale before the one-hour boundary")
	}
	if isFreshUpdateStatusCache(completedAt.Add(time.Hour), completedAt) {
		t.Fatal("cache remained fresh at the one-hour boundary")
	}
	if isFreshUpdateStatusCache(completedAt.Add(time.Hour+time.Nanosecond), completedAt) {
		t.Fatal("cache remained fresh after the one-hour boundary")
	}
}

func TestUpdateStatusServiceCachesEveryCompletedOutcomeForOneWindow(t *testing.T) {
	tests := []struct {
		name     string
		metadata ReleaseMetadata
		err      error
	}{
		{name: "current", metadata: ReleaseMetadata{Version: "1.1.0"}},
		{name: "available", metadata: ReleaseMetadata{Version: "1.2.0"}},
		{name: "network unavailable", err: &ReleaseTransportError{Cause: errors.New("offline")}},
		{name: "http failure", err: &ReleaseHTTPStatusError{StatusCode: 429, Status: "429 Too Many Requests"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &countingReleaseSource{metadata: test.metadata, err: test.err}
			service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
			t.Cleanup(func() { closeUpdateStatusService(t, service) })

			first, err := service.Status(context.Background())
			if err != nil {
				t.Fatalf("first Status: %v", err)
			}
			second, err := service.Status(context.Background())
			if err != nil {
				t.Fatalf("second Status: %v", err)
			}
			if first.Kind() != second.Kind() {
				t.Fatalf("cached kind = %q, want %q", second.Kind(), first.Kind())
			}
			if calls := source.calls.Load(); calls != 1 {
				t.Fatalf("release calls = %d, want 1", calls)
			}
		})
	}
}

func TestUpdateStatusServiceRefreshesExpiredCacheExactlyOnce(t *testing.T) {
	source := &sequenceReleaseSource{
		results: []ReleaseMetadata{{Version: "1.1.0"}, {Version: "1.2.0"}},
	}
	service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
	t.Cleanup(func() { closeUpdateStatusService(t, service) })

	first, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("first Status: %v", err)
	}
	if first.Kind() != serverapi.UpdateStatusCurrent {
		t.Fatalf("first kind = %q, want current", first.Kind())
	}
	expireUpdateStatusCache(service)

	second, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("second Status: %v", err)
	}
	if second.Kind() != serverapi.UpdateStatusAvailable {
		t.Fatalf("second kind = %q, want available", second.Kind())
	}
	third, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("third Status: %v", err)
	}
	if third.Kind() != serverapi.UpdateStatusAvailable {
		t.Fatalf("third kind = %q, want available", third.Kind())
	}
	if calls := source.calls.Load(); calls != 2 {
		t.Fatalf("release calls = %d, want 2", calls)
	}
}

func TestUpdateStatusServiceCoalescesConcurrentCallers(t *testing.T) {
	source := newControlledReleaseSource(ReleaseMetadata{Version: "1.2.0"}, nil)
	service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
	t.Cleanup(func() { closeUpdateStatusService(t, service) })

	const callers = 12
	start := make(chan struct{})
	results := make(chan serverapi.UpdateStatusResult, callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			result, err := service.Status(context.Background())
			results <- result
			errs <- err
		}()
	}
	close(start)
	<-source.started
	waitForUpdateStatusWaiters(t, service, callers)
	close(source.release)

	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("Status: %v", err)
		}
		if result := <-results; result.Kind() != serverapi.UpdateStatusAvailable {
			t.Fatalf("kind = %q, want available", result.Kind())
		}
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls = %d, want 1", calls)
	}
}

func TestUpdateStatusCallerCancellationLeavesSharedAttemptRunning(t *testing.T) {
	source := newControlledReleaseSource(ReleaseMetadata{Version: "1.2.0"}, nil)
	service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
	t.Cleanup(func() { closeUpdateStatusService(t, service) })

	initiatorContext, cancelInitiator := context.WithCancel(context.Background())
	initiatorResult := make(chan error, 1)
	go func() {
		_, err := service.Status(initiatorContext)
		initiatorResult <- err
	}()
	<-source.started

	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	waiterResult := make(chan error, 1)
	go func() {
		_, err := service.Status(waiterContext)
		waiterResult <- err
	}()
	waitForUpdateStatusWaiters(t, service, 2)

	cancelInitiator()
	cancelWaiter()
	if err := <-initiatorResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("initiator error = %v, want context canceled", err)
	}
	if err := <-waiterResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error = %v, want context canceled", err)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls before completion = %d, want 1", calls)
	}

	close(source.release)
	result, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after canceled callers: %v", err)
	}
	if result.Kind() != serverapi.UpdateStatusAvailable {
		t.Fatalf("kind = %q, want available", result.Kind())
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls after completion = %d, want 1", calls)
	}
}

func TestUpdateStatusServiceCloseWinsOverInFlightPublication(t *testing.T) {
	source := newControlledReleaseSource(ReleaseMetadata{Version: "1.2.0"}, nil)
	service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})

	initiator := make(chan error, 1)
	waiter := make(chan error, 1)
	go func() {
		_, err := service.Status(context.Background())
		initiator <- err
	}()
	<-source.started
	go func() {
		_, err := service.Status(context.Background())
		waiter <- err
	}()
	waitForUpdateStatusWaiters(t, service, 2)

	closeReturned := make(chan error, 1)
	go func() {
		closeReturned <- service.Close()
	}()
	waitForUpdateStatusServiceClosed(t, service)
	close(source.release)

	if err := <-closeReturned; err != nil {
		t.Fatalf("Close: %v", err)
	}
	for name, result := range map[string]<-chan error{"initiator": initiator, "waiter": waiter} {
		if err := <-result; !errors.Is(err, ErrUpdateStatusServiceClosed) {
			t.Fatalf("%s error = %v, want ErrUpdateStatusServiceClosed", name, err)
		}
	}
	if _, err := service.Status(context.Background()); !errors.Is(err, ErrUpdateStatusServiceClosed) {
		t.Fatalf("post-close Status error = %v, want ErrUpdateStatusServiceClosed", err)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release calls = %d, want 1", calls)
	}
	assertNoReusableUpdateStatusState(t, service)
}

func TestUpdateStatusImpossibleTerminalUsesInvariantPolicy(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		t.Setenv("KENT_INVARIANT_MODE", "panic")
		service := NewUpdateStatusService("1.1.0", Dependencies{
			ReleaseSource: &countingReleaseSource{metadata: ReleaseMetadata{Version: "1.2.0"}},
		})
		installIncompleteTerminal(service)

		defer func() {
			recovered := recover()
			if recovered == nil {
				t.Fatal("expected invariant panic")
			}
			diagnostic, ok := recovered.(invariant.Diagnostic)
			if !ok {
				t.Fatalf("panic payload = %T, want invariant.Diagnostic", recovered)
			}
			if diagnostic.Scope != invariant.ScopeUpdateStatus {
				t.Fatalf("scope = %q, want update status", diagnostic.Scope)
			}
			for _, field := range []invariant.Field{
				invariant.FieldOperation,
				invariant.FieldCurrentVersion,
				invariant.FieldCacheState,
				invariant.FieldInflightState,
				invariant.FieldInvariantError,
			} {
				if diagnostic.Fields[field] == "" {
					t.Fatalf("diagnostic field %q is blank", field)
				}
			}
			if diagnostic.Stack == "" {
				t.Fatal("diagnostic stack is blank")
			}
		}()
		_, _ = service.Status(context.Background())
	})

	t.Run("diagnostic result is cached", func(t *testing.T) {
		t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
		source := &countingReleaseSource{metadata: ReleaseMetadata{Version: "1.2.0"}}
		service := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
		t.Cleanup(func() { closeUpdateStatusService(t, service) })
		installIncompleteTerminal(service)

		first, err := service.Status(context.Background())
		if err != nil {
			t.Fatalf("first Status: %v", err)
		}
		if first.Kind() != serverapi.UpdateStatusCheckFailed {
			t.Fatalf("first kind = %q, want check_failed", first.Kind())
		}
		if cause, ok := first.FailureCause(); !ok || cause == "" {
			t.Fatal("diagnostic result omitted failure cause")
		}
		second, err := service.Status(context.Background())
		if err != nil {
			t.Fatalf("second Status: %v", err)
		}
		if second.Kind() != serverapi.UpdateStatusCheckFailed {
			t.Fatalf("second kind = %q, want cached check_failed", second.Kind())
		}
		if calls := source.calls.Load(); calls != 0 {
			t.Fatalf("release calls = %d, want 0", calls)
		}
	})
}

func TestUpdateStatusDuplicateTerminalPublicationWakesWaitersWithDiagnosticFailure(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	service := NewUpdateStatusService("1.1.0", Dependencies{
		ReleaseSource: &countingReleaseSource{metadata: ReleaseMetadata{Version: "1.2.0"}},
	})
	t.Cleanup(func() { closeUpdateStatusService(t, service) })

	operation := &updateStatusOperation{
		done: make(chan struct{}),
		terminal: updateStatusTerminal{
			result: serverapi.CurrentUpdateStatusResult("1.1.0", "1.1.0"),
			set:    true,
		},
	}
	service.mu.Lock()
	service.inflight = operation
	service.mu.Unlock()

	resultCh := make(chan serverapi.UpdateStatusResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := service.Status(context.Background())
		resultCh <- result
		errCh <- err
	}()
	waitForUpdateStatusWaiters(t, service, 1)

	service.publishUpdateStatusOperation(
		operation,
		serverapi.AvailableUpdateStatusResult("1.1.0", "1.2.0"),
	)

	if err := <-errCh; err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result := <-resultCh; result.Kind() != serverapi.UpdateStatusCheckFailed {
		t.Fatalf("kind = %q, want check_failed", result.Kind())
	}
	if calls := service.releaseSource.(*countingReleaseSource).calls.Load(); calls != 0 {
		t.Fatalf("release calls = %d, want 0", calls)
	}
}

func TestUpdateVersionParserBoundaries(t *testing.T) {
	max := strconv.FormatUint(math.MaxUint64, 10)
	for component := range 3 {
		parts := []string{"1", "1", "1"}
		parts[component] = max
		version := parts[0] + "." + parts[1] + "." + parts[2]
		parsed, err := parseUpdateVersion(version)
		if err != nil {
			t.Fatalf("component %d max parse: %v", component, err)
		}
		if parsed.components[component] != math.MaxUint64 {
			t.Fatalf("component %d = %d, want max uint64", component, parsed.components[component])
		}
	}

	tooLarge := "18446744073709551616"
	for component := range 3 {
		parts := []string{"1", "1", "1"}
		parts[component] = tooLarge
		version := parts[0] + "." + parts[1] + "." + parts[2]
		if _, err := parseUpdateVersion(version); err == nil {
			t.Fatalf("component %d accepted one above max", component)
		}
	}
	if _, err := parseUpdateVersion("999999999999999999999999999999999999999999999999999.1.1"); err == nil {
		t.Fatal("parser accepted arbitrarily long component")
	}
}

func TestUpdateVersionOverflowClassificationDoesNotWrapOrCallUnexpectedSource(t *testing.T) {
	tooLarge := "18446744073709551616"
	currentSource := &countingReleaseSource{metadata: ReleaseMetadata{Version: "1.2.0"}}
	currentService := NewUpdateStatusService(tooLarge+".0.0", Dependencies{ReleaseSource: currentSource})
	t.Cleanup(func() { closeUpdateStatusService(t, currentService) })
	current, err := currentService.Status(context.Background())
	if err != nil {
		t.Fatalf("current overflow Status: %v", err)
	}
	if current.Kind() != serverapi.UpdateStatusCheckFailed {
		t.Fatalf("current overflow kind = %q, want check_failed", current.Kind())
	}
	if calls := currentSource.calls.Load(); calls != 0 {
		t.Fatalf("current overflow release calls = %d, want 0", calls)
	}

	latestSource := &countingReleaseSource{metadata: ReleaseMetadata{Version: "1." + tooLarge + ".0"}}
	latestService := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: latestSource})
	t.Cleanup(func() { closeUpdateStatusService(t, latestService) })
	latest, err := latestService.Status(context.Background())
	if err != nil {
		t.Fatalf("latest overflow Status: %v", err)
	}
	if latest.Kind() != serverapi.UpdateStatusCheckFailed {
		t.Fatalf("latest overflow kind = %q, want check_failed", latest.Kind())
	}
	if calls := latestSource.calls.Load(); calls != 1 {
		t.Fatalf("latest overflow release calls = %d, want 1", calls)
	}
}

type countingReleaseSource struct {
	calls    atomic.Int32
	metadata ReleaseMetadata
	err      error
}

func (s *countingReleaseSource) LatestRelease(context.Context) (ReleaseMetadata, error) {
	s.calls.Add(1)
	return s.metadata, s.err
}

type sequenceReleaseSource struct {
	calls   atomic.Int32
	mu      sync.Mutex
	results []ReleaseMetadata
}

func (s *sequenceReleaseSource) LatestRelease(context.Context) (ReleaseMetadata, error) {
	call := s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	index := int(call - 1)
	if index >= len(s.results) {
		index = len(s.results) - 1
	}
	return s.results[index], nil
}

type controlledReleaseSource struct {
	calls    atomic.Int32
	started  chan struct{}
	release  chan struct{}
	metadata ReleaseMetadata
	err      error
	once     sync.Once
}

func newControlledReleaseSource(metadata ReleaseMetadata, err error) *controlledReleaseSource {
	return &controlledReleaseSource{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		metadata: metadata,
		err:      err,
	}
}

func (s *controlledReleaseSource) LatestRelease(ctx context.Context) (ReleaseMetadata, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.metadata, s.err
	case <-ctx.Done():
		return ReleaseMetadata{}, ctx.Err()
	}
}

type blockingUntilContextReleaseSource struct {
	started          chan struct{}
	once             sync.Once
	deadlineObserved atomic.Bool
}

func (s *blockingUntilContextReleaseSource) LatestRelease(ctx context.Context) (ReleaseMetadata, error) {
	if _, ok := ctx.Deadline(); ok {
		s.deadlineObserved.Store(true)
	}
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return ReleaseMetadata{}, ctx.Err()
}

func expireUpdateStatusCache(service *UpdateStatusService) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.completed.completedAt = time.Now().Add(-updateStatusFreshness)
}

func waitForUpdateStatusWaiters(t *testing.T, service *UpdateStatusService, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		service.mu.Lock()
		got := 0
		if service.inflight != nil {
			got = service.inflight.waiters
		}
		service.mu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("in-flight waiters = %d, want at least %d", got, want)
		}
		runtime.Gosched()
	}
}

func waitForUpdateStatusServiceClosed(t *testing.T, service *UpdateStatusService) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		service.mu.Lock()
		closed := service.closed
		service.mu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not enter closed state")
		}
		runtime.Gosched()
	}
}

func assertNoReusableUpdateStatusState(t *testing.T, service *UpdateStatusService) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.completed != nil {
		t.Fatal("closed service retained a reusable cache entry")
	}
	if service.inflight != nil {
		t.Fatal("closed service retained an in-flight operation")
	}
}

func installIncompleteTerminal(service *UpdateStatusService) {
	done := make(chan struct{})
	close(done)
	service.mu.Lock()
	service.inflight = &updateStatusOperation{
		done:    done,
		waiters: 0,
	}
	service.mu.Unlock()
}

func closeUpdateStatusService(t *testing.T, service *UpdateStatusService) {
	t.Helper()
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
