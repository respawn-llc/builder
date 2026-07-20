package app

import (
	"math"
	"sync"
)

const lifecycleHookIssueMailboxCapacity = 8

type lifecycleHookIssueKind uint8

const (
	lifecycleHookIssueEncoding lifecycleHookIssueKind = iota + 1
	lifecycleHookIssueLaunchDisabled
	lifecycleHookIssueLaunchFailed
	lifecycleHookIssueNonzeroExit
	lifecycleHookIssueTimeout
	lifecycleHookIssueQueueOverload
)

type lifecycleHookLaunchFailureKind uint8

const (
	lifecycleHookLaunchUnavailable lifecycleHookLaunchFailureKind = iota + 1
	lifecycleHookLaunchNonExecutable
)

type lifecycleHookIssue struct {
	Kind                lifecycleHookIssueKind
	Cause               error
	LaunchFailure       *lifecycleHookLaunchFailureKind
	ExitCode            *int
	Stderr              *string
	StderrOverflowBytes *int64
	DroppedCount        *uint64
}

type lifecycleHookIssueMailbox struct {
	mu            sync.Mutex
	issues        []lifecycleHookIssue
	overloadCount uint64
	signal        chan struct{}
	closed        bool
}

func newLifecycleHookIssueMailbox() *lifecycleHookIssueMailbox {
	return &lifecycleHookIssueMailbox{
		issues: make([]lifecycleHookIssue, 0, lifecycleHookIssueMailboxCapacity),
		signal: make(chan struct{}, 1),
	}
}

func (m *lifecycleHookIssueMailbox) Signal() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.signal
}

func (m *lifecycleHookIssueMailbox) Report(issue lifecycleHookIssue) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if len(m.issues) < lifecycleHookIssueMailboxCapacity {
		m.issues = append(m.issues, cloneLifecycleHookIssue(issue))
		m.signalLocked()
		return
	}
	if issue.Kind == lifecycleHookIssueLaunchDisabled {
		m.issues[len(m.issues)-1] = cloneLifecycleHookIssue(issue)
		m.signalLocked()
	}
}

func (m *lifecycleHookIssueMailbox) ReportOverload() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if m.overloadCount < math.MaxUint64 {
		m.overloadCount++
	}
	m.signalLocked()
}

func (m *lifecycleHookIssueMailbox) Take() (lifecycleHookIssue, bool) {
	if m == nil {
		return lifecycleHookIssue{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var issue lifecycleHookIssue
	switch {
	case m.overloadCount > 0:
		count := m.overloadCount
		m.overloadCount = 0
		issue = lifecycleHookIssue{
			Kind:         lifecycleHookIssueQueueOverload,
			DroppedCount: &count,
		}
	case len(m.issues) > 0:
		issue = m.issues[0]
		copy(m.issues, m.issues[1:])
		m.issues = m.issues[:len(m.issues)-1]
	default:
		return lifecycleHookIssue{}, false
	}
	if m.overloadCount > 0 || len(m.issues) > 0 || m.closed {
		m.signalLocked()
	}
	return issue, true
}

func (m *lifecycleHookIssueMailbox) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.signalLocked()
}

func (m *lifecycleHookIssueMailbox) ClosedAndEmpty() bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed && m.overloadCount == 0 && len(m.issues) == 0
}

func (m *lifecycleHookIssueMailbox) signalLocked() {
	select {
	case m.signal <- struct{}{}:
	default:
	}
}

func cloneLifecycleHookIssue(issue lifecycleHookIssue) lifecycleHookIssue {
	cloned := issue
	if issue.LaunchFailure != nil {
		value := *issue.LaunchFailure
		cloned.LaunchFailure = &value
	}
	if issue.ExitCode != nil {
		value := *issue.ExitCode
		cloned.ExitCode = &value
	}
	if issue.Stderr != nil {
		value := *issue.Stderr
		cloned.Stderr = &value
	}
	if issue.StderrOverflowBytes != nil {
		value := *issue.StderrOverflowBytes
		cloned.StderrOverflowBytes = &value
	}
	if issue.DroppedCount != nil {
		value := *issue.DroppedCount
		cloned.DroppedCount = &value
	}
	return cloned
}
