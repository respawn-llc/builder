package lifecyclehook

import (
	"context"
	"sync"

	"core/shared/lifecyclecontract"
)

type ObservationFact string

const (
	ObservationFactSessionIdentity ObservationFact = "session_identity"
	ObservationFactSessionStatus   ObservationFact = "session_status"
)

type IssueDetail interface {
	error
	lifecycleIssueDetail()
}

type ProcessIssue struct {
	Category lifecyclecontract.Category
	Cause    error
	Stderr   string
}

func (i ProcessIssue) Error() string {
	if i.Cause == nil {
		return "lifecycle hook process failed"
	}
	return i.Cause.Error()
}

func (ProcessIssue) lifecycleIssueDetail() {}

type ObservationIssue struct {
	Fact  ObservationFact
	Cause error
}

func (i ObservationIssue) Error() string {
	if i.Cause == nil {
		return "lifecycle hook observation failed"
	}
	return i.Cause.Error()
}

func (ObservationIssue) lifecycleIssueDetail() {}

type Issue struct {
	Detail IssueDetail
	Count  int
}

func NewProcessIssue(category lifecyclecontract.Category, cause error, stderr string) Issue {
	return Issue{Detail: ProcessIssue{Category: category, Cause: cause, Stderr: stderr}}
}

func NewObservationIssue(fact ObservationFact, cause error) Issue {
	return Issue{Detail: ObservationIssue{Fact: fact, Cause: cause}}
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
	if issue.Detail == nil {
		panic("report lifecycle issue without detail")
	}
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
