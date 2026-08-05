package main

import (
	"bytes"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestRunWatchIsRecognizedAsLiveControl(t *testing.T) {
	if got := liveControlSubcommand([]string{"watch"}); got != "watch" {
		t.Fatalf("liveControlSubcommand(watch) = %q", got)
	}
}

func TestRunWatchMalformedSessionStillUsesWatchRoute(t *testing.T) {
	if got := liveControlSubcommand([]string{"watch", "invalid-session"}); got != "watch" {
		t.Fatalf("liveControlSubcommand malformed selector = %q, want watch", got)
	}
	if _, err := parseCLILiveSessionID("invalid-session"); err == nil {
		t.Fatal("parseCLILiveSessionID accepted a non-canonical selector")
	}
}

func TestTaskObservationRendersDiscriminatorAndTaskTargetForOneQuestion(t *testing.T) {
	sessionID := "session-1"
	response := serverapi.WorkflowTaskObservationResponse{
		TaskID: "task-1", TaskShortID: "T-1",
		Outcomes: []serverapi.WorkflowTaskObservationOutcome{{
			Kind:      serverapi.WorkflowTaskObservationQuestion,
			SessionID: &sessionID,
			NodeKey:   stringPointerForTest("build"),
			Question: &serverapi.ObservationQuestion{Ask: &clientui.PendingAsk{
				AskID: "ask-1", SessionID: sessionID, Question: "Proceed?",
			}},
		}},
	}
	var output bytes.Buffer
	if code := writeTaskObservation(&output, response, "project"); code != 0 {
		t.Fatalf("writeTaskObservation exit code = %d", code)
	}
	text := output.String()
	if !strings.Contains(text, "Session session-1 (Node build):") ||
		!strings.Contains(text, "kent question answer --task T-1") ||
		strings.Contains(text, "--session session-1") {
		t.Fatalf("task observation output = %q", text)
	}
}

func TestTaskObservationUsesSessionTargetsForParallelQuestions(t *testing.T) {
	firstSession, secondSession := "session-1", "session-2"
	response := serverapi.WorkflowTaskObservationResponse{
		TaskID: "task-1", TaskShortID: "T-1",
		Outcomes: []serverapi.WorkflowTaskObservationOutcome{
			{
				Kind: serverapi.WorkflowTaskObservationQuestion, SessionID: &firstSession,
				Question: &serverapi.ObservationQuestion{Ask: &clientui.PendingAsk{AskID: "ask-1", SessionID: firstSession, Question: "One?"}},
			},
			{
				Kind: serverapi.WorkflowTaskObservationQuestion, SessionID: &secondSession,
				Question: &serverapi.ObservationQuestion{Ask: &clientui.PendingAsk{AskID: "ask-2", SessionID: secondSession, Question: "Two?"}},
			},
		},
	}
	var output bytes.Buffer
	if code := writeTaskObservation(&output, response, "."); code != 0 {
		t.Fatalf("writeTaskObservation exit code = %d", code)
	}
	text := output.String()
	if !strings.Contains(text, "kent question answer --session session-1") ||
		!strings.Contains(text, "kent question answer --session session-2") {
		t.Fatalf("parallel task observation output = %q", text)
	}
}

func stringPointerForTest(value string) *string {
	return &value
}

func TestTaskWaitAndWatchReachObservationArgumentValidation(t *testing.T) {
	for _, verb := range []string{"wait", "watch"} {
		var stdout, stderr bytes.Buffer
		if code := taskSubcommand([]string{verb}, &stdout, &stderr); code != 2 {
			t.Fatalf("task %s exit code = %d, want 2", verb, code)
		}
		if !strings.Contains(stderr.String(), "task reference is required") {
			t.Fatalf("task %s stderr = %q", verb, stderr.String())
		}
	}
}
