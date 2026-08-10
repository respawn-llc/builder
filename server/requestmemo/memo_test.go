package requestmemo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoDoesNotReplayCanceledOrDeadlineExceededOutcome(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
	}{
		{
			name:    "canceled",
			wantErr: context.Canceled,
		},
		{
			name:    "deadline exceeded",
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			memo := New[string, string]()
			calls := 0

			first, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
				return a == b
			}, func(context.Context) (string, error) {
				calls++
				return "", tt.wantErr
			})
			if err != tt.wantErr {
				t.Fatalf("first error = %v, want %v", err, tt.wantErr)
			}
			if first != "" {
				t.Fatalf("first response = %q, want empty", first)
			}

			second, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
				return a == b
			}, func(context.Context) (string, error) {
				calls++
				return "ok", nil
			})
			if err != nil {
				t.Fatalf("second error = %v", err)
			}
			if second != "ok" {
				t.Fatalf("second response = %q, want ok", second)
			}
			if calls != 2 {
				t.Fatalf("run calls = %d, want 2", calls)
			}
		})
	}
}

func TestMemoRequiresConfiguredOwner(t *testing.T) {
	var memo *Memo[string, string]
	runCalls := 0

	resp, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		runCalls++
		return "unexpected", nil
	})

	if !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrOwnerUnavailable)
	}
	if resp != "" {
		t.Fatalf("response = %q, want empty", resp)
	}
	if runCalls != 0 {
		t.Fatalf("run calls = %d, want 0", runCalls)
	}
}

func TestMemoDuplicateReplaysOneCompletedOwner(t *testing.T) {
	memo := New[string, string]()
	runCalls := 0

	first, err := memo.Do(
		context.Background(),
		"req-1",
		"same",
		equalString,
		func(context.Context) (string, error) {
			runCalls++
			return "completed", nil
		},
	)
	if err != nil {
		t.Fatalf("first error = %v", err)
	}

	second, err := memo.Do(
		context.Background(),
		"req-1",
		"same",
		equalString,
		func(context.Context) (string, error) {
			runCalls++
			return "unexpected", nil
		},
	)
	if err != nil {
		t.Fatalf("second error = %v", err)
	}
	if first != "completed" || second != first {
		t.Fatalf("responses = %q, %q, want matching completed outcome", first, second)
	}
	if runCalls != 1 {
		t.Fatalf("run calls = %d, want 1", runCalls)
	}
}

func TestMemoAdmittedWorkRequiresConfiguredOwner(t *testing.T) {
	var memo *Memo[string, string]
	prepareCalls := 0

	resp, err := memo.DoAdmitted(
		context.Background(),
		"req-1",
		"same",
		func(a string, b string) bool { return a == b },
		func(context.Context, *Admission[string]) {
			prepareCalls++
		},
	)

	if !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrOwnerUnavailable)
	}
	if resp != "" {
		t.Fatalf("response = %q, want empty", resp)
	}
	if prepareCalls != 0 {
		t.Fatalf("prepare calls = %d, want 0", prepareCalls)
	}
}

func TestMemoAdmittedWorkRequiresPreparationResolution(t *testing.T) {
	memo := New[string, string]()

	resp, err := memo.DoAdmitted(
		context.Background(),
		"req-1",
		"same",
		equalString,
		func(context.Context, *Admission[string]) {},
	)

	if !errors.Is(err, ErrAdmissionUnresolved) {
		t.Fatalf("error = %v, want %v", err, ErrAdmissionUnresolved)
	}
	if resp != "" {
		t.Fatalf("response = %q, want empty", resp)
	}
}

func TestMemoAdmittedWorkDuplicateJoinsOneOwner(t *testing.T) {
	memo := New[string, string]()
	workReady := make(chan *AdmittedWork[string], 1)
	prepareCalls := 0

	call := func() <-chan struct {
		resp string
		err  error
	} {
		result := make(chan struct {
			resp string
			err  error
		}, 1)
		go func() {
			resp, err := memo.DoAdmitted(
				context.Background(),
				"req-1",
				"same",
				func(a string, b string) bool { return a == b },
				func(_ context.Context, admission *Admission[string]) {
					prepareCalls++
					work, admitErr := admission.Admit()
					if admitErr != nil {
						t.Errorf("Admit: %v", admitErr)
						return
					}
					workReady <- work
				},
			)
			result <- struct {
				resp string
				err  error
			}{resp: resp, err: err}
		}()
		return result
	}

	first := call()
	work := <-workReady
	second := call()
	if err := work.Complete("done", nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	for i, result := range []<-chan struct {
		resp string
		err  error
	}{first, second} {
		got := <-result
		if got.err != nil {
			t.Fatalf("call %d error = %v", i+1, got.err)
		}
		if got.resp != "done" {
			t.Fatalf("call %d response = %q, want done", i+1, got.resp)
		}
	}
	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
	}
}

