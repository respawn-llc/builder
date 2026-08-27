package app

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func runContextualStartupPicker(ctx context.Context, model tea.Model) (tea.Model, error) {
	terminal := startupPickerTerminal{state: startupPickerTerminalInactive}
	if err := terminal.Enter(); err != nil {
		return nil, err
	}
	finalModel, runErr := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	closeErr := terminal.Close()
	if runErr != nil && closeErr != nil {
		return nil, errors.Join(runErr, closeErr)
	}
	if runErr != nil {
		return nil, runErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return finalModel, nil
}

type startupPickerTerminalState uint8

const (
	startupPickerTerminalInactive startupPickerTerminalState = iota + 1
	startupPickerTerminalAltScreen
	startupPickerTerminalAlternateScroll
	startupPickerTerminalCleaned
)

type startupPickerTerminalError struct {
	Operation string
	Err       error
}

func (e startupPickerTerminalError) Error() string {
	return fmt.Sprintf("startup picker terminal %s failed: %v", e.Operation, e.Err)
}

func (e startupPickerTerminalError) Unwrap() error { return e.Err }

type startupPickerTerminal struct {
	state startupPickerTerminalState
}

func (t *startupPickerTerminal) Enter() error {
	if t == nil || t.state != startupPickerTerminalInactive {
		return errors.New("startup picker terminal must be inactive before entry")
	}
	if err := writeTerminalSequence("\x1b[?1049h"); err != nil {
		return startupPickerTerminalError{Operation: "enter alt-screen", Err: err}
	}
	t.state = startupPickerTerminalAltScreen
	if err := writeTerminalSequence("\x1b[?1007h"); err != nil {
		cleanupErr := t.Close()
		entryErr := startupPickerTerminalError{Operation: "enable alternate scroll", Err: err}
		if cleanupErr != nil {
			return errors.Join(entryErr, cleanupErr)
		}
		return entryErr
	}
	t.state = startupPickerTerminalAlternateScroll
	return nil
}

func (t *startupPickerTerminal) Close() error {
	if t == nil || t.state == startupPickerTerminalCleaned {
		return nil
	}
	var result error
	switch t.state {
	case startupPickerTerminalAlternateScroll:
		if err := writeTerminalSequence("\x1b[?1007l"); err != nil {
			result = startupPickerTerminalError{Operation: "disable alternate scroll", Err: err}
		}
		fallthrough
	case startupPickerTerminalAltScreen:
		if err := writeTerminalSequence("\x1b[?1049l"); err != nil {
			exitErr := startupPickerTerminalError{Operation: "exit alt-screen", Err: err}
			if result != nil {
				result = errors.Join(result, exitErr)
			} else {
				result = exitErr
			}
		}
	case startupPickerTerminalInactive:
	default:
		panic(fmt.Sprintf("unknown startup picker terminal state %d", t.state))
	}
	t.state = startupPickerTerminalCleaned
	return result
}
