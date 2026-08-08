package clientui

import (
	"time"

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
	ApprovalID string
	SessionID  string
	Question   string
	Options    []ApprovalOption
	CreatedAt  time.Time
}