func TestMemoAdmittedWorkReplaysCompletedErrorOutcome(t *testing.T) {
	memo := New[string, string]()
	terminalErr := errors.New("terminal outcome")
	prepareCalls := 0

	first, err := memo.DoAdmitted(
		context.Background(),
		"req-1",
		"same",
		equalString,
		func(_ context.Context, admission *Admission[string]) {
			prepareCalls++
			work, admitErr := admission.Admit()
			if admitErr != nil {
				t.Errorf("Admit: %v", admitErr)
				return
			}
			if completeErr := work.Complete("completed", terminalErr); completeErr != nil {
				t.Errorf("Complete: %v", completeErr)
			}
		},
	)
	if !errors.Is(err, terminalErr) {
		t.Fatalf("first error = %v, want %v", err, terminalErr)
	}

	second, err := memo.DoAdmitted(
		context.Background(),
		"req-1",
		"same",
		equalString,
		func(context.Context, *Admission[string]) {
			prepareCalls++
		},
	)
	if !errors.Is(err, terminalErr) {
		t.Fatalf("second error = %v, want %v", err, terminalErr)
	}
	if first != "completed" || second != first {
		t.Fatalf("responses = %q, %q, want matching completed outcome", first, second)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
	}
}

func TestMemoAdmittedWorkOutlivesCanceledWaiter(t *testing.T) {
	memo := New[string, string]()
	workReady := make(chan *AdmittedWork[string], 1)
	var prepareCalls atomic.Int32

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan memoStringResult, 1)
	go func() {
		resp, err := memo.DoAdmitted(
			firstCtx,
			"req-1",
			"same",
			equalString,
			func(_ context.Context, admission *Admission[string]) {
				prepareCalls.Add(1)
				work, admitErr := admission.Admit()
				if admitErr != nil {
					t.Errorf("Admit: %v", admitErr)
					return
				}
				workReady <- work
			},
		)
		firstDone <- memoStringResult{resp: resp, err: err}
	}()
	work := <-workReady
	cancelFirst()
	first := awaitMemoStringResult(t, firstDone)
	if !errors.Is(first.err, context.Canceled) {
		t.Fatalf("first waiter error = %v, want %v", first.err, context.Canceled)
	}

	secondDone := make(chan memoStringResult, 1)
	go func() {
		resp, err := memo.DoAdmitted(
			context.Background(),
			"req-1",
			"same",
			equalString,
			func(context.Context, *Admission[string]) {
				prepareCalls.Add(1)
			},
		)
		secondDone <- memoStringResult{resp: resp, err: err}
	}()
	if err := work.Complete("completed", nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	second := awaitMemoStringResult(t, secondDone)
	if second.err != nil {
		t.Fatalf("second waiter error = %v", second.err)
	}
	if second.resp != "completed" {
		t.Fatalf("second waiter response = %q, want completed", second.resp)
	}
	if prepareCalls.Load() != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls.Load())
	}
}

func TestMemoAdmittedWorkRejectsReusedIdentityWithDifferentPayload(t *testing.T) {
	memo := New[string, string]()
	workReady := make(chan *AdmittedWork[string], 1)
	firstDone := make(chan error, 1)
	go func() {
		_, err := memo.DoAdmitted(
			context.Background(),
			"req-1",
			"original",
			func(a string, b string) bool { return a == b },
			func(_ context.Context, admission *Admission[string]) {
				work, admitErr := admission.Admit()
				if admitErr != nil {
					t.Errorf("Admit: %v", admitErr)
					return
				}
				workReady <- work
			},
		)
		firstDone <- err
	}()
	work := <-workReady

	prepareCalls := 0
	resp, err := memo.DoAdmitted(
		context.Background(),
		"req-1",
		"changed",
		func(a string, b string) bool { return a == b },
		func(context.Context, *Admission[string]) {
			prepareCalls++
		},
	)

	if !errors.Is(err, ErrClientRequestIDReused) {
		t.Fatalf("error = %v, want %v", err, ErrClientRequestIDReused)
	}
	if resp != "" {
		t.Fatalf("response = %q, want empty", resp)
	}
	if prepareCalls != 0 {
		t.Fatalf("prepare calls = %d, want 0", prepareCalls)
	}
	if err := work.Complete("done", nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first call error = %v", err)
	}
}

