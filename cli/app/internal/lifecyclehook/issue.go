package lifecyclehook

import (
	"context"
	"sync"

	"core/shared/lifecyclecontract"
)

type Issue struct {
	Category lifecyclecontract.Category
	Err      error
	Stderr   string
	Count    int
}

type issueReporter struct {
	ctx     context.Context
	issues  chan Issue
	signal  chan struct{}
	mu      sync.Mutex
	pending *Issue
}

func newIssueReporter(ctx context.Context, capacity int) *issueReporter {
	reporter := &issueReporter{
		ctx:    ctx,
		issues: make(chan Issue, capacity),
		signal: make(chan struct{}, 1),
	}
	go reporter.run()
	return reporter
}

func (r *issueReporter) Issues() <-chan Issue {
	return r.issues
}

func (r *issueReporter) Report(issue Issue) {
	if issue.Count <= 0 {
		issue.Count = 1
	}
	r.mu.Lock()
	if r.pending == nil {
		r.pending = &issue
	} else {
		issue.Count += r.pending.Count
		r.pending = &issue
	}
	r.mu.Unlock()
	select {
	case r.signal <- struct{}{}:
	default:
	}
}

func (r *issueReporter) run() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.signal:
		}
		for {
			r.mu.Lock()
			pending := r.pending
			r.pending = nil
			r.mu.Unlock()
			if pending == nil {
				break
			}
			select {
			case <-r.ctx.Done():
				return
			case r.issues <- *pending:
			}
		}
	}
}
