package app

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerLifecycleOptions struct {
	Loader sessionPageLoader
	Theme  string
	Header sessionPickerHeaderInfo
}

type sessionPickerLifecycle struct {
	picker   *sessionPickerModel
	geometry terminalGeometry
	result   sessionPickerResult
	cancel   context.CancelFunc
	closed   bool
}

func newSessionPickerLifecycle(options sessionPickerLifecycleOptions) *sessionPickerLifecycle {
	if options.Loader == nil {
		panic("session picker lifecycle requires a page loader")
	}
	requestContext, cancel := context.WithCancel(context.Background())
	return &sessionPickerLifecycle{
		picker: newSessionPickerModel(
			requestContext,
			options.Loader,
			options.Theme,
			options.Header,
		),
		geometry: terminalGeometryUnknown(),
		cancel:   cancel,
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

func (l *sessionPickerLifecycle) Close() {
	if l == nil || l.closed {
		return
	}
	l.cancel()
	l.closed = true
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
