package main

import (
	"bytes"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestObservedQuestionUsesDynamicQuestionAndAnswerTarget(t *testing.T) {
	question := serverapi.ObservationQuestion{Approval: &clientui.PendingApproval{
		ApprovalID: "approval-dynamic", Question: "dynamic question",
		Options: []clientui.ApprovalOption{{Label: "dynamic allow", Decision: clientui.ApprovalDecisionAllowOnce}},
	}}
	var output bytes.Buffer
	writeObservedQuestion(&output, question, "kent question answer --session session-dynamic --option <number>")
	for _, value := range []string{"dynamic question", "dynamic allow", "session-dynamic"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output %q does not contain dynamic value %q", output.String(), value)
		}
	}
}
