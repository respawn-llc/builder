package runtime

import "core/shared/transcript"

func toolCompletionDiagnosticFact(diagnostic transcript.DeveloperDiagnostic) TranscriptCommittedRowFact {
	return TranscriptCommittedRowFact{
		Kind:       TranscriptCommittedRowFactNotice,
		Visibility: transcript.EntryVisibilityOngoing,
		Notice: &TranscriptNoticeRowFact{
			Reason:              transcript.NoticeReasonRuntimeDiagnostic,
			Severity:            transcript.NoticeSeverityError,
			DeveloperDiagnostic: transcript.CloneDeveloperDiagnostic(&diagnostic),
		},
	}
}
