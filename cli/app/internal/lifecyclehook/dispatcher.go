package lifecyclehook

import (
	"context"
	"sync/atomic"

	"core/shared/lifecyclecontract"
)

const (
	eventCapacity = 64
	activeLimit   = 64
)

type Dispatcher struct {
	command []string
	ctx     context.Context
	cancel  context.CancelFunc
	events  chan lifecyclecontract.Event
	active  chan struct{}
	issues  *issueReporter
	done    chan struct{}
	closed  atomic.Bool
}

func New(parent context.Context, command []string) *Dispatcher {
	if len(command) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	dispatcher := &Dispatcher{
		command: append([]string(nil), command...),
		ctx:     ctx,
		cancel:  cancel,
		events:  make(chan lifecyclecontract.Event, eventCapacity),
		active:  make(chan struct{}, activeLimit),
		issues:  newIssueReporter(ctx, eventCapacity),
		done:    make(chan struct{}),
	}
	go dispatcher.drain()
	return dispatcher
}

func (d *Dispatcher) Submit(event lifecyclecontract.Event) {
	if d == nil || d.closed.Load() {
		return
	}
	select {
	case d.events <- event:
	default:
	}
}

func (d *Dispatcher) Issues() <-chan Issue {
	if d == nil {
		return nil
	}
	return d.issues.Issues()
}

func (d *Dispatcher) Report(issue Issue) {
	if d == nil || d.closed.Load() {
		return
	}
	d.issues.Report(issue)
}

func (d *Dispatcher) Done() <-chan struct{} {
	if d == nil {
		return nil
	}
	return d.done
}

func (d *Dispatcher) Close() {
	if d == nil || !d.closed.CompareAndSwap(false, true) {
		return
	}
	d.cancel()
	close(d.done)
}

func (d *Dispatcher) drain() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case event := <-d.events:
			select {
			case d.active <- struct{}{}:
				go d.invoke(event)
			default:
			}
		}
	}
}
