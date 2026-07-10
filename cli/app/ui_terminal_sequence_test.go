package app

import (
	"errors"
	"testing"
)

func TestTerminalSequenceWriteFailureIsDeliveredAsErrorNotice(t *testing.T) {
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
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("terminal write failure notice = %q kind %v, want visible error", model.transientStatus, model.transientStatusKind)
	}
}
