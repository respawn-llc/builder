package app

import (
	"errors"
	"testing"
)

func TestOngoingSurfaceErrorExitsTUIInRelease(t *testing.T) {
	m := newProjectedStaticUIModel()

	cmd := m.handleOngoingSurfaceError(errors.New("terminal write failed"))

	if cmd != nil {
		t.Fatal("ongoing fatal error handler returned recovery command")
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
