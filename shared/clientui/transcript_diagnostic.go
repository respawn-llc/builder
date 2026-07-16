package clientui

import (
	"encoding/json"
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

func NewLegacyTranscriptDiagnostic(code TranscriptDiagnosticCode, detail string) *TranscriptDiagnostic {
	return &TranscriptDiagnostic{Code: &code, Detail: &detail}
}

func NewDeveloperTranscriptDiagnostic(diagnostic transcript.DeveloperDiagnostic) *TranscriptDiagnostic {
	return &TranscriptDiagnostic{Developer: transcript.CloneDeveloperDiagnostic(&diagnostic)}
}

func (d TranscriptDiagnostic) Legacy() (TranscriptDiagnosticCode, string, bool) {
	if d.Code == nil || d.Detail == nil {
		return "", "", false
	}
	return *d.Code, *d.Detail, true
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

func (d TranscriptDiagnostic) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(transcriptDiagnosticWire{
		Code:      d.Code,
		Detail:    d.Detail,
		Developer: d.Developer,
	})
}

func (d *TranscriptDiagnostic) UnmarshalJSON(data []byte) error {
	var wire transcriptDiagnosticWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	decoded := TranscriptDiagnostic{
		Code:      wire.Code,
		Detail:    wire.Detail,
		Developer: wire.Developer,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*d = decoded
	return nil
}

type transcriptDiagnosticWire struct {
	Code      *TranscriptDiagnosticCode       `json:"Code"`
	Detail    *string                         `json:"Detail"`
	Developer *transcript.DeveloperDiagnostic `json:"Developer"`
}
