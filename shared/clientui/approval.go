package clientui

import (
	"fmt"
	"time"

	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type ApprovalDecision = sessioncontract.PromptApprovalDecision

const (
	ApprovalDecisionAllowOnce    = sessioncontract.PromptApprovalDecisionAllowOnce
	ApprovalDecisionAllowSession = sessioncontract.PromptApprovalDecisionAllowSession
	ApprovalDecisionDeny         = sessioncontract.PromptApprovalDecisionDeny
)

type ApprovalOption struct {
	Decision ApprovalDecision
}

func ApprovalDecisionLabel(decision ApprovalDecision) string {
	switch decision {
	case ApprovalDecisionAllowOnce:
		return "Allow once"
	case ApprovalDecisionAllowSession:
		return "Allow for this session"
	case ApprovalDecisionDeny:
		return "Deny"
	default:
		panic(fmt.Sprintf("unsupported approval decision %q", decision))
	}
}

type PendingApproval struct {
	PromptID  PromptID
	SessionID runtimeids.SessionID
	StepID    runtimeids.StepID
	Question  string
	Options   []ApprovalOption
	CreatedAt time.Time
}
