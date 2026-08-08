package sessioncontract

import (
	"errors"
	"fmt"
	"strings"
)

type PromptApprovalDecision string

const (
	PromptApprovalDecisionAllowOnce    PromptApprovalDecision = "allow_once"
	PromptApprovalDecisionAllowSession PromptApprovalDecision = "allow_session"
	PromptApprovalDecisionDeny         PromptApprovalDecision = "deny"
)

var ErrPromptQuestionAnswerRequired = errors.New("question answer requires a selected option or freeform text")

func ValidatePromptQuestionAnswerShape(selectedOptionNumber *int, freeform *string) error {
	if selectedOptionNumber != nil && *selectedOptionNumber <= 0 {
		return errors.New("selected option number must be positive when present")
	}
	if freeform != nil && strings.TrimSpace(*freeform) == "" {
		return errors.New("freeform answer must be non-blank when present")
	}
	if selectedOptionNumber == nil && freeform == nil {
		return ErrPromptQuestionAnswerRequired
	}
	return nil
}

func ValidatePromptApprovalDecision(decision PromptApprovalDecision) error {
	switch decision {
	case PromptApprovalDecisionAllowOnce,
		PromptApprovalDecisionAllowSession,
		PromptApprovalDecisionDeny:
		return nil
	default:
		return fmt.Errorf("approval decision %q is invalid", decision)
	}
}

func ValidatePromptApprovalAnswerShape(decision PromptApprovalDecision, commentary *string) error {
	if err := ValidatePromptApprovalDecision(decision); err != nil {
		return err
	}
	if commentary != nil && strings.TrimSpace(*commentary) == "" {
		return errors.New("approval commentary must be non-blank when present")
	}
	return nil
}
