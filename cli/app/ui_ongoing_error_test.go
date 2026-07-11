package app

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOngoingSurfaceErrorExitsTUIInRelease(t *testing.T) {
	m := newProjectedStaticUIModel()

	cmd := m.handleOngoingSurfaceError(errors.New("terminal write failed"))

	if cmd == nil {
		t.Fatal("ongoing fatal error handler did not return quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ongoing fatal error handler command did not return quit message")
	}
	if !m.Transition().Exit {
		t.Fatal("ongoing fatal error did not request clear TUI exit")
	}
}

func TestOngoingSurfaceErrorPanicsInDebug(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIDebug(true))

	defer func() {
		if recover() == nil {
			t.Fatal("expected debug panic")
		}
	}()

	_ = m.handleOngoingSurfaceError(errors.New("terminal write failed"))
}
