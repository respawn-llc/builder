package transcript

import (
	"fmt"
	"strings"
)

type AssistantPhase string

const (
	AssistantPhaseCommentary  AssistantPhase = "commentary"
	AssistantPhaseFinal       AssistantPhase = "final_answer"
	AssistantPhaseLegacyFinal AssistantPhase = "legacy_final_answer"
)

func ClassifyAssistantPhase(raw string) AssistantPhase {
	if phase, ok := ParseExplicitAssistantPhase(raw); ok {
		return phase
	}
	if strings.TrimSpace(raw) == "" {
		return AssistantPhaseLegacyFinal
	}
	panic(fmt.Sprintf("unsupported transcript assistant phase %q", raw))
}

func ParseExplicitAssistantPhase(raw string) (AssistantPhase, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "commentary":
		return AssistantPhaseCommentary, true
	case "final_answer", "finalanswer", "final":
		return AssistantPhaseFinal, true
	default:
		return "", false
	}
}
