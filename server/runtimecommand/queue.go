package runtimecommand

import (
	"context"
	"errors"
	"sync"
)

const waitingEventLimit = 10_000

var ErrUnavailable = errors.New("runtime event queue unavailable")

type Queue struct {
	ctx    context.Context
	cancel context.CancelFunc

	ingress chan submission
	work    chan queuedEvent

	brokerDone chan struct{}
	workerDone chan struct{}
	closed     chan struct{}

	workGroup sync.WaitGroup

	outstandingMu sync.Mutex
	outstanding   map[resultSettler]struct{}
}

type submission struct {
	event    queuedEvent
	received chan struct{}
}

type queuedEvent interface {
	handle(Admission) error
	settle(error)
	result() resultSettler
}

type resultSettler interface {
	settle(error)
}

func NewQueue(parent context.Context) *Queue {
	ctx, cancel := context.WithCancel(parent)
	queue := &Queue{
		ctx:         ctx,
		cancel:      cancel,
		ingress:     make(chan submission),
		work:        make(chan queuedEvent),
		brokerDone:  make(chan struct{}),
		workerDone:  make(chan struct{}),
		closed:      make(chan struct{}),
		outstanding: make(map[resultSettler]struct{}),
	}
	go queue.runBroker()
	go queue.runWorker()
	go queue.finishClose()
	return queue
}

func (q *Queue) Close() {
	if q == nil {
		return
	}
	q.outstandingMu.Lock()
	q.cancel()
	q.outstandingMu.Unlock()
	<-q.closed
}

func (q *Queue) runBroker() {
	defer close(q.brokerDone)
	defer close(q.work)

	pending := make([]queuedEvent, 0)
	for {
		var next queuedEvent
		var dispatch chan queuedEvent
		if len(pending) > 0 {
			next = pending[0]
			dispatch = q.work
		}
		select {
		case <-q.ctx.Done():
			for _, event := range pending {
				event.settle(ErrUnavailable)
			}
			return
		case accepted := <-q.ingress:
			if !q.track(accepted.event.result()) {
				accepted.event.settle(ErrUnavailable)
				accepted.received <- struct{}{}
				continue
			}
			pending = append(pending, accepted.event)
			if len(pending) >= waitingEventLimit {
				panic("runtime event queue reached 10,000 waiting events")
			}
			accepted.received <- struct{}{}
		case dispatch <- next:
			pending[0] = nil
			pending = pending[1:]
		}
	}
}

func (q *Queue) runWorker() {
	defer close(q.workerDone)
	for event := range q.work {
		scope := newAdmission(q)
		err := event.handle(scope)
		scope.finish(err == nil)
		if q.ctx.Err() != nil {
			event.settle(ErrUnavailable)
			continue
		}
		if err != nil {
			event.settle(err)
		}
	}
}

func (q *Queue) finishClose() {
	<-q.ctx.Done()
	<-q.brokerDone
	<-q.workerDone
	q.workGroup.Wait()
	q.settleOutstanding()
	close(q.closed)
}

func (q *Queue) track(result resultSettler) bool {
	q.outstandingMu.Lock()
	defer q.outstandingMu.Unlock()
	if q.ctx.Err() != nil {
		return false
	}
	q.outstanding[result] = struct{}{}
	return true
}

func (q *Queue) settleOutstanding() {
	q.outstandingMu.Lock()
	results := make([]resultSettler, 0, len(q.outstanding))
	for result := range q.outstanding {
		results = append(results, result)
	}
	clear(q.outstanding)
	q.outstandingMu.Unlock()
	for _, result := range results {
		result.settle(ErrUnavailable)
	}
}

type Admission struct {
	state *admissionState
}

type admissionState struct {
	queue *Queue

	mu       sync.Mutex
	finished bool
	work     func(context.Context)
}

func newAdmission(queue *Queue) Admission {
	return Admission{state: &admissionState{queue: queue}}
}

func (a Admission) Context() context.Context {
	if a.state == nil || a.state.queue == nil {
		return context.Background()
	}
	return a.state.queue.ctx
}

func (a Admission) Owns(queue *Queue) bool {
	return a.state != nil && a.state.queue != nil && a.state.queue == queue
}

func (a Admission) StartWork(work func(context.Context)) error {
	if a.state == nil || a.state.queue == nil {
		return errors.New("runtime event admission is unavailable")
	}
	if work == nil {
		return errors.New("runtime event work is required")
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if a.state.finished {
		return errors.New("runtime event admission has finished")
	}
	if a.state.work != nil {
		return errors.New("runtime event admission already transferred work")
	}
	a.state.work = work
	return nil
}

func (a Admission) finish(start bool) {
	if a.state == nil || a.state.queue == nil {
		return
	}
	a.state.mu.Lock()
	if a.state.finished {
		a.state.mu.Unlock()
		return
	}
	a.state.finished = true
	work := a.state.work
	a.state.mu.Unlock()
	if !start || work == nil {
		return
	}
	a.state.queue.workGroup.Add(1)
	go func() {
		defer a.state.queue.workGroup.Done()
		work(a.state.queue.ctx)
	}()
}

type Deferred[Result interface{}] struct {
	done chan struct{}

	mu        sync.Mutex
	completed bool
	value     Result
	err       error
	queue     *Queue
}

func newDeferred[Result interface{}](queue *Queue) *Deferred[Result] {
	return &Deferred[Result]{done: make(chan struct{}), queue: queue}
}

func (d *Deferred[Result]) Await(ctx context.Context) (Result, error) {
	if d == nil {
		var zero Result
		return zero, ErrUnavailable
	}
	select {
	case <-ctx.Done():
		var zero Result
		return zero, ctx.Err()
	case <-d.done:
		d.mu.Lock()
		value, err := d.value, d.err
		d.mu.Unlock()
		return value, err
	}
}

func (d *Deferred[Result]) complete(value Result, err error) {
	if d == nil {
		return
	}
	d.queue.outstandingMu.Lock()
	defer d.queue.outstandingMu.Unlock()
	if d.queue.ctx.Err() != nil {
		var zero Result
		value = zero
		err = ErrUnavailable
	}
	delete(d.queue.outstanding, d)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completed {
		return
	}
	d.completed = true
	d.value = value
	d.err = err
	close(d.done)
}

func (d *Deferred[Result]) settle(err error) {
	var zero Result
	d.complete(zero, err)
}

type eventEnvelope[Payload, Result interface{}] struct {
	payload  Payload
	handler  func(Admission, Payload, func(Result, error)) error
	deferred *Deferred[Result]
}

func (e *eventEnvelope[Payload, Result]) handle(admission Admission) error {
	return e.handler(admission, e.payload, e.deferred.complete)
}

func (e *eventEnvelope[Payload, Result]) settle(err error) {
	e.deferred.settle(err)
}

func (e *eventEnvelope[Payload, Result]) result() resultSettler {
	return e.deferred
}

func Submit[Payload, Result interface{}](
	ctx context.Context,
	queue *Queue,
	payload Payload,
	handler func(Admission, Payload, func(Result, error)) error,
) (*Deferred[Result], error) {
	if queue == nil || queue.ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, errors.New("runtime event handler is required")
	}
	deferred := newDeferred[Result](queue)
	event := &eventEnvelope[Payload, Result]{
		payload:  payload,
		handler:  handler,
		deferred: deferred,
	}
	accepted := submission{
		event:    event,
		received: make(chan struct{}, 1),
	}
	select {
	case queue.ingress <- accepted:
		<-accepted.received
		return deferred, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-queue.ctx.Done():
		return nil, ErrUnavailable
	}
}
