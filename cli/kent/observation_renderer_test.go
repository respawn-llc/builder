package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestObservedQuestionUsesDynamicQuestionAndAnswerTarget(t *testing.T) {
	question := serverapi.ObservationQuestion{Approval: &clientui.PendingApproval{
		PromptID: "approval-dynamic", SessionID: questionCommandSessionID(t),
		StepID: questionCommandStepID(), Question: "dynamic question",
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

func TestRunWatchApprovalHintIncludesOptionalCommentary(t *testing.T) {
	sessionID := "session-dynamic"
	var output bytes.Buffer
	code := writeRunWatchResponse(&output, io.Discard, serverapi.RuntimeLiveWatchResponse{
		SessionID: sessionID,
		Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchQuestion,
			Question: &serverapi.ObservationQuestion{Approval: &clientui.PendingApproval{
				PromptID: "approval-dynamic", SessionID: questionCommandSessionID(t),
				StepID: questionCommandStepID(), Question: "Allow access?",
				Options: []clientui.ApprovalOption{{
					Label: "Allow once",
				}},
			}},
		},
	}, "")
	if code != 0 {
		t.Fatalf("writeRunWatchResponse exit code = %d", code)
	}
	hint := commandString([]string{
		config.Command, "question", "answer",
		"--session", sessionID,
		"--option", "<number>",
		"--commentary", "<commentary>",
	})
	if !strings.Contains(output.String(), hint) {
		t.Fatalf("output %q does not contain answer hint %q", output.String(), hint)
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
