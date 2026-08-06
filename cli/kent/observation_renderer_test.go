package main

import (
	"bytes"
	"io"
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

func TestRunWatchRendersInterruptedReasonAndDiagnostic(t *testing.T) {
	reason := "interrupted"
	diagnostic := "stop detail"
	var output bytes.Buffer
	code := writeRunWatchResponse(&output, io.Discard, serverapi.RuntimeLiveWatchResponse{
		SessionID: "session-dynamic",
		Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchInterrupted,
			Failure: &serverapi.RuntimeLiveWatchFailure{
				Reason: reason, Diagnostic: &diagnostic,
			},
		},
	}, "")
	if code != 130 {
		t.Fatalf("writeRunWatchResponse exit code = %d, want 130", code)
	}
	for _, value := range []string{reason, diagnostic} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output %q does not contain %q", output.String(), value)
		}
	}
}
