package runtime

import (
	"context"
	"sync"
)

const maxPendingRuntimeOperations = 9_999

type runtimeDeferred[T any] struct {
	state *runtimeDeferredState[T]
}

type runtimeDeferredState[T any] struct {
	once   sync.Once
	done   chan struct{}
	result T
	err    error
}

func newRuntimeDeferred[T any]() runtimeDeferred[T] {
	return runtimeDeferred[T]{state: &runtimeDeferredState[T]{done: make(chan struct{})}}
}

func completedRuntimeDeferred[T any](err error) runtimeDeferred[T] {
	deferred := newRuntimeDeferred[T]()
	deferred.complete(*new(T), err)
	return deferred
}

func (d runtimeDeferred[T]) Await(ctx context.Context) (T, error) {
	if d.state == nil {
		var zero T
		return zero, ErrEngineClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-d.state.done:
		return d.state.result, d.state.err
	case <-ctx.Done():
		var zero T
		return zero, context.Cause(ctx)
	}
}

func (d runtimeDeferred[T]) complete(result T, err error) {
	if d.state == nil {
		return
	}
	d.state.once.Do(func() {
		d.state.result = result
		d.state.err = err
		close(d.state.done)
	})
}

type runtimeOperation struct {
	sequence    uint64
	execute     func(context.Context)
	unavailable func()
}

type runtimeOperationSequenceKey struct{}

func withRuntimeOperationSequence(ctx context.Context, sequence uint64) context.Context {
	return context.WithValue(ctx, runtimeOperationSequenceKey{}, sequence)
}

func runtimeOperationSequence(ctx context.Context) uint64 {
	sequence, _ := ctx.Value(runtimeOperationSequenceKey{}).(uint64)
	return sequence
}

type runtimeOperationFIFO struct {
	mu            sync.Mutex
	ready         *sync.Cond
	pending       []runtimeOperation
	pendingCount  int
	accepted      uint64
	completed     uint64
	paused        bool
	pauseTarget   uint64
	pauseDone     chan struct{}
	pauseWaiters  int
	idleDone      chan struct{}
	closed        bool
	currentCancel context.CancelFunc
	workerDone    chan struct{}
}

func newRuntimeOperationFIFO() *runtimeOperationFIFO {
	idleDone := make(chan struct{})
	close(idleDone)
	fifo := &runtimeOperationFIFO{
		idleDone:   idleDone,
		workerDone: make(chan struct{}),
	}
	fifo.ready = sync.NewCond(&fifo.mu)
	go fifo.run()
	return fifo
}

func submitRuntimeOperation[T any](
	fifo *runtimeOperationFIFO,
	operation func(context.Context) (T, error),
) runtimeDeferred[T] {
	deferred, _ := trySubmitRuntimeOperation(fifo, operation)
	return deferred
}

func trySubmitRuntimeOperation[T any](
	fifo *runtimeOperationFIFO,
	operation func(context.Context) (T, error),
) (runtimeDeferred[T], bool) {
	if fifo == nil || operation == nil {
		return completedRuntimeDeferred[T](ErrEngineClosed), false
	}
	deferred := newRuntimeDeferred[T]()
	accepted := fifo.submit(runtimeOperation{
		execute: func(ctx context.Context) {
			result, err := operation(ctx)
			deferred.complete(result, err)
		},
		unavailable: func() {
			var zero T
			deferred.complete(zero, ErrEngineClosed)
		},
	})
	if !accepted {
		deferred.complete(*new(T), ErrEngineClosed)
	}
	return deferred, accepted
}

func submitEngineRuntimeOperation[T any](
	engine *Engine,
	operation func(context.Context) (T, error),
) runtimeDeferred[T] {
	deferred, _ := trySubmitEngineRuntimeOperation(engine, operation)
	return deferred
}

func trySubmitEngineRuntimeOperation[T any](
	engine *Engine,
	operation func(context.Context) (T, error),
) (runtimeDeferred[T], bool) {
	if engine == nil || engine.closed.Load() || operation == nil {
		return completedRuntimeDeferred[T](ErrEngineClosed), false
	}
	return trySubmitRuntimeOperation(engine.runtimeFIFO, operation)
}

func awaitEngineRuntimeOperation[T any](
	ctx context.Context,
	engine *Engine,
	operation func(context.Context) (T, error),
) (T, error) {
	return submitEngineRuntimeOperation(engine, operation).Await(ctx)
}

func (e *Engine) pauseRuntimeOperations(ctx context.Context) error {
	if e == nil {
		return ErrEngineClosed
	}
	return e.runtimeFIFO.Pause(ctx)
}

func (e *Engine) pauseRuntimeOperationsThrough(ctx context.Context) (uint64, error) {
	if e == nil {
		return 0, ErrEngineClosed
	}
	return e.runtimeFIFO.pauseThrough(ctx)
}

func (e *Engine) drainRuntimeOperations(ctx context.Context) error {
	if e == nil {
		return ErrEngineClosed
	}
	return e.runtimeFIFO.Drain(ctx)
}

func (e *Engine) drainRuntimeOperationsThrough(ctx context.Context) (uint64, error) {
	if e == nil {
		return 0, ErrEngineClosed
	}
	return e.runtimeFIFO.drainThrough(ctx)
}

func (e *Engine) hasPendingRuntimeOperations() bool {
	return e.HasPendingRuntimeOperations()
}

