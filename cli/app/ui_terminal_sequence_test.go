package app

import (
	"errors"
	"testing"
)

func TestTerminalSequenceWriteFailureUsesFatalTerminalErrorPolicy(t *testing.T) {
	output := newUITerminalOutput(&terminalOutputScriptWriter{
		results: []terminalOutputWriteResult{{err: errors.New("terminal unavailable")}},
	})

	msg := enableAlternateScrollCmd(output)()
	if _, ok := msg.(terminalSequenceWriteErrMsg); !ok {
		t.Fatalf("terminal write failure message = %T, want terminalSequenceWriteErrMsg", msg)
	}
	model := updateUIModel(t, newProjectedStaticUIModel(), msg)
	if !model.Transition().Exit || !model.forcedLocalExit {
		t.Fatalf("terminal write failure did not request a clean local exit: transition=%+v forced=%t", model.Transition(), model.forcedLocalExit)
	}
}
