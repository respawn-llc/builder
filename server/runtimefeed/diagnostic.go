package runtimefeed

import (
	"fmt"
	"strings"
)

type TranscriptDiagnosticCode string

type TranscriptDiagnostic struct {
	Code   TranscriptDiagnosticCode
	Detail string
}

func (d TranscriptDiagnostic) Validate() error {
	if strings.TrimSpace(string(d.Code)) == "" {
		return fmt.Errorf("transcript diagnostic code is required")
	}
	if strings.TrimSpace(d.Detail) == "" {
		return fmt.Errorf("transcript diagnostic detail is required")
	}
	return nil
}
