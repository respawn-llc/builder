package app

import (
	"testing"

	"core/shared/clientui"
)

func TestMountedTUIOwnsProviderTurnStateDiagnosticCopy(t *testing.T) {
	for _, code := range []clientui.OperationalDiagnosticCode{
		clientui.OperationalDiagnosticProviderTurnStateInvalid,
		clientui.OperationalDiagnosticProviderTurnStateConflict,
	} {
		model := &uiModel{}
		cmd := model.applyTranscriptProviderStateDiagnostic(
			clientui.TranscriptProviderStateDiagnostic{Code: code},
		)
		if cmd == nil {
			t.Fatalf("%q produced no mounted TUI command", code)
		}
		if model.transientStatus == "" || model.transientStatusKind != uiStatusNoticeError {
			t.Fatalf("%q mounted status = %q kind=%d", code, model.transientStatus, model.transientStatusKind)
		}
	}
}
