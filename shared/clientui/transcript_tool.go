package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

type ToolCallID string

type TranscriptToolStart struct {
	StepID       runtimeids.StepID
	ToolCallID   ToolCallID
	ToolName     string
	Presentation *transcript.ToolCallMeta
}

type ToolAbortReason string

const (
	ToolAbortCanceled ToolAbortReason = "canceled"
	ToolAbortFailed   ToolAbortReason = "failed"
)

type TranscriptToolAbort struct {
	StepID     runtimeids.StepID
	ToolCallID ToolCallID
	Reason     ToolAbortReason
	Diagnostic *TranscriptDiagnostic
}

func (s TranscriptToolStart) Validate() error {
	if s.StepID.IsZero() {
		return fmt.Errorf("tool start step id is required")
	}
	if err := s.ToolCallID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.ToolName) == "" {
		return fmt.Errorf("tool start tool name is required")
	}
	return nil
}

func (a TranscriptToolAbort) Validate() error {
	if a.StepID.IsZero() {
		return fmt.Errorf("tool abort step id is required")
	}
	if err := a.ToolCallID.Validate(); err != nil {
		return err
	}
	switch a.Reason {
	case ToolAbortCanceled:
		if a.Diagnostic != nil {
			return fmt.Errorf("canceled tool abort cannot carry diagnostic")
		}
		return nil
	case ToolAbortFailed:
		if a.Diagnostic == nil {
			return fmt.Errorf("failed tool abort requires diagnostic")
		}
		return a.Diagnostic.Validate()
	default:
		return fmt.Errorf("unknown tool abort reason %q", a.Reason)
	}
}

func (id ToolCallID) Validate() error {
	if strings.TrimSpace(string(id)) == "" {
		return fmt.Errorf("tool call id is required")
	}
	return nil
}
