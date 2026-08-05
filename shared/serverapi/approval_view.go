package serverapi

import (
	"core/shared/clientui"
)

type ApprovalListPendingBySessionRequest struct {
	SessionID string
}

type ApprovalListPendingBySessionResponse struct {
	Approvals []clientui.PendingApproval
}

func (r ApprovalListPendingBySessionRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
