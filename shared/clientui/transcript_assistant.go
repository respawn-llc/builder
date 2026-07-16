package clientui

import (
	"fmt"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

type TranscriptAssistantStream struct {
	StepID   runtimeids.StepID
	StreamID runtimeids.AssistantStreamID
	Text     string
	Phase    transcript.AssistantPhase
}

type TranscriptAssistantDelta struct {
	StepID   runtimeids.StepID
	StreamID runtimeids.AssistantStreamID
	Delta    string
	Phase    transcript.AssistantPhase
}

type AssistantStreamAbortReason string

const (
	AssistantStreamAbortInterrupted AssistantStreamAbortReason = "interrupted"
	AssistantStreamAbortFailed      AssistantStreamAbortReason = "failed"
	AssistantStreamAbortSuperseded  AssistantStreamAbortReason = "superseded"
)

type TranscriptAssistantStreamAbort struct {
	StepID     runtimeids.StepID
	StreamID   runtimeids.AssistantStreamID
	Reason     AssistantStreamAbortReason
	Diagnostic *TranscriptDiagnostic
}

func (s TranscriptAssistantStream) Validate() error {
	if s.StepID.IsZero() {
		return fmt.Errorf("assistant stream step id is required")
	}
	if s.StreamID.IsZero() {
		return fmt.Errorf("assistant stream id is required")
	}
	if s.Text == "" {
		return fmt.Errorf("assistant stream text is required")
	}
	switch s.Phase {
	case transcript.AssistantPhaseCommentary, transcript.AssistantPhaseFinal:
		return nil
	default:
		return fmt.Errorf("unknown assistant stream phase %q", s.Phase)
	}
}

func (d TranscriptAssistantDelta) Validate() error {
	if d.StepID.IsZero() {
		return fmt.Errorf("assistant delta step id is required")
	}
	if d.StreamID.IsZero() {
		return fmt.Errorf("assistant delta stream id is required")
	}
	if d.Delta == "" {
		return fmt.Errorf("assistant delta text is required")
	}
	switch d.Phase {
	case transcript.AssistantPhaseCommentary, transcript.AssistantPhaseFinal:
		return nil
	default:
		return fmt.Errorf("unknown assistant delta phase %q", d.Phase)
	}
}

func (a TranscriptAssistantStreamAbort) Validate() error {
	if a.StepID.IsZero() {
		return fmt.Errorf("assistant stream abort step id is required")
	}
	if a.StreamID.IsZero() {
		return fmt.Errorf("assistant stream abort stream id is required")
	}
	switch a.Reason {
	case AssistantStreamAbortInterrupted, AssistantStreamAbortSuperseded:
		if a.Diagnostic != nil {
			return fmt.Errorf("%s assistant stream abort cannot carry diagnostic", a.Reason)
		}
		return nil
	case AssistantStreamAbortFailed:
		if a.Diagnostic == nil {
			return fmt.Errorf("failed assistant stream abort requires diagnostic")
		}
		return a.Diagnostic.Validate()
	default:
		return fmt.Errorf("unknown assistant stream abort reason %q", a.Reason)
	}
}