func (e *Engine) HasPendingRuntimeOperations() bool {
	if e == nil {
		return false
	}
	return e.runtimeFIFO.Pending()
}

func (f *runtimeOperationFIFO) submit(operation runtimeOperation) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	if f.pendingCount == maxPendingRuntimeOperations {
		panic("Runtime operation FIFO cannot retain 10,000 pending operations")
	}
	f.accepted++
	operation.sequence = f.accepted
	if f.pendingCount == 0 {
		f.idleDone = make(chan struct{})
	}
	f.pending = append(f.pending, operation)
	f.pendingCount++
	f.ready.Signal()
	return true
}

func (f *runtimeOperationFIFO) Pause(ctx context.Context) error {
	_, err := f.pauseThrough(ctx)
	return err
}

func (f *runtimeOperationFIFO) pauseThrough(ctx context.Context) (uint64, error) {
	if f == nil {
		return 0, ErrEngineClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, ErrEngineClosed
	}
	if f.paused {
		through := f.completed
		f.mu.Unlock()
		return through, nil
	}
	pauseTarget := f.pauseTarget
	if f.pauseDone == nil {
		pauseTarget = f.accepted
		f.pauseTarget = pauseTarget
		f.pauseDone = make(chan struct{})
		if f.completed >= f.pauseTarget {
			f.paused = true
			close(f.pauseDone)
			f.pauseDone = nil
			f.pauseTarget = 0
		}
	}
	pauseDone := f.pauseDone
	paused := f.paused
	if !paused {
		f.pauseWaiters++
	}
	f.mu.Unlock()
	if paused {
		return pauseTarget, nil
	}
	select {
	case <-pauseDone:
		return pauseTarget, nil
	case <-f.workerDone:
		return 0, ErrEngineClosed
	case <-ctx.Done():
		f.withdrawPauseWaiter(pauseDone)
		return 0, context.Cause(ctx)
	}
}

func (f *runtimeOperationFIFO) withdrawPauseWaiter(pauseDone <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pauseDone != pauseDone {
		return
	}
	f.pauseWaiters--
	if f.pauseWaiters < 0 {
		panic("Runtime operation FIFO pause waiter count became negative")
	}
	if f.pauseWaiters == 0 {
		f.pauseDone = nil
		f.pauseTarget = 0
	}
}

func (f *runtimeOperationFIFO) Drain(ctx context.Context) error {
	_, err := f.drainThrough(ctx)
	return err
}

func (f *runtimeOperationFIFO) drainThrough(ctx context.Context) (uint64, error) {
	if f == nil {
		return 0, ErrEngineClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, ErrEngineClosed
	}
	f.paused = false
	idleDone := f.idleDone
	f.ready.Broadcast()
	f.mu.Unlock()
	select {
	case <-idleDone:
		f.mu.Lock()
		through := f.completed
		closed := f.closed
		f.mu.Unlock()
		if closed {
			return 0, ErrEngineClosed
		}
		return through, nil
	case <-f.workerDone:
		return 0, ErrEngineClosed
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	}
}

func (f *runtimeOperationFIFO) Pending() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pendingCount > 0
}

func (f *runtimeOperationFIFO) beginCloseIfIdle() bool {
	if f == nil {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return true
	}
	if f.pendingCount != 0 {
		return false
	}
	f.closed = true
	f.ready.Broadcast()
	return true
}

func (f *runtimeOperationFIFO) Close() {
	if f == nil {
		return
	}
	f.beginClose()
	<-f.workerDone
}

func (f *runtimeOperationFIFO) beginClose() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	cancel := f.currentCancel
	queued := append([]runtimeOperation(nil), f.pending...)
	f.pending = nil
	f.pendingCount -= len(queued)
	if f.pauseDone != nil {
		close(f.pauseDone)
		f.pauseDone = nil
		f.pauseTarget = 0
		f.pauseWaiters = 0
	}
	if f.pendingCount == 0 {
		closeRuntimeOperationSignal(f.idleDone)
	}
	f.ready.Broadcast()
	f.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, operation := range queued {
		operation.unavailable()
	}
}

func (f *runtimeOperationFIFO) run() {
	defer close(f.workerDone)
	for {
		f.mu.Lock()
		for (len(f.pending) == 0 || f.paused) && !f.closed {
			f.ready.Wait()
		}
		if f.closed {
			f.mu.Unlock()
			return
		}
		operation := f.pending[0]
		f.pending = f.pending[1:]
		ctx, cancel := context.WithCancel(context.Background())
		f.currentCancel = cancel
		f.mu.Unlock()

		operation.execute(withRuntimeOperationSequence(ctx, operation.sequence))
		cancel()

		f.mu.Lock()
		f.currentCancel = nil
		f.pendingCount--
		f.completed = operation.sequence
		if f.pauseDone != nil && f.completed >= f.pauseTarget {
			f.paused = true
			close(f.pauseDone)
			f.pauseDone = nil
			f.pauseTarget = 0
			f.pauseWaiters = 0
		}
		if f.pendingCount == 0 {
			closeRuntimeOperationSignal(f.idleDone)
		}
		f.mu.Unlock()
	}
}

func closeRuntimeOperationSignal(signal chan struct{}) {
	select {
	case <-signal:
	default:
		close(signal)
	}
}