func TestMemoAdmittedWorkPreEffectRejectionDeletesIdentity(t *testing.T) {
	memo := New[string, string]()
	rejected := errors.New("not admitted")
	prepareCalls := 0

	resp, err := memo.DoAdmitted(
		context.Background(),
		"req-1",
		"same",
		func(a string, b string) bool { return a == b },
		func(_ context.Context, admission *Admission[string]) {
			prepareCalls++
			if prepareCalls == 1 {
				if rejectErr := admission.Reject(rejected); rejectErr != nil {
					t.Errorf("Reject: %v", rejectErr)
				}
				return
			}
			if completeErr := admission.Complete("unexpected retry", nil); completeErr != nil {
				t.Errorf("Complete: %v", completeErr)
			}
		},
	)

	if !errors.Is(err, rejected) {
		t.Fatalf("error = %v, want %v", err, rejected)
	}
	if resp != "" {
		t.Fatalf("response = %q, want empty", resp)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
	}

	retried, err := memo.DoAdmitted(
		context.Background(),
		"req-1",
		"same",
		func(a string, b string) bool { return a == b },
		func(_ context.Context, admission *Admission[string]) {
			prepareCalls++
			if completeErr := admission.Complete("retried", nil); completeErr != nil {
				t.Errorf("Complete: %v", completeErr)
			}
		},
	)
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if retried != "retried" {
		t.Fatalf("retry response = %q, want retried", retried)
	}
	if prepareCalls != 2 {
		t.Fatalf("prepare calls after retry = %d, want 2", prepareCalls)
	}
}

func TestMemoDoesNotReplayGenericErrorOutcome(t *testing.T) {
	memo := New[string, string]()
	calls := 0

	first, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		calls++
		return "", errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("first error = %v, want boom", err)
	}
	if first != "" {
		t.Fatalf("first response = %q, want empty", first)
	}

	second, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("second error = %v", err)
	}
	if second != "ok" {
		t.Fatalf("second response = %q, want ok", second)
	}
	if calls != 2 {
		t.Fatalf("run calls = %d, want 2", calls)
	}
}

func TestMemoRerunsWaitingRetryAfterNonMemoizedInflightCompletion(t *testing.T) {
	memo := New[string, string]()
	started := make(chan struct{})
	allowCancel := make(chan struct{})
	firstDone := make(chan error, 1)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	go func() {
		_, err := memo.Do(firstCtx, "req-1", "same", func(a string, b string) bool {
			return a == b
		}, func(ctx context.Context) (string, error) {
			close(started)
			<-allowCancel
			return "", ctx.Err()
		})
		firstDone <- err
	}()

	<-started
	secondDone := make(chan struct {
		resp string
		err  error
	}, 1)
	go func() {
		resp, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
			return a == b
		}, func(context.Context) (string, error) {
			return "ok", nil
		})
		secondDone <- struct {
			resp string
			err  error
		}{resp: resp, err: err}
	}()

	close(allowCancel)
	cancelFirst()
	if err := <-firstDone; err != context.Canceled {
		t.Fatalf("first error = %v, want canceled", err)
	}
	second := <-secondDone
	if second.err != nil {
		t.Fatalf("second error = %v", second.err)
	}
	if second.resp != "ok" {
		t.Fatalf("second response = %q, want ok", second.resp)
	}
}

