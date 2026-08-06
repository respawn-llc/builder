package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"core/cli/app"
	"core/shared/clientui"
	"core/shared/config"
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

func TestLiveControlSubcommandPreservesHeadlessControlVerbPrompts(t *testing.T) {
	for _, args := range [][]string{
		{"wait", "for", "CI", "to", "finish"},
		{"stop", "the", "run", "after", "CI"},
		{"steer", "the", "agent", "toward", "the", "fix"},
	} {
		if got := liveControlSubcommand(args); got != "" {
			t.Fatalf("liveControlSubcommand(%v) = %q, want headless prompt", args, got)
		}
	}
}

func TestRunWatchStreamFailureDoesNotUseInterruptExitCode(t *testing.T) {
	run := func(context.Context, app.Options, runtimeids.SessionID) (serverapi.RuntimeLiveWatchResponse, error) {
		return serverapi.RuntimeLiveWatchResponse{}, fmt.Errorf("%w: %v", serverapi.ErrStreamFailed, context.Canceled)
	}
	if code := runLiveWatchSubcommandWithRunner([]string{runtimeids.NewSessionID().String()}, run); code != 1 {
		t.Fatalf("runLiveWatchSubcommand stream failure exit code = %d, want 1", code)
	}
}

func TestRunWatchCallerCancellationKeepsInterruptExitCode(t *testing.T) {
	run := func(context.Context, app.Options, runtimeids.SessionID) (serverapi.RuntimeLiveWatchResponse, error) {
		return serverapi.RuntimeLiveWatchResponse{}, context.Canceled
	}
	if code := runLiveWatchSubcommandWithRunner([]string{runtimeids.NewSessionID().String()}, run); code != 130 {
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
	if code := writeTaskObservation(&output, io.Discard, response, projectRef); code != 0 {
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
	if code := writeTaskObservation(&output, io.Discard, response, "."); code != 0 {
		t.Fatalf("writeTaskObservation exit code = %d", code)
	}
	text := output.String()
	firstHint := commandString([]string{config.Command, "question", "answer", "--session", firstSession})
	secondHint := commandString([]string{config.Command, "question", "answer", "--session", secondSession})
	if !strings.Contains(text, firstHint) || !strings.Contains(text, secondHint) {
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
	}
}
