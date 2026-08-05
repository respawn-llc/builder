package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"core/cli/app"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestRunWatchIsRecognizedAsLiveControl(t *testing.T) {
	if got := liveControlSubcommand([]string{"watch"}); got != "watch" {
		t.Fatalf("liveControlSubcommand(watch) = %q", got)
	}
}

func TestRunWatchMalformedSessionStillUsesWatchRoute(t *testing.T) {
	malformed := "../invalid-session"
	if got := liveControlSubcommand([]string{"watch", malformed}); got != "watch" {
		t.Fatalf("liveControlSubcommand malformed selector = %q, want watch", got)
	}
	if _, err := parseCLILiveSessionID(malformed); err == nil {
		t.Fatal("parseCLILiveSessionID accepted a malformed selector")
	}
	legacy := "legacy-session"
	if got, err := parseCLILiveSessionID(legacy); err != nil || got.String() != legacy {
		t.Fatalf("parseCLILiveSessionID legacy selector = %q, err=%v", got.String(), err)
	}
}

func TestRunWatchStreamFailureDoesNotUseInterruptExitCode(t *testing.T) {
	original := runLiveWatchApp
	t.Cleanup(func() { runLiveWatchApp = original })
	runLiveWatchApp = func(context.Context, app.Options, runtimeids.SessionID) (serverapi.RuntimeLiveWatchResponse, error) {
		return serverapi.RuntimeLiveWatchResponse{}, fmt.Errorf("%w: %v", serverapi.ErrStreamFailed, context.Canceled)
	}
	if code := runLiveWatchSubcommand([]string{runtimeids.NewSessionID().String()}); code != 1 {
		t.Fatalf("runLiveWatchSubcommand stream failure exit code = %d, want 1", code)
	}
}

func TestRunWatchCallerCancellationKeepsInterruptExitCode(t *testing.T) {
	original := runLiveWatchApp
	t.Cleanup(func() { runLiveWatchApp = original })
	runLiveWatchApp = func(context.Context, app.Options, runtimeids.SessionID) (serverapi.RuntimeLiveWatchResponse, error) {
		return serverapi.RuntimeLiveWatchResponse{}, context.Canceled
	}
	if code := runLiveWatchSubcommand([]string{runtimeids.NewSessionID().String()}); code != 130 {
		t.Fatalf("runLiveWatchSubcommand cancellation exit code = %d, want 130", code)
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
	projectRef := "workspace path/$branch"
	if code := writeTaskObservation(&output, response, projectRef); code != 0 {
		t.Fatalf("writeTaskObservation exit code = %d", code)
	}
	text := output.String()
	if !strings.Contains(text, sessionID) ||
		!strings.Contains(text, "build") ||
		!strings.Contains(text, response.TaskShortID) ||
		!strings.Contains(text, "--project "+shellQuote(projectRef)) ||
		strings.Contains(text, "--session "+sessionID) {
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
