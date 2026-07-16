package clientui

import (
	"testing"

	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestTranscriptCommittedAssistantRowCarriesStepAndOptionalStreamIdentity(t *testing.T) {
	row := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       TranscriptRowAssistant,
		Assistant: &TranscriptAssistantRow{
			StepID: transcriptTestStepID(t),
			Text:   "Done",
			Phase:  transcript.AssistantPhaseFinal,
		},
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("validate committed assistant row: %v", err)
	}
}

func TestTranscriptCommittedRowRejectsImplicitVisibilityAndMismatchedPayload(t *testing.T) {
	base := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       TranscriptRowAssistant,
		Assistant: &TranscriptAssistantRow{
			StepID: transcriptTestStepID(t),
			Text:   "Done",
			Phase:  transcript.AssistantPhaseFinal,
		},
	}
	tests := []TranscriptCommittedRow{
		func() TranscriptCommittedRow {
			row := base
			row.Visibility = transcript.EntryVisibilityAuto
			return row
		}(),
		func() TranscriptCommittedRow {
			row := base
			row.Integrity = transcript.RowIntegrity(9)
			return row
		}(),
		func() TranscriptCommittedRow {
			row := base
			row.Kind = TranscriptRowTool
			return row
		}(),
		func() TranscriptCommittedRow {
			row := base
			row.User = &TranscriptUserRow{StepID: transcriptTestStepID(t), Text: "hello"}
			return row
		}(),
		func() TranscriptCommittedRow {
			row := base
			row.Assistant = &TranscriptAssistantRow{
				Text:  "Done",
				Phase: transcript.AssistantPhaseFinal,
			}
			return row
		}(),
	}
	for _, row := range tests {
		if err := row.Validate(); err == nil {
			t.Fatalf("accepted invalid committed row: %+v", row)
		}
	}
}

func TestTranscriptNoticeRowCarriesTypedCacheWarningFacts(t *testing.T) {
	notice := TranscriptNoticeRow{
		Reason:   TranscriptNoticeCacheWarning,
		Severity: TranscriptNoticeWarning,
		CacheWarning: &TranscriptCacheWarning{
			Scope:           "conversation",
			Reason:          "cache_miss",
			LostInputTokens: 100,
			Visibility:      transcript.EntryVisibilityOngoing,
		},
	}
	if err := notice.Validate(); err != nil {
		t.Fatalf("validate typed cache-warning notice: %v", err)
	}
}

func TestTranscriptNoticeRowRejectsReasonPayloadMismatch(t *testing.T) {
	legacyText := "legacy notice"
	tests := []TranscriptNoticeRow{
		{
			Reason:   TranscriptNoticeCacheWarning,
			Severity: TranscriptNoticeWarning,
		},
		{
			Reason:   TranscriptNoticeRuntimeDiagnostic,
			Severity: TranscriptNoticeError,
		},
		{
			Reason:   TranscriptNoticeLegacyUntypedNotice,
			Severity: TranscriptNoticeInfo,
		},
		{
			Reason:   TranscriptNoticeCacheWarning,
			Severity: TranscriptNoticeWarning,
			CacheWarning: &TranscriptCacheWarning{
				Scope:      "conversation",
				Reason:     "cache_miss",
				Visibility: transcript.EntryVisibilityOngoing,
			},
			Diagnostic: NewLegacyTranscriptDiagnostic(
				TranscriptDiagnosticCode("runtime"),
				"contradictory",
			),
		},
		{
			Reason:   TranscriptNoticeRuntimeDiagnostic,
			Severity: TranscriptNoticeError,
			CacheWarning: &TranscriptCacheWarning{
				Scope:      "conversation",
				Reason:     "cache_miss",
				Visibility: transcript.EntryVisibilityOngoing,
			},
			Diagnostic: NewLegacyTranscriptDiagnostic(
				TranscriptDiagnosticCode("runtime"),
				"failed",
			),
		},
		{
			Reason:     TranscriptNoticeLegacyUntypedNotice,
			Severity:   TranscriptNoticeInfo,
			LegacyText: &legacyText,
			Diagnostic: NewLegacyTranscriptDiagnostic(
				TranscriptDiagnosticCode("runtime"),
				"contradictory",
			),
		},
	}
	for _, notice := range tests {
		if err := notice.Validate(); err == nil {
			t.Fatalf("accepted notice without required typed payload: %+v", notice)
		}
	}
}

func TestTranscriptNoticeRowCarriesTypedDeveloperDiagnosticWithoutLegacyFields(t *testing.T) {
	diagnostic := transcript.NewDeletionFactMismatchDeveloperDiagnostic(
		"call-1",
		patchformat.WholeFileDeletionFactMismatchError{
			Kind: patchformat.WholeFileDeletionFactMismatchMissing,
			ID:   patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		},
	)
	notice := TranscriptNoticeRow{
		Reason:     TranscriptNoticeRuntimeDiagnostic,
		Severity:   TranscriptNoticeError,
		Diagnostic: NewDeveloperTranscriptDiagnostic(diagnostic),
	}
	if err := notice.Validate(); err != nil {
		t.Fatalf("validate typed developer diagnostic notice: %v", err)
	}

	legacyCode := TranscriptDiagnosticCode("legacy")
	legacyDetail := "must not coexist"
	notice.Diagnostic.Code = &legacyCode
	notice.Diagnostic.Detail = &legacyDetail
	if err := notice.Validate(); err == nil {
		t.Fatal("accepted developer diagnostic with legacy code and detail")
	}
}