func TestMemoSaturationPreservesExistingIdentityAndRejectsOnlyNewWork(t *testing.T) {
	memo := New[string, string]()
	held := saturateOrdinaryMemo(t, memo)
	defer completeHeldRequests(held)
	existing := held[0]

	var duplicateRunCalls atomic.Int32
	duplicateCtx, cancelDuplicate := context.WithCancel(context.Background())
	duplicateDone := make(chan memoStringResult, 1)
	go func() {
		resp, err := memo.Do(
			duplicateCtx,
			existing.requestID,
			existing.payload,
			equalString,
			func(context.Context) (string, error) {
				duplicateRunCalls.Add(1)
				return "unexpected", nil
			},
		)
		duplicateDone <- memoStringResult{resp: resp, err: err}
	}()

	var mismatchRunCalls atomic.Int32
	resp, err := memo.Do(
		context.Background(),
		existing.requestID,
		"different",
		equalString,
		func(context.Context) (string, error) {
			mismatchRunCalls.Add(1)
			return "unexpected", nil
		},
	)
	if !errors.Is(err, ErrClientRequestIDReused) {
		t.Fatalf("mismatched duplicate error = %v, want %v", err, ErrClientRequestIDReused)
	}
	if resp != "" {
		t.Fatalf("mismatched duplicate response = %q, want empty", resp)
	}

	var newRunCalls atomic.Int32
	resp, err = memo.Do(
		context.Background(),
		"new-at-capacity",
		"new",
		equalString,
		func(context.Context) (string, error) {
			newRunCalls.Add(1)
			return "unexpected", nil
		},
	)
	if !errors.Is(err, ErrCapacityUnavailable) {
		t.Fatalf("new identity error = %v, want %v", err, ErrCapacityUnavailable)
	}
	if resp != "" {
		t.Fatalf("new identity response = %q, want empty", resp)
	}

	cancelDuplicate()
	duplicate := awaitMemoStringResult(t, duplicateDone)
	if !errors.Is(duplicate.err, context.Canceled) {
		t.Fatalf("matching duplicate error = %v, want %v", duplicate.err, context.Canceled)
	}
	if duplicateRunCalls.Load() != 0 || mismatchRunCalls.Load() != 0 || newRunCalls.Load() != 0 {
		t.Fatalf(
			"unexpected callbacks: duplicate=%d mismatch=%d new=%d",
			duplicateRunCalls.Load(),
			mismatchRunCalls.Load(),
			newRunCalls.Load(),
		)
	}
}

func TestMemoCompletedOutcomeAdmitsLaterIdentityAtCapacity(t *testing.T) {
	memo := New[string, string]()
	held := saturateOrdinaryMemo(t, memo)
	defer completeHeldRequests(held)

	held[0].complete()
	completed := awaitMemoStringResult(t, held[0].done)
	if completed.err != nil {
		t.Fatalf("completed request error = %v", completed.err)
	}

	runCalls := 0
	resp, err := memo.Do(
		context.Background(),
		"new-after-completion",
		"new",
		equalString,
		func(context.Context) (string, error) {
			runCalls++
			return "fresh", nil
		},
	)
	if err != nil {
		t.Fatalf("new identity error = %v", err)
	}
	if resp != "fresh" {
		t.Fatalf("new identity response = %q, want fresh", resp)
	}
	if runCalls != 1 {
		t.Fatalf("run calls = %d, want 1", runCalls)
	}
}

func TestMemoAdmittedWorkSaturationPreservesExistingIdentityAndRejectsOnlyNewWork(t *testing.T) {
	memo := New[string, string]()
	held := saturateAdmittedMemo(t, memo)
	defer completeHeldRequests(held)
	existing := held[0]

	var duplicatePrepareCalls atomic.Int32
	duplicateCtx, cancelDuplicate := context.WithCancel(context.Background())
	duplicateDone := make(chan memoStringResult, 1)
	go func() {
		resp, err := memo.DoAdmitted(
			duplicateCtx,
			existing.requestID,
			existing.payload,
			equalString,
			func(context.Context, *Admission[string]) {
				duplicatePrepareCalls.Add(1)
			},
		)
		duplicateDone <- memoStringResult{resp: resp, err: err}
	}()

	var mismatchPrepareCalls atomic.Int32
	resp, err := memo.DoAdmitted(
		context.Background(),
		existing.requestID,
		"different",
		equalString,
		func(context.Context, *Admission[string]) {
			mismatchPrepareCalls.Add(1)
		},
	)
	if !errors.Is(err, ErrClientRequestIDReused) {
		t.Fatalf("mismatched duplicate error = %v, want %v", err, ErrClientRequestIDReused)
	}
	if resp != "" {
		t.Fatalf("mismatched duplicate response = %q, want empty", resp)
	}

	var newPrepareCalls atomic.Int32
	resp, err = memo.DoAdmitted(
		context.Background(),
		"new-at-capacity",
		"new",
		equalString,
		func(context.Context, *Admission[string]) {
			newPrepareCalls.Add(1)
		},
	)
	if !errors.Is(err, ErrCapacityUnavailable) {
		t.Fatalf("new identity error = %v, want %v", err, ErrCapacityUnavailable)
	}
	if resp != "" {
		t.Fatalf("new identity response = %q, want empty", resp)
	}

	cancelDuplicate()
	duplicate := awaitMemoStringResult(t, duplicateDone)
	if !errors.Is(duplicate.err, context.Canceled) {
		t.Fatalf("matching duplicate error = %v, want %v", duplicate.err, context.Canceled)
	}
	if duplicatePrepareCalls.Load() != 0 || mismatchPrepareCalls.Load() != 0 || newPrepareCalls.Load() != 0 {
		t.Fatalf(
			"unexpected preparations: duplicate=%d mismatch=%d new=%d",
			duplicatePrepareCalls.Load(),
			mismatchPrepareCalls.Load(),
			newPrepareCalls.Load(),
		)
	}
}

