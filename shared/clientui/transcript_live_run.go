package clientui

import (
	"fmt"
	"strings"
	"time"

	"core/shared/textutil"
)

type LiveRunBatchDisposition string

const (
	LiveRunBatchDispositionFinalAnswer    LiveRunBatchDisposition = "final_answer"
	LiveRunBatchDispositionRuntimeFailure LiveRunBatchDisposition = "runtime_failure"
	LiveRunBatchDispositionNoFinalAnswer  LiveRunBatchDisposition = "no_final_answer"
	LiveRunBatchDispositionInterrupted    LiveRunBatchDisposition = "interrupted"
	LiveRunBatchDispositionExcluded       LiveRunBatchDisposition = "excluded"
)

type LiveRunBatchExclusionReason string

const (
	LiveRunBatchExclusionWorkflowCompleted LiveRunBatchExclusionReason = "workflow_completed"
	LiveRunBatchExclusionNonTaskActivity   LiveRunBatchExclusionReason = "non_task_activity"
)

type TranscriptFinalAnswerPreviewTruncation string

const TranscriptFinalAnswerPreviewTruncationByteLimit TranscriptFinalAnswerPreviewTruncation = "byte_limit"

type TranscriptFinalAnswerPreview struct {
	Markdown   string
	Truncation *TranscriptFinalAnswerPreviewTruncation
}

type TranscriptLiveRunBatchFinished struct {
	Disposition        LiveRunBatchDisposition
	ExclusionReason    *LiveRunBatchExclusionReason
	FinishedAt         time.Time
	WorkPerformed      bool
	FinalAnswerPreview *TranscriptFinalAnswerPreview
	FailureDiagnostic  *TranscriptDiagnostic
}

func (f TranscriptLiveRunBatchFinished) Validate() error {
	if f.FinishedAt.IsZero() {
		return fmt.Errorf("live-run batch finished time is required")
	}
	switch f.Disposition {
	case LiveRunBatchDispositionFinalAnswer:
		if f.ExclusionReason != nil || f.FinalAnswerPreview == nil || f.FailureDiagnostic != nil {
			return fmt.Errorf("final-answer live-run batch has invalid payload variant")
		}
		return f.FinalAnswerPreview.Validate()
	case LiveRunBatchDispositionRuntimeFailure:
		if f.ExclusionReason != nil || f.FinalAnswerPreview != nil || f.FailureDiagnostic == nil {
			return fmt.Errorf("runtime-failure live-run batch has invalid payload variant")
		}
		return f.FailureDiagnostic.Validate()
	case LiveRunBatchDispositionNoFinalAnswer, LiveRunBatchDispositionInterrupted:
		if f.ExclusionReason != nil || f.FinalAnswerPreview != nil || f.FailureDiagnostic != nil {
			return fmt.Errorf("%s live-run batch has invalid payload variant", f.Disposition)
		}
		return nil
	case LiveRunBatchDispositionExcluded:
		if f.ExclusionReason == nil || f.FinalAnswerPreview != nil || f.FailureDiagnostic != nil {
			return fmt.Errorf("excluded live-run batch has invalid payload variant")
		}
		return f.ExclusionReason.Validate()
	default:
		return fmt.Errorf("unknown live-run batch disposition %q", f.Disposition)
	}
}

func (p TranscriptFinalAnswerPreview) Validate() error {
	if strings.TrimSpace(p.Markdown) == "" {
		return fmt.Errorf("final-answer preview markdown is required")
	}
	if err := textutil.ValidateUTF8ByteLimit(p.Markdown, textutil.MarkdownSummaryLimitBytes); err != nil {
		return fmt.Errorf("final-answer preview markdown: %w", err)
	}
	if p.Truncation == nil {
		return nil
	}
	switch *p.Truncation {
	case TranscriptFinalAnswerPreviewTruncationByteLimit:
		return nil
	default:
		return fmt.Errorf("unknown final-answer preview truncation %q", *p.Truncation)
	}
}

func (r LiveRunBatchExclusionReason) Validate() error {
	switch r {
	case LiveRunBatchExclusionWorkflowCompleted, LiveRunBatchExclusionNonTaskActivity:
		return nil
	default:
		return fmt.Errorf("unknown live-run batch exclusion reason %q", r)
	}
}
