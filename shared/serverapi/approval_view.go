package serverapi

import (
	"strings"

	"core/shared/clientui"
)

// FindPendingApproval returns the authoritative pending approval selected by
// its typed identity.
func FindPendingApproval(approvals []clientui.PendingApproval, approvalID string) (clientui.PendingApproval, bool) {
	if strings.TrimSpace(approvalID) == "" {
		return clientui.PendingApproval{}, false
	}
	for _, approval := range approvals {
		if approval.ApprovalID == approvalID {
			return approval, true
		}
	}
	return clientui.PendingApproval{}, false
}

type ApprovalListPendingBySessionRequest struct {
	SessionID string
}

type ApprovalListPendingBySessionResponse struct {
	Approvals []clientui.PendingApproval
}

func (r ApprovalListPendingBySessionRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
