package app

import (
	"errors"
	"testing"
)

func TestTerminalSequenceWriteFailureUsesFatalTerminalErrorPolicy(t *testing.T) {
	originalWrite := writeTerminalSequence
	writeTerminalSequence = func(string) error {
		return errors.New("terminal unavailable")
	}
	t.Cleanup(func() {
		writeTerminalSequence = originalWrite
	})

	msg := enableAlternateScrollCmd()()
	if _, ok := msg.(terminalSequenceWriteErrMsg); !ok {
		t.Fatalf("terminal write failure message = %T, want terminalSequenceWriteErrMsg", msg)
	}
	model := updateUIModel(t, newProjectedStaticUIModel(), msg)
	if !model.Transition().Exit || !model.forcedLocalExit {
		t.Fatalf("terminal write failure did not request a clean local exit: transition=%+v forced=%t", model.Transition(), model.forcedLocalExit)
	}
}
