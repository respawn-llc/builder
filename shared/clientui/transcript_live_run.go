package clientui

import (
	"fmt"
	"strings"
	"time"
)

type LiveRunStatus string
type LiveRunResultKind string
type LiveRunNoFinalReason string

const (
	LiveRunStatusCompleted   LiveRunStatus = "completed"
	LiveRunStatusInterrupted LiveRunStatus = "interrupted"
	LiveRunStatusFailed      LiveRunStatus = "failed"

	LiveRunResultAssistantFinalAnswer LiveRunResultKind = "assistant_final_answer"
	LiveRunResultNoFinalAnswer        LiveRunResultKind = "no_final_answer"
)

type TranscriptLiveRunResult struct {
	Status        LiveRunStatus
	ResultKind    LiveRunResultKind
	NoFinalReason LiveRunNoFinalReason
	WorkPerformed bool
	FinalAnswer   *string
	Failure       *string
	StartedAt     time.Time
	FinishedAt    time.Time
}

func (r TranscriptLiveRunResult) Validate() error {
	switch r.Status {
	case LiveRunStatusCompleted, LiveRunStatusInterrupted, LiveRunStatusFailed:
	default:
		return fmt.Errorf("unknown live-run status %q", r.Status)
	}
	switch r.ResultKind {
	case LiveRunResultAssistantFinalAnswer:
		if r.FinalAnswer == nil {
			return fmt.Errorf("final-answer live run requires final answer")
		}
		if r.Failure != nil {
			return fmt.Errorf("final-answer live run cannot carry failure")
		}
	case LiveRunResultNoFinalAnswer:
		if r.FinalAnswer != nil {
			return fmt.Errorf("no-final live run cannot carry final answer")
		}
		if r.Status == LiveRunStatusFailed && (r.Failure == nil || strings.TrimSpace(*r.Failure) == "") {
			return fmt.Errorf("failed live run requires failure")
		}
	default:
		return fmt.Errorf("unknown live-run result kind %q", r.ResultKind)
	}
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return fmt.Errorf("live-run timestamps are required")
	}
	return nil
}
