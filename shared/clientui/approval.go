package clientui

import (
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
	Label    string
}

type PendingApproval struct {
	PromptID  PromptID
	SessionID runtimeids.SessionID
	StepID    runtimeids.StepID
	Question  string
	Options   []ApprovalOption
	CreatedAt time.Time
}
