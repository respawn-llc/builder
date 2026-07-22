//go:build windows

package driver

import (
	"errors"

	"core/internal/testharness/pty/analyzer"

	"github.com/google/uuid"
)

var errConPTYUnavailable = errors.New("PTY harness is unavailable on Windows: ConPTY support is not implemented")

type SessionEventKind int

const (
	SessionEventTerminalAnalysis SessionEventKind = iota + 1
	SessionEventProcessExit
	SessionEventCommandStarted
	SessionEventCommandCompleted
	SessionEventCommandFailed
	SessionEventFailure
)

// SessionEvent is an immutable observation emitted by one PTY reactor.
type SessionEvent struct {
	Kind        SessionEventKind
	CommandID   uuid.UUID
	Analysis    *analyzer.Analysis
	ProcessExit *analyzer.ProcessExit
	Err         error
}

// Session is unavailable until the harness has a ConPTY backend.
type Session struct{}

func StartSession(SessionSpec) (*Session, error) {
	return nil, errConPTYUnavailable
}

func (*Session) Events() <-chan SessionEvent {
	return nil
}

func (*Session) Done() <-chan struct{} {
	return nil
}

func (*Session) Failure() <-chan struct{} {
	return nil
}

func (*Session) Error() error {
	return errConPTYUnavailable
}

func (*Session) Enqueue(SessionCommand) error {
	return errConPTYUnavailable
}

func (*Session) Capture() (analyzer.Capture, error) {
	return analyzer.Capture{}, errConPTYUnavailable
}

func (*Session) Close() error {
	return nil
}

func (*Session) Terminate() error {
	return nil
}

func (*Session) ForceKill() error {
	return nil
}
