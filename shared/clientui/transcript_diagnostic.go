package clientui

import (
	"fmt"
	"strings"

	"core/shared/transcript"
)

type TranscriptDiagnosticCode string

type TranscriptDiagnostic struct {
	Code      *TranscriptDiagnosticCode
	Detail    *string
	Developer *transcript.DeveloperDiagnostic
}

func (d TranscriptDiagnostic) Validate() error {
	hasLegacy := d.Code != nil || d.Detail != nil
	hasDeveloper := d.Developer != nil
	if hasLegacy == hasDeveloper {
		return fmt.Errorf("transcript diagnostic must contain exactly one legacy or developer variant")
	}
	if hasLegacy {
		if d.Code == nil || strings.TrimSpace(string(*d.Code)) == "" {
			return fmt.Errorf("transcript diagnostic legacy code is required")
		}
		if d.Detail == nil || strings.TrimSpace(*d.Detail) == "" {
			return fmt.Errorf("transcript diagnostic legacy detail is required")
		}
	}
	if d.Developer != nil {
		if err := d.Developer.Validate(); err != nil {
			return fmt.Errorf("transcript developer diagnostic: %w", err)
		}
	}
	return nil
}
