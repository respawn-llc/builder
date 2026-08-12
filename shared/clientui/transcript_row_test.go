package clientui

import (
	"testing"

	"core/shared/textutil"
	"core/shared/transcript"
)

func TestTranscriptCommittedAssistantRowCarriesStepAndOptionalStreamIdentity(t *testing.T) {
	row := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       TranscriptRowAssistant,
		Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
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

func TestTranscriptMessageRowsValidateCommittedTime(t *testing.T) {
	outOfRange := transcript.CommittedAtUnixMs(transcript.MaxCommittedAtUnixMs + 1)
	user := TranscriptUserRow{StepID: transcriptTestStepID(t), Text: "user", CommittedAtUnixMs: &outOfRange}
	if err := user.Validate(); err == nil {
		t.Fatal("user row accepted out-of-range committed time")
	}
	assistant := TranscriptAssistantRow{
		StepID:            transcriptTestStepID(t),
		Text:              "assistant",
		Phase:             transcript.AssistantPhaseFinal,
		CommittedAtUnixMs: &outOfRange,
	}
	if err := assistant.Validate(); err == nil {
		t.Fatal("assistant row accepted out-of-range committed time")
	}
}

func TestTranscriptCommittedRowValidateStructureOwnsPayloadDiscriminator(t *testing.T) {
	row := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       TranscriptRowReasoningTrace,
		ReasoningTrace: &TranscriptReasoningTraceRow{
			StepID:      transcriptTestStepID(t),
			CompactText: "Planning",
			Text:        "Planning\nDetails",
		},
	}
	if err := row.ValidateStructure(); err != nil {
		t.Fatalf("validate committed reasoning row structure: %v", err)
	}
	row.ReasoningTrace = nil
	if err := row.ValidateStructure(); err == nil {
		t.Fatal("accepted committed reasoning row without its payload")
	}
}

func TestTranscriptCommittedRowRejectsImplicitVisibilityAndMismatchedPayload(t *testing.T) {
	base := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       TranscriptRowAssistant,
		Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
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
			LostInputTokens: textutil.Value(100),
			Visibility:      transcript.EntryVisibilityOngoing,
		},
	}
	if err := notice.Validate(); err != nil {
		t.Fatalf("validate typed cache-warning notice: %v", err)
	}
}

func TestTranscriptNoticeRowCarriesTypedCompactionFacts(t *testing.T) {
	messageType := TranscriptMessageCompactionSummary
	notice := TranscriptNoticeRow{
		Reason:      TranscriptNoticeCompaction,
		Severity:    TranscriptNoticeInfo,
		MessageType: &messageType,
		Compaction: &TranscriptCompactionNotice{
			Count:  textutil.Value(2),
			Detail: textutil.Value("provider summary"),
		},
	}
	if err := notice.Validate(); err != nil {
		t.Fatalf("validate typed compaction notice: %v", err)
	}
	notice.Compaction.Count = textutil.Value(0)
	if err := notice.Validate(); err == nil {
		t.Fatal("accepted present zero compaction count")
	}
}

func TestTranscriptNoticeRowCarriesTypedToolOutputRepairFacts(t *testing.T) {
	notice := TranscriptNoticeRow{
		Reason:   TranscriptNoticeToolOutputRepair,
		Severity: TranscriptNoticeWarning,
		ToolOutputRepair: &transcript.ToolOutputRepairNotice{
			Kind:  transcript.ToolOutputRepairFreshResource,
			Count: 2,
		},
	}
	if err := notice.Validate(); err != nil {
		t.Fatalf("validate typed tool-output repair notice: %v", err)
	}
	notice.ToolOutputRepair.Count = 0
	if err := notice.Validate(); err == nil {
		t.Fatal("accepted zero repaired-call count")
	}
}

func TestTranscriptCacheWarningAcceptsAbsentLossAndRejectsPresentZero(t *testing.T) {
	warning := TranscriptCacheWarning{
		Scope:      "conversation",
		Reason:     "cache_miss",
		Visibility: transcript.EntryVisibilityOngoing,
	}
	if err := warning.Validate(); err != nil {
		t.Fatalf("validate absent token loss: %v", err)
	}
	warning.LostInputTokens = textutil.Value(0)
	if err := warning.Validate(); err == nil {
		t.Fatal("accepted present zero token loss")
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
			Reason:   TranscriptNoticeCompaction,
			Severity: TranscriptNoticeInfo,
		},
		{
			Reason:   TranscriptNoticeToolOutputRepair,
			Severity: TranscriptNoticeWarning,
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
			Diagnostic: &TranscriptDiagnostic{
				Code:   TranscriptDiagnosticCode("runtime"),
				Detail: "contradictory",
			},
		},
		{
			Reason:   TranscriptNoticeRuntimeDiagnostic,
			Severity: TranscriptNoticeError,
			CacheWarning: &TranscriptCacheWarning{
				Scope:      "conversation",
				Reason:     "cache_miss",
				Visibility: transcript.EntryVisibilityOngoing,
			},
			Diagnostic: &TranscriptDiagnostic{
				Code:   TranscriptDiagnosticCode("runtime"),
				Detail: "failed",
			},
		},
		{
			Reason:     TranscriptNoticeLegacyUntypedNotice,
			Severity:   TranscriptNoticeInfo,
			LegacyText: &legacyText,
			Diagnostic: &TranscriptDiagnostic{
				Code:   TranscriptDiagnosticCode("runtime"),
				Detail: "contradictory",
			},
		},
	}
	for _, notice := range tests {
		if err := notice.Validate(); err == nil {
			t.Fatalf("accepted notice without required typed payload: %+v", notice)
		}
	}
}
