package runtime

import (
	"context"
	"errors"
	"sync"
)

const maxPendingRuntimeOperations = 9_999

var errRuntimeOperationFIFORequired = errors.New("active Runtime Engine requires a Runtime operation FIFO")

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
	if fifo == nil || operation == nil {
		return completedRuntimeDeferred[T](ErrEngineClosed)
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
	return deferred
}

func submitEngineRuntimeOperation[T any](
	engine *Engine,
	operation func(context.Context) (T, error),
) runtimeDeferred[T] {
	if engine == nil || engine.closed.Load() || operation == nil {
		return completedRuntimeDeferred[T](ErrEngineClosed)
	}
	fifo, err := engine.requiredRuntimeOperationFIFO()
	if err != nil {
		return completedRuntimeDeferred[T](err)
	}
	return submitRuntimeOperation(fifo, operation)
}

func awaitEngineRuntimeOperation[T any](
	ctx context.Context,
	engine *Engine,
	operation func(context.Context) (T, error),
) (T, error) {
	return submitEngineRuntimeOperation(engine, operation).Await(ctx)
}

func (e *Engine) pauseRuntimeOperations(ctx context.Context) error {
	fifo, err := e.requiredRuntimeOperationFIFO()
	if err != nil {
		return err
	}
	return fifo.Pause(ctx)
}

func (e *Engine) drainRuntimeOperations(ctx context.Context) error {
	fifo, err := e.requiredRuntimeOperationFIFO()
	if err != nil {
		return err
	}
	return fifo.Drain(ctx)
}

func (e *Engine) hasPendingRuntimeOperations() bool {
	if e == nil {
		return false
	}
	fifo, err := e.requiredRuntimeOperationFIFO()
	if err != nil {
		panic(err)
	}
	return fifo.Pending()
}

func (e *Engine) requiredRuntimeOperationFIFO() (*runtimeOperationFIFO, error) {
	if e == nil {
		return nil, ErrEngineClosed
	}
	if e.runtimeFIFO == nil {
		return nil, errRuntimeOperationFIFORequired
	}
	return e.runtimeFIFO, nil
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
	if f == nil {
		return ErrEngineClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return ErrEngineClosed
	}
	if f.paused {
		f.mu.Unlock()
		return nil
	}
	if f.pauseDone == nil {
		f.pauseTarget = f.accepted
		f.pauseDone = make(chan struct{})
		if f.completed >= f.pauseTarget {
			f.paused = true
			close(f.pauseDone)
			f.pauseDone = nil
		}
	}
	pauseDone := f.pauseDone
	paused := f.paused
	f.mu.Unlock()
	if paused {
		return nil
	}
	select {
	case <-pauseDone:
		return nil
	case <-f.workerDone:
		return ErrEngineClosed
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (f *runtimeOperationFIFO) Drain(ctx context.Context) error {
	if f == nil {
		return ErrEngineClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return ErrEngineClosed
	}
	f.paused = false
	idleDone := f.idleDone
	f.ready.Broadcast()
	f.mu.Unlock()
	select {
	case <-idleDone:
		return nil
	case <-f.workerDone:
		return ErrEngineClosed
	case <-ctx.Done():
		return context.Cause(ctx)
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

		operation.execute(ctx)
		cancel()

		f.mu.Lock()
		f.currentCancel = nil
		f.pendingCount--
		f.completed = operation.sequence
		if f.pauseDone != nil && f.completed >= f.pauseTarget {
			f.paused = true
			close(f.pauseDone)
			f.pauseDone = nil
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
