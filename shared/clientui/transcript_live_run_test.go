package clientui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/shared/textutil"
)

func TestTranscriptLiveRunBatchFinishedValidatesVariantPayloads(t *testing.T) {
	finishedAt := time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC)
	workflow := LiveRunBatchExclusionWorkflowCompleted
	nonTask := LiveRunBatchExclusionNonTaskActivity
	preview := TranscriptFinalAnswerPreview{Markdown: "final answer"}
	diagnostic := TranscriptDiagnostic{Code: TranscriptDiagnosticCode("runtime_failure"), Detail: "provider failed"}
	tests := []TranscriptLiveRunBatchFinished{
		{
			Disposition:        LiveRunBatchDispositionFinalAnswer,
			FinishedAt:         finishedAt,
			WorkPerformed:      true,
			FinalAnswerPreview: &preview,
		},
		{
			Disposition:       LiveRunBatchDispositionRuntimeFailure,
			FinishedAt:        finishedAt,
			FailureDiagnostic: &diagnostic,
		},
		{
			Disposition: LiveRunBatchDispositionNoFinalAnswer,
			FinishedAt:  finishedAt,
		},
		{
			Disposition: LiveRunBatchDispositionInterrupted,
			FinishedAt:  finishedAt,
		},
		{
			Disposition:     LiveRunBatchDispositionExcluded,
			ExclusionReason: &workflow,
			FinishedAt:      finishedAt,
		},
		{
			Disposition:     LiveRunBatchDispositionExcluded,
			ExclusionReason: &nonTask,
			FinishedAt:      finishedAt,
		},
	}
	for _, fact := range tests {
		message := TranscriptMessage{
			Sequence: 2,
			Kind:     TranscriptMessageLiveRunBatchFinished,
			Payload:  TranscriptPayload{LiveRunBatchFinished: &fact},
		}
		if err := message.Validate(); err != nil {
			t.Fatalf("validate %q: %v", fact.Disposition, err)
		}
	}
}

func TestTranscriptLiveRunBatchFinishedRejectsInvalidVariants(t *testing.T) {
	finishedAt := time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC)
	workflow := LiveRunBatchExclusionWorkflowCompleted
	preview := TranscriptFinalAnswerPreview{Markdown: "final answer"}
	unknownTruncation := TranscriptFinalAnswerPreviewTruncation("unknown")
	diagnostic := TranscriptDiagnostic{Code: TranscriptDiagnosticCode("runtime_failure"), Detail: "provider failed"}
	tests := []struct {
		name string
		fact TranscriptLiveRunBatchFinished
	}{
		{
			name: "final answer without preview",
			fact: TranscriptLiveRunBatchFinished{
				Disposition: LiveRunBatchDispositionFinalAnswer,
				FinishedAt:  finishedAt,
			},
		},
		{
			name: "final answer with diagnostic",
			fact: TranscriptLiveRunBatchFinished{
				Disposition:        LiveRunBatchDispositionFinalAnswer,
				FinishedAt:         finishedAt,
				FinalAnswerPreview: &preview,
				FailureDiagnostic:  &diagnostic,
			},
		},
		{
			name: "runtime failure without diagnostic",
			fact: TranscriptLiveRunBatchFinished{
				Disposition: LiveRunBatchDispositionRuntimeFailure,
				FinishedAt:  finishedAt,
			},
		},
		{
			name: "no final answer with payload",
			fact: TranscriptLiveRunBatchFinished{
				Disposition:       LiveRunBatchDispositionNoFinalAnswer,
				FinishedAt:        finishedAt,
				FailureDiagnostic: &diagnostic,
			},
		},
		{
			name: "interrupted with exclusion",
			fact: TranscriptLiveRunBatchFinished{
				Disposition:     LiveRunBatchDispositionInterrupted,
				FinishedAt:      finishedAt,
				ExclusionReason: &workflow,
			},
		},
		{
			name: "excluded without reason",
			fact: TranscriptLiveRunBatchFinished{
				Disposition: LiveRunBatchDispositionExcluded,
				FinishedAt:  finishedAt,
			},
		},
		{
			name: "excluded with preview",
			fact: TranscriptLiveRunBatchFinished{
				Disposition:        LiveRunBatchDispositionExcluded,
				ExclusionReason:    &workflow,
				FinishedAt:         finishedAt,
				FinalAnswerPreview: &preview,
			},
		},
		{
			name: "zero finished time",
			fact: TranscriptLiveRunBatchFinished{
				Disposition:        LiveRunBatchDispositionFinalAnswer,
				FinalAnswerPreview: &preview,
			},
		},
		{
			name: "unknown preview truncation",
			fact: TranscriptLiveRunBatchFinished{
				Disposition: LiveRunBatchDispositionFinalAnswer,
				FinishedAt:  finishedAt,
				FinalAnswerPreview: &TranscriptFinalAnswerPreview{
					Markdown:   "final answer",
					Truncation: &unknownTruncation,
				},
			},
		},
		{
			name: "oversized preview",
			fact: TranscriptLiveRunBatchFinished{
				Disposition: LiveRunBatchDispositionFinalAnswer,
				FinishedAt:  finishedAt,
				FinalAnswerPreview: &TranscriptFinalAnswerPreview{
					Markdown: strings.Repeat("x", textutil.MarkdownSummaryLimitBytes+1),
				},
			},
		},
		{
			name: "invalid UTF-8 preview",
			fact: TranscriptLiveRunBatchFinished{
				Disposition: LiveRunBatchDispositionFinalAnswer,
				FinishedAt:  finishedAt,
				FinalAnswerPreview: &TranscriptFinalAnswerPreview{
					Markdown: string([]byte{0xff}),
				},
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.fact.Validate(); err == nil {
				t.Fatalf("invalid live-run batch fact validated: %+v", testCase.fact)
			}
		})
	}
}

func TestTranscriptLiveRunBatchFinishedOmitsRuntimeIdentities(t *testing.T) {
	factType := reflect.TypeOf(TranscriptLiveRunBatchFinished{})
	for _, forbidden := range []string{"GroupID", "RunID", "StepID"} {
		if _, found := factType.FieldByName(forbidden); found {
			t.Fatalf("client live-run batch fact exposes runtime identity %q", forbidden)
		}
	}
	raw, err := json.Marshal(TranscriptLiveRunBatchFinished{
		Disposition:        LiveRunBatchDispositionFinalAnswer,
		FinishedAt:         time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC),
		FinalAnswerPreview: &TranscriptFinalAnswerPreview{Markdown: "final answer"},
	})
	if err != nil {
		t.Fatalf("marshal live-run batch fact: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("decode live-run batch fact: %v", err)
	}
	for _, forbidden := range []string{"GroupID", "RunID", "StepID"} {
		if _, found := shape[forbidden]; found {
			t.Fatalf("client live-run batch JSON exposes runtime identity %q: %#v", forbidden, shape)
		}
	}
}