func TestMemoAdmittedWorkCompletionAdmitsLaterIdentityAtCapacity(t *testing.T) {
	memo := New[string, string]()
	held := saturateAdmittedMemo(t, memo)
	defer completeHeldRequests(held)

	held[0].complete()
	completed := awaitMemoStringResult(t, held[0].done)
	if completed.err != nil {
		t.Fatalf("completed admitted work error = %v", completed.err)
	}

	prepareCalls := 0
	resp, err := memo.DoAdmitted(
		context.Background(),
		"new-after-completion",
		"new",
		equalString,
		func(_ context.Context, admission *Admission[string]) {
			prepareCalls++
			if completeErr := admission.Complete("fresh", nil); completeErr != nil {
				t.Errorf("Complete: %v", completeErr)
			}
		},
	)
	if err != nil {
		t.Fatalf("new identity error = %v", err)
	}
	if resp != "fresh" {
		t.Fatalf("new identity response = %q, want fresh", resp)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
	}
}

type memoStringResult struct {
	resp string
	err  error
}

type heldMemoRequest struct {
	requestID string
	payload   string
	complete  func()
	done      <-chan memoStringResult
}

func saturateOrdinaryMemo(t *testing.T, memo *Memo[string, string]) []*heldMemoRequest {
	return saturateMemo(t, func(requestID string, payload string) (<-chan func(), <-chan memoStringResult) {
		release := make(chan struct{})
		complete := sync.OnceFunc(func() { close(release) })
		started := make(chan func(), 1)
		done := make(chan memoStringResult, 1)
		go func() {
			resp, err := memo.Do(
				context.Background(),
				requestID,
				payload,
				equalString,
				func(context.Context) (string, error) {
					started <- complete
					<-release
					return requestID, nil
				},
			)
			done <- memoStringResult{resp: resp, err: err}
		}()
		return started, done
	})
}

func saturateAdmittedMemo(t *testing.T, memo *Memo[string, string]) []*heldMemoRequest {
	return saturateMemo(t, func(requestID string, payload string) (<-chan func(), <-chan memoStringResult) {
		started := make(chan func(), 1)
		done := make(chan memoStringResult, 1)
		go func() {
			resp, err := memo.DoAdmitted(
				context.Background(),
				requestID,
				payload,
				equalString,
				func(_ context.Context, admission *Admission[string]) {
					work, admitErr := admission.Admit()
					if admitErr != nil {
						t.Errorf("Admit: %v", admitErr)
						return
					}
					started <- func() { _ = work.Complete("completed", nil) }
				},
			)
			done <- memoStringResult{resp: resp, err: err}
		}()
		return started, done
	})
}

func saturateMemo(
	t *testing.T,
	start func(requestID string, payload string) (<-chan func(), <-chan memoStringResult),
) []*heldMemoRequest {
	t.Helper()
	var held []*heldMemoRequest
	for sequence := 1; ; sequence++ {
		requestID := fmt.Sprintf("held-%d", sequence)
		payload := fmt.Sprintf("payload-%d", sequence)
		started, done := start(requestID, payload)
		select {
		case complete := <-started:
			held = append(held, &heldMemoRequest{
				requestID: requestID,
				payload:   payload,
				complete:  complete,
				done:      done,
			})
		case result := <-done:
			if !errors.Is(result.err, ErrCapacityUnavailable) {
				completeHeldRequests(held)
				t.Fatalf("saturation request error = %v, want %v", result.err, ErrCapacityUnavailable)
			}
			return held
		case <-time.After(10 * time.Second):
			completeHeldRequests(held)
			t.Fatal("timed out driving memo to capacity")
		}
	}
}

func completeHeldRequests(held []*heldMemoRequest) {
	for _, request := range held {
		request.complete()
	}
}

func awaitMemoStringResult(t *testing.T, result <-chan memoStringResult) memoStringResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for memo result")
		return memoStringResult{}
	}
}

func equalString(a string, b string) bool {
	return a == b
}
