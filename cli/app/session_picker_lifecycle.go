package app

import (
	"context"
	"errors"
	"fmt"

	"core/shared/client"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerTerminalIO interface {
	EnterAltScreen() error
	EnableAlternateScroll() error
	DisableAlternateScroll() error
	ExitAltScreen() error
}

type sessionPickerLifecycleOptions struct {
	Loader                     sessionPageLoader
	ExecutionEnvironmentClient client.SessionViewClient
	Theme                      string
	Header                     sessionPickerHeaderInfo
	Terminal                   sessionPickerTerminalIO
	RunProgram                 func(context.Context, *sessionPickerModel) (sessionPickerResult, error)
}

type sessionPickerLifecycle struct {
	picker   *sessionPickerModel
	geometry terminalGeometry
	terminal sessionPickerTerminalIO
	options  sessionPickerLifecycleOptions
	result   sessionPickerResult
	cancel   context.CancelFunc

	terminalState sessionPickerTerminalState
}

type sessionPickerTerminalState uint8

const (
	sessionPickerTerminalInactive sessionPickerTerminalState = iota + 1
	sessionPickerTerminalAltScreen
	sessionPickerTerminalAlternateScroll
	sessionPickerTerminalCleaned
)

type sessionPickerTerminalError struct {
	Operation string
	Err       error
}

func (e sessionPickerTerminalError) Error() string {
	return fmt.Sprintf("session picker terminal %s failed: %v", e.Operation, e.Err)
}

func (e sessionPickerTerminalError) Unwrap() error {
	return e.Err
}

type sessionPickerTerminal struct{}

func (sessionPickerTerminal) EnterAltScreen() error {
	return writeTerminalSequence("\x1b[?1049h")
}

func (sessionPickerTerminal) EnableAlternateScroll() error {
	return writeTerminalSequence("\x1b[?1007h")
}

func (sessionPickerTerminal) DisableAlternateScroll() error {
	return writeTerminalSequence("\x1b[?1007l")
}

func (sessionPickerTerminal) ExitAltScreen() error {
	return writeTerminalSequence("\x1b[?1049l")
}

func newSessionPickerLifecycle(options sessionPickerLifecycleOptions) *sessionPickerLifecycle {
	if options.Loader == nil {
		panic("session picker lifecycle requires a page loader")
	}
	if options.Terminal == nil {
		options.Terminal = sessionPickerTerminal{}
	}
	requestContext, cancel := context.WithCancel(context.Background())
	return &sessionPickerLifecycle{
		picker: newSessionPickerModelWithExecutionEnvironmentClient(
			requestContext,
			options.Loader,
			options.ExecutionEnvironmentClient,
			options.Theme,
			options.Header,
		),
		geometry:      terminalGeometryUnknown(),
		terminal:      options.Terminal,
		options:       options,
		cancel:        cancel,
		terminalState: sessionPickerTerminalInactive,
	}
}

func (l *sessionPickerLifecycle) Init() tea.Cmd {
	if l == nil || l.picker == nil {
		return nil
	}
	return l.picker.Init()
}

func (l *sessionPickerLifecycle) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if l == nil || l.picker == nil {
		return l, nil
	}
	if size, ok := message.(tea.WindowSizeMsg); ok {
		if size.Width > 0 && size.Height > 0 {
			l.geometry = terminalGeometryKnown(size.Width, size.Height)
		} else {
			l.geometry = terminalGeometryUnknown()
		}
	}
	next, command := l.picker.Update(message)
	if updated, ok := next.(*sessionPickerModel); ok {
		l.picker = updated
	}
	if l.picker.result != nil {
		l.result = l.picker.result
	}
	return l, command
}

func (l *sessionPickerLifecycle) View() string {
	if l == nil || l.picker == nil {
		return ""
	}
	size := l.geometry.Size()
	if size == nil || size.width < 40 || size.height < 10 {
		return ""
	}
	return l.picker.View()
}

func (l *sessionPickerLifecycle) Result() sessionPickerResult {
	if l == nil {
		return nil
	}
	return l.result
}

func (l *sessionPickerLifecycle) Run(ctx context.Context) (sessionPickerResult, error) {
	if l == nil {
		return nil, errors.New("session picker lifecycle is required")
	}
	if err := l.enterTerminal(); err != nil {
		return nil, err
	}
	var runErr error
	if l.options.RunProgram == nil {
		runErr = errors.New("session picker lifecycle program is required")
	} else {
		l.result, runErr = l.options.RunProgram(ctx, l.picker)
	}
	cleanupErr := l.Cleanup()
	if runErr != nil && cleanupErr != nil {
		return nil, errors.Join(runErr, cleanupErr)
	}
	if runErr != nil {
		return nil, runErr
	}
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	if err := validateSessionPickerLifecycleResult(l.result); err != nil {
		return nil, err
	}
	return l.result, nil
}

func (l *sessionPickerLifecycle) enterTerminal() error {
	if err := l.terminal.EnterAltScreen(); err != nil {
		return sessionPickerTerminalError{Operation: "enter alt-screen", Err: err}
	}
	l.terminalState = sessionPickerTerminalAltScreen
	if err := l.terminal.EnableAlternateScroll(); err != nil {
		cleanupErr := l.Cleanup()
		if cleanupErr != nil {
			return errors.Join(sessionPickerTerminalError{Operation: "enable alternate scroll", Err: err}, cleanupErr)
		}
		return sessionPickerTerminalError{Operation: "enable alternate scroll", Err: err}
	}
	l.terminalState = sessionPickerTerminalAlternateScroll
	return nil
}

func (l *sessionPickerLifecycle) Cleanup() error {
	if l == nil || l.terminalState == sessionPickerTerminalCleaned {
		return nil
	}
	l.picker.cancelSelectedDetailRequests()
	l.cancel()
	var cleanupErr error
	switch l.terminalState {
	case sessionPickerTerminalAlternateScroll:
		if err := l.terminal.DisableAlternateScroll(); err != nil {
			cleanupErr = sessionPickerTerminalError{Operation: "disable alternate scroll", Err: err}
		}
		fallthrough
	case sessionPickerTerminalAltScreen:
		if err := l.terminal.ExitAltScreen(); err != nil {
			exitErr := sessionPickerTerminalError{Operation: "exit alt-screen", Err: err}
			if cleanupErr != nil {
				cleanupErr = errors.Join(cleanupErr, exitErr)
			} else {
				cleanupErr = exitErr
			}
		}
	case sessionPickerTerminalInactive:
	default:
		panic(fmt.Sprintf("unknown session picker terminal state %d", l.terminalState))
	}
	l.terminalState = sessionPickerTerminalCleaned
	return cleanupErr
}

func validateSessionPickerLifecycleResult(result sessionPickerResult) error {
	switch typed := result.(type) {
	case sessionPickerCreateResult, sessionPickerCancelResult:
	case sessionPickerOpenResult:
		if typed.sessionID.IsZero() {
			return errors.New("session picker lifecycle open result requires a session ID")
		}
	default:
		return errors.New("session picker lifecycle exited without a result")
	}
	return nil
}
