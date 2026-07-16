package clientuitest

import "core/shared/clientui"

func LegacyTranscriptDiagnostic(code clientui.TranscriptDiagnosticCode, detail string) *clientui.TranscriptDiagnostic {
	return &clientui.TranscriptDiagnostic{Code: &code, Detail: &detail}
}
