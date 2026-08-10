package runtimecommand_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/runtimecommand"
)

const queueContractTimeout = 3 * time.Second

func TestQueueContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "applies admitted events in FIFO order", run: testQueueFIFO},
		{name: "receives later events while the current handler is open", run: testQueueReceiptWhileHandlerOpen},
		{name: "uses separate admission and result wait contexts", run: testQueueContextOwnership},
		{name: "supports synchronous and result-event completion", run: testQueueTypedCompletion},
		{name: "binds one read-only view to the Queue-owned result", run: testQueueCompletionBinding},
		{name: "does not couple independent queues", run: testIndependentQueues},
		{name: "distinguishes cancellation before and after admission", run: testQueueCancellationBoundaries},
		{name: "settles close races without panicking senders", run: testQueueCloseSettlement},
		{name: "cancels and joins queue-owned work", run: testQueueOwnedWorkCancellation},
		{name: "panics when ten thousand events wait for admission", run: testQueueWaitingLimitPanic},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func testQueueCompletionBinding(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	t.Cleanup(queue.Close)

	viewReady := make(chan runtimecommand.DeferredView[int], 1)
	deferred, err := runtimecommand.SubmitBound(
		context.Background(),
		queue,
		21,
		func(
			_ runtimecommand.Admission,
			value int,
			binding runtimecommand.CompletionBinding[int],
		) error {
			viewReady <- binding.Deferred()
			binding.Complete(value*2, nil)
			binding.Complete(0, errors.New("duplicate completion"))
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit bound event: %v", err)
	}
	view := receive(t, viewReady)
	awaitValue(t, deferred, 42)
	if value, err := view.Await(context.Background()); err != nil || value != 42 {
		t.Fatalf("bound Deferred view = (%d, %v), want (42, nil)", value, err)
	}
}

func testQueueFIFO(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	t.Cleanup(queue.Close)

	release := make(chan struct{})
	applied := make(chan int, 3)
	first, err := runtimecommand.Submit(context.Background(), queue, 1, func(
		scope runtimecommand.Admission,
		value int,
		complete func(int, error),
	) error {
		applied <- value
		select {
		case <-release:
			complete(value, nil)
		case <-scope.Context().Done():
		}
		return nil
	})
	if err != nil {
		t.Fatalf("submit first event: %v", err)
	}
	second, err := runtimecommand.Submit(context.Background(), queue, 2, recordingHandler(applied))
	if err != nil {
		t.Fatalf("submit second event: %v", err)
	}
	third, err := runtimecommand.Submit(context.Background(), queue, 3, recordingHandler(applied))
	if err != nil {
		t.Fatalf("submit third event: %v", err)
	}

	if got := receive(t, applied); got != 1 {
		t.Fatalf("first applied event = %d, want 1", got)
	}
	close(release)
	if got := receive(t, applied); got != 2 {
		t.Fatalf("second applied event = %d, want 2", got)
	}
	if got := receive(t, applied); got != 3 {
		t.Fatalf("third applied event = %d, want 3", got)
	}
	awaitValue(t, first, 1)
	awaitValue(t, second, 2)
	awaitValue(t, third, 3)
}

func testQueueReceiptWhileHandlerOpen(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	t.Cleanup(queue.Close)

	started := make(chan struct{})
	release := make(chan struct{})
	_, err := runtimecommand.Submit(context.Background(), queue, 1, func(
		scope runtimecommand.Admission,
		value int,
		complete func(int, error),
	) error {
		close(started)
		select {
		case <-release:
			complete(value, nil)
		case <-scope.Context().Done():
		}
		return nil
	})
	if err != nil {
		t.Fatalf("submit blocking event: %v", err)
	}
	waitSignal(t, started)

	received := make(chan *runtimecommand.Deferred[int], 1)
	submitErr := make(chan error, 1)
	go func() {
		deferred, submitError := runtimecommand.Submit(context.Background(), queue, 2, func(
			_ runtimecommand.Admission,
			value int,
			complete func(int, error),
		) error {
			complete(value, nil)
			return nil
		})
		received <- deferred
		submitErr <- submitError
	}()

	deferred := receive(t, received)
	if err := receive(t, submitErr); err != nil {
		t.Fatalf("submit later event: %v", err)
	}
	resultCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := deferred.Await(resultCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("later result before current handler release = %v, want deadline", err)
	}
	close(release)
	awaitValue(t, deferred, 2)
}

func testQueueContextOwnership(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	t.Cleanup(queue.Close)

	admissionCtx, cancelAdmission := context.WithCancel(context.Background())
	release := make(chan struct{})
	deferred, err := runtimecommand.Submit(admissionCtx, queue, "accepted", func(
		scope runtimecommand.Admission,
		value string,
		complete func(string, error),
	) error {
		select {
		case <-release:
			complete(value, nil)
		case <-scope.Context().Done():
		}
		return nil
	})
	if err != nil {
		t.Fatalf("submit accepted event: %v", err)
	}
	cancelAdmission()
	close(release)
	awaitValue(t, deferred, "accepted")
}

func testQueueTypedCompletion(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	t.Cleanup(queue.Close)

	synchronous, err := runtimecommand.Submit(context.Background(), queue, 21, func(
		_ runtimecommand.Admission,
		value int,
		complete func(int, error),
	) error {
		complete(value*2, nil)
		return nil
	})
	if err != nil {
		t.Fatalf("submit synchronous event: %v", err)
	}
	awaitValue(t, synchronous, 42)

	var mu sync.Mutex
	applied := make([]string, 0, 3)
	resultReady := make(chan struct{})
	original, err := runtimecommand.Submit(context.Background(), queue, "command", func(
		scope runtimecommand.Admission,
		value string,
		complete func(string, error),
	) error {
		mu.Lock()
		applied = append(applied, value)
		mu.Unlock()
		return scope.StartWork(func(ctx context.Context) {
			waitSignalContext(ctx, resultReady)
			resultEvent, submitErr := runtimecommand.Submit(ctx, queue, "result", func(
				_ runtimecommand.Admission,
				result string,
				completeResult func(string, error),
			) error {
				mu.Lock()
				applied = append(applied, result)
				mu.Unlock()
				completeResult(result, nil)
				return nil
			})
			if submitErr != nil {
				complete("", submitErr)
				return
			}
			result, waitErr := resultEvent.Await(ctx)
			complete(result, waitErr)
		})
	})
	if err != nil {
		t.Fatalf("submit original event: %v", err)
	}
	later, err := runtimecommand.Submit(context.Background(), queue, "later", func(
		_ runtimecommand.Admission,
		value string,
		complete func(string, error),
	) error {
		mu.Lock()
		applied = append(applied, value)
		mu.Unlock()
		complete(value, nil)
		return nil
	})
	if err != nil {
		t.Fatalf("submit later event: %v", err)
	}
	awaitValue(t, later, "later")
	close(resultReady)
	awaitValue(t, original, "result")

	mu.Lock()
	defer mu.Unlock()
	want := []string{"command", "later", "result"}
	if len(applied) != len(want) {
		t.Fatalf("applied order = %v, want %v", applied, want)
	}
	for index := range want {
		if applied[index] != want[index] {
			t.Fatalf("applied order = %v, want %v", applied, want)
		}
	}
}

func testIndependentQueues(t *testing.T) {
	blockedQueue := runtimecommand.NewQueue(context.Background())
	freeQueue := runtimecommand.NewQueue(context.Background())
	t.Cleanup(blockedQueue.Close)
	t.Cleanup(freeQueue.Close)

	release := make(chan struct{})
	_, err := runtimecommand.Submit(context.Background(), blockedQueue, 1, func(
		scope runtimecommand.Admission,
		value int,
		complete func(int, error),
	) error {
		select {
		case <-release:
			complete(value, nil)
		case <-scope.Context().Done():
		}
		return nil
	})
	if err != nil {
		t.Fatalf("submit blocked queue event: %v", err)
	}
	free, err := runtimecommand.Submit(context.Background(), freeQueue, 2, recordingHandler(make(chan int, 1)))
	if err != nil {
		t.Fatalf("submit free queue event: %v", err)
	}
	awaitValue(t, free, 2)
	close(release)
}

func testQueueCancellationBoundaries(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	t.Cleanup(queue.Close)

	preAdmission, cancelPreAdmission := context.WithCancel(context.Background())
	cancelPreAdmission()
	if deferred, err := runtimecommand.Submit(preAdmission, queue, 1, recordingHandler(make(chan int, 1))); !errors.Is(err, context.Canceled) || deferred != nil {
		t.Fatalf("pre-admission cancellation = (%v, %v), want nil and context canceled", deferred, err)
	}

	release := make(chan struct{})
	accepted, err := runtimecommand.Submit(context.Background(), queue, 2, func(
		scope runtimecommand.Admission,
		value int,
		complete func(int, error),
	) error {
		select {
		case <-release:
			complete(value, nil)
		case <-scope.Context().Done():
		}
		return nil
	})
	if err != nil {
		t.Fatalf("submit accepted event: %v", err)
	}
	waitCtx, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if _, err := accepted.Await(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-admission canceled wait = %v, want context canceled", err)
	}
	close(release)
	awaitValue(t, accepted, 2)
}

func testQueueCloseSettlement(t *testing.T) {
	t.Run("parent cancellation linearizes while a handler completion is settling", func(t *testing.T) {
		parent := newControlledCancellationContext()
		queue := runtimecommand.NewQueue(parent)
		t.Cleanup(queue.Close)
		started := make(chan struct{})
		completeNow := make(chan struct{})
		deferred, err := runtimecommand.Submit(context.Background(), queue, 1, func(
			_ runtimecommand.Admission,
			value int,
			complete func(int, error),
		) error {
			close(started)
			<-completeNow
			complete(value, nil)
			return nil
		})
		if err != nil {
			t.Fatalf("submit dispatched event: %v", err)
		}
		waitSignal(t, started)
		close(completeNow)
		waitSignal(t, parent.completionErrEntered)
		parent.Cancel()
		close(parent.releaseCompletionErr)
		if _, err := deferred.Await(context.Background()); !errors.Is(err, runtimecommand.ErrUnavailable) {
			t.Fatalf("handler completion after parent cancellation = %v, want runtime unavailable", err)
		}
	})

	queue := runtimecommand.NewQueue(context.Background())

	started := make(chan struct{})
	current, err := runtimecommand.Submit(context.Background(), queue, 1, func(
		scope runtimecommand.Admission,
		value int,
		complete func(int, error),
	) error {
		close(started)
		<-scope.Context().Done()
		complete(value, nil)
		return nil
	})
	if err != nil {
		t.Fatalf("submit current event: %v", err)
	}
	waitSignal(t, started)

	const senderCount = 32
	results := make(chan error, senderCount)
	for index := 0; index < senderCount; index++ {
		go func(value int) {
			deferred, submitErr := runtimecommand.Submit(context.Background(), queue, value, recordingHandler(make(chan int, 1)))
			if submitErr != nil {
				results <- submitErr
				return
			}
			_, waitErr := deferred.Await(context.Background())
			results <- waitErr
		}(index)
	}
	queue.Close()
	if _, err := current.Await(context.Background()); !errors.Is(err, runtimecommand.ErrUnavailable) {
		t.Fatalf("handler completion after close = %v, want runtime unavailable", err)
	}
	for range senderCount {
		if err := receive(t, results); !errors.Is(err, runtimecommand.ErrUnavailable) {
			t.Fatalf("close-racing sender result = %v, want runtime unavailable", err)
		}
	}
	if deferred, err := runtimecommand.Submit(context.Background(), queue, 99, recordingHandler(make(chan int, 1))); !errors.Is(err, runtimecommand.ErrUnavailable) || deferred != nil {
		t.Fatalf("submit after close = (%v, %v), want nil and runtime unavailable", deferred, err)
	}
}

type controlledCancellationContext struct {
	context.Context
	done                 chan struct{}
	errChecks            atomic.Int32
	completionErrEntered chan struct{}
	releaseCompletionErr chan struct{}
}

func newControlledCancellationContext() *controlledCancellationContext {
	return &controlledCancellationContext{
		Context:              context.Background(),
		done:                 make(chan struct{}),
		completionErrEntered: make(chan struct{}),
		releaseCompletionErr: make(chan struct{}),
	}
}

func (c *controlledCancellationContext) Done() <-chan struct{} {
	return c.done
}

func (c *controlledCancellationContext) Err() error {
	if c.errChecks.Add(1) == 2 {
		close(c.completionErrEntered)
		<-c.releaseCompletionErr
	}
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

func (c *controlledCancellationContext) Cancel() {
	close(c.done)
}

func testQueueOwnedWorkCancellation(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	workStarted := make(chan struct{})
	workStopped := make(chan struct{})

	deferred, err := runtimecommand.Submit(context.Background(), queue, "work", func(
		scope runtimecommand.Admission,
		_ string,
		_ func(string, error),
	) error {
		return scope.StartWork(func(ctx context.Context) {
			close(workStarted)
			<-ctx.Done()
			close(workStopped)
		})
	})
	if err != nil {
		t.Fatalf("submit work event: %v", err)
	}
	waitSignal(t, workStarted)
	queue.Close()
	waitSignal(t, workStopped)
	if _, err := deferred.Await(context.Background()); !errors.Is(err, runtimecommand.ErrUnavailable) {
		t.Fatalf("unresolved work result after close = %v, want runtime unavailable", err)
	}
}

func testQueueWaitingLimitPanic(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "threshold-reached")
	command := exec.Command(os.Args[0], "-test.run=^TestQueueWaitingLimitProcess$")
	command.Env = append(
		os.Environ(),
		"KENT_QUEUE_LIMIT_HELPER=1",
		"KENT_QUEUE_LIMIT_MARKER="+marker,
	)
	if err := command.Run(); err == nil {
		t.Fatal("queue limit helper succeeded, want backend panic")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("queue limit helper exited before reaching the waiting threshold: %v", err)
	}
}

func TestQueueWaitingLimitProcess(t *testing.T) {
	if os.Getenv("KENT_QUEUE_LIMIT_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	queue := runtimecommand.NewQueue(context.Background())
	started := make(chan struct{})
	_, err := runtimecommand.Submit(context.Background(), queue, 0, func(
		scope runtimecommand.Admission,
		_ int,
		_ func(int, error),
	) error {
		close(started)
		<-scope.Context().Done()
		return scope.Context().Err()
	})
	if err != nil {
		t.Fatalf("submit current event: %v", err)
	}
	waitSignal(t, started)
	for index := 0; index < 9_999; index++ {
		if _, err := runtimecommand.Submit(context.Background(), queue, index+1, recordingHandler(make(chan int, 1))); err != nil {
			t.Fatalf("submit waiting event %d: %v", index, err)
		}
	}
	if err := os.WriteFile(os.Getenv("KENT_QUEUE_LIMIT_MARKER"), []byte("ready"), 0o600); err != nil {
		t.Fatalf("write queue limit marker: %v", err)
	}
	if _, err := runtimecommand.Submit(context.Background(), queue, 10_000, recordingHandler(make(chan int, 1))); err != nil {
		t.Fatalf("submit threshold event: %v", err)
	}
	t.Fatal("queue accepted ten thousand waiting events without panicking")
}

func recordingHandler(applied chan<- int) func(runtimecommand.Admission, int, func(int, error)) error {
	return func(_ runtimecommand.Admission, value int, complete func(int, error)) error {
		applied <- value
		complete(value, nil)
		return nil
	}
}

func awaitValue[T comparable](t *testing.T, deferred *runtimecommand.Deferred[T], want T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queueContractTimeout)
	defer cancel()
	got, err := deferred.Await(ctx)
	if err != nil {
		t.Fatalf("await result: %v", err)
	}
	if got != want {
		t.Fatalf("result = %v, want %v", got, want)
	}
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(queueContractTimeout):
		t.Fatal("timed out waiting for value")
		var zero T
		return zero
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(queueContractTimeout):
		t.Fatal("timed out waiting for signal")
	}
}

func waitSignalContext(ctx context.Context, signal <-chan struct{}) {
	select {
	case <-signal:
	case <-ctx.Done():
	}
}
