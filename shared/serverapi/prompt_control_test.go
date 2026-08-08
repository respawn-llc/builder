package serverapi

import (
	"testing"

	"core/shared/clientui"
)

func TestApprovalAnswerRejectsBlankPresentCommentary(t *testing.T) {
	for _, commentary := range []string{"", " \t "} {
		t.Run(commentary, func(t *testing.T) {
			request := ApprovalAnswerRequest{
				ClientRequestID: "request-1",
				SessionID:       "session-1",
				ApprovalID:      "approval-1",
				Decision:        clientui.ApprovalDecisionAllowOnce,
				Commentary:      &commentary,
			}
			if err := request.Validate(); err == nil {
				t.Fatalf("blank commentary %q unexpectedly validated", commentary)
			}
		})
	}
}
