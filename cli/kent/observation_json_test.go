package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"core/cli/app"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type observationCommandRemote struct {
	apicontract.WorkflowService
	closed     bool
	closeErr   error
	projectErr error
}

func (r *observationCommandRemote) Close() error {
	r.closed = true
	return r.closeErr
}

func (r *observationCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	if r.projectErr != nil {
		return serverapi.ProjectResolvePathResponse{}, r.projectErr
	}
	return serverapi.ProjectResolvePathResponse{}, errors.New("project resolution should not run")
}

func (r *observationCommandRemote) GetWorkflowTask(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{ID: "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ProjectID: "project"},
	}}, nil
}

func (r *observationCommandRemote) ObserveWorkflowTask(context.Context, serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, error) {
	return serverapi.WorkflowTaskObservationResponse{
		TaskID:      "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		TaskShortID: "T-1",
		Outcomes:    []serverapi.WorkflowTaskObservationOutcome{{Kind: serverapi.WorkflowTaskObservationDone}},
	}, nil
}

func TestTaskObservationJSONUsesTrailingFlagAndClosesRemoteOnce(t *testing.T) {
	remote := &observationCommandRemote{}
	installWorkflowCommandRemote(t, remote)
	var stdout, stderr strings.Builder
	if code := taskWaitSubcommand([]string{"task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if !remote.closed || stderr.Len() != 0 {
		t.Fatalf("closed=%v stderr=%q", remote.closed, stderr.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["status"] != "success" || envelope["target"] == nil {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestTaskObservationJSONPreservesPrimaryResultWithCloseWarning(t *testing.T) {
	remote := &observationCommandRemote{closeErr: errors.New("close failed")}
	installWorkflowCommandRemote(t, remote)
	var stdout, stderr strings.Builder
	if code := taskWaitSubcommand([]string{"--json", "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["status"] != "success" || envelope["warnings"] == nil || stderr.Len() != 0 {
		t.Fatalf("envelope=%#v stderr=%q", envelope, stderr.String())
	}
}

func TestTaskObservationJSONMapsProjectUnavailable(t *testing.T) {
	remote := &observationCommandRemote{projectErr: serverapi.ErrProjectUnavailable}
	installWorkflowCommandRemote(t, remote)
	var stdout, stderr strings.Builder
	if code := taskWaitSubcommand([]string{"--project", "/project", "short-id", "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	var envelope struct {
		Status string `json:"status"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Status != "error" || envelope.Error.Code != "unavailable" || stderr.Len() != 0 || !remote.closed {
		t.Fatalf("envelope=%#v stderr=%q closed=%v", envelope, stderr.String(), remote.closed)
	}
}

func TestObservationErrorPreservesStartupCleanupWarning(t *testing.T) {
	var output bytes.Buffer
	if code := emitObservationError(&output, observationOperationTaskWait, nil, context.Background(), serverapi.ErrProjectUnavailable, nil, func() error {
		return errors.New("close failed")
	}); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	var envelope struct {
		Warnings []string `json:"warnings"`
		Error    struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "unavailable" || len(envelope.Warnings) != 1 || envelope.Warnings[0] != "close failed" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestTaskObservationJSONParseFailureEmitsOneUsageObjectWithoutRemote(t *testing.T) {
	remote := &observationCommandRemote{}
	installWorkflowCommandRemote(t, remote)
	var stdout, stderr strings.Builder
	if code := taskWaitSubcommand([]string{"--json", "--unknown", "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	var envelope struct {
		Status string `json:"status"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Status != "error" || envelope.Error.Code != "usage" || stderr.Len() != 0 || remote.closed {
		t.Fatalf("envelope=%#v stderr=%q closed=%v", envelope, stderr.String(), remote.closed)
	}
}

func TestProjectRunWatchJSONQuestionUsesAnswerTargetAndOrderedSuggestions(t *testing.T) {
	recommended := 2
	response := serverapi.RuntimeLiveWatchResponse{
		SessionID: "response-session",
		Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchQuestion,
			Question: &serverapi.ObservationQuestion{Ask: &clientui.PendingAsk{
				AskID: "ask-1", SessionID: "response-session", Question: "Proceed?",
				Suggestions: []string{"yes", "no"}, RecommendedOptionIndex: &recommended,
			}},
		},
	}
	envelope, code, err := projectRunWatchJSON("requested-session", response)
	if err != nil || code != 0 {
		t.Fatalf("projection = %#v, code=%d, err=%v", envelope, code, err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	target := decoded["target"].(map[string]any)
	if target["session_id"] != "requested-session" {
		t.Fatalf("target = %#v", target)
	}
	outcome := decoded["outcomes"].([]any)[0].(map[string]any)
	if outcome["answer_target"].(map[string]any)["session_id"] != "requested-session" {
		t.Fatalf("answer_target = %#v", outcome["answer_target"])
	}
	if _, present := outcome["session_id"]; present {
		t.Fatal("question repeated an outcome-level session_id")
	}
}

func TestProjectTaskObservationJSONPreservesQuestionNodeAndKeepsDoneEmpty(t *testing.T) {
	sessionID := "question-session"
	nodeKey := "build"
	envelope, _, err := projectTaskObservationJSON("task-id", serverapi.WorkflowTaskObservationResponse{
		TaskID: "response-task", TaskShortID: "T-1",
		Outcomes: []serverapi.WorkflowTaskObservationOutcome{
			{Kind: serverapi.WorkflowTaskObservationDone, SessionID: &sessionID, NodeKey: &nodeKey},
			{Kind: serverapi.WorkflowTaskObservationQuestion, SessionID: &sessionID, NodeKey: &nodeKey,
				Question: &serverapi.ObservationQuestion{Ask: &clientui.PendingAsk{
					AskID: "ask", SessionID: sessionID, Question: "Continue?",
				}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Outcomes []map[string]any `json:"outcomes"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Outcomes) != 2 {
		t.Fatalf("outcomes = %#v", decoded.Outcomes)
	}
	if len(decoded.Outcomes[0]) != 1 || decoded.Outcomes[0]["kind"] != "task_done" {
		t.Fatalf("task_done = %#v", decoded.Outcomes[0])
	}
	if decoded.Outcomes[1]["node_key"] != nodeKey {
		t.Fatalf("question node_key = %#v", decoded.Outcomes[1])
	}
}

func TestProjectObservationJSONStatusPrecedenceAndNoFinalProjection(t *testing.T) {
	noFinal, code, err := projectRunWatchJSON("session", serverapi.RuntimeLiveWatchResponse{
		SessionID: "session",
		Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind:    serverapi.RuntimeLiveWatchNoFinalResult,
			Failure: &serverapi.RuntimeLiveWatchFailure{Reason: "no final"},
		},
	})
	rawNoFinal, marshalErr := json.Marshal(noFinal)
	if err != nil || marshalErr != nil || code != 0 || noFinal.Status != "success" {
		t.Fatalf("no-final projection = %#v, code=%d, err=%v", noFinal, code, err)
	}
	var noFinalJSON map[string]any
	if err := json.Unmarshal(rawNoFinal, &noFinalJSON); err != nil || noFinalJSON["status"] != "success" {
		t.Fatalf("no-final JSON = %s, err=%v", rawNoFinal, err)
	}
	task, code, err := projectTaskObservationJSON("task-id", serverapi.WorkflowTaskObservationResponse{
		TaskID:      "response-task",
		TaskShortID: "T-1",
		Outcomes: []serverapi.WorkflowTaskObservationOutcome{
			{Kind: serverapi.WorkflowTaskObservationInterrupted, Failure: &serverapi.RuntimeLiveWatchFailure{Reason: "stopped"}},
			{Kind: serverapi.WorkflowTaskObservationExecutionError, Failure: &serverapi.RuntimeLiveWatchFailure{Reason: "failed"}},
		},
	})
	if err != nil || code != 1 || task.Status != "error" {
		t.Fatalf("task precedence = %#v, code=%d, err=%v", task, code, err)
	}
}

func TestRunFinalJSONPreservesSessionNameAndDuration(t *testing.T) {
	resultText := "done"
	envelope, code, err := projectRunWatchJSON("session", serverapi.RuntimeLiveWatchResponse{
		SessionID: "session",
		Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchFinalAnswer,
			FinalAnswer: &serverapi.RuntimeLiveWatchFinal{
				Result: &resultText, SessionName: "live", DurationMillis: 2500,
			},
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("projection = %#v, code=%d, err=%v", envelope, code, err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	outcome := generic["outcomes"].([]any)[0].(map[string]any)
	if outcome["session_name"] != "live" || outcome["duration_ms"] != float64(2500) {
		t.Fatalf("final outcome = %#v", outcome)
	}
	wait, code := projectRunWaitJSON("session", app.RunPromptResult{
		Result: "done", SessionName: "waited", Duration: 1500 * time.Millisecond,
	}, nil, context.Background())
	if code != 0 {
		t.Fatalf("wait projection code = %d", code)
	}
	raw, err = json.Marshal(wait)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	outcome = generic["outcomes"].([]any)[0].(map[string]any)
	if outcome["session_name"] != "waited" || outcome["duration_ms"] != float64(1500) {
		t.Fatalf("wait final outcome = %#v", outcome)
	}
}

func TestTaskFailureJSONPreservesTypedDiscriminatorMetadata(t *testing.T) {
	sessionID, scriptPath, nodeKey := "session", "script.sh", "build"
	envelope, _, err := projectTaskObservationJSON("task", serverapi.WorkflowTaskObservationResponse{
		TaskID: "task", TaskShortID: "T-1",
		Outcomes: []serverapi.WorkflowTaskObservationOutcome{{
			Kind:      serverapi.WorkflowTaskObservationExecutionError,
			SessionID: &sessionID, ScriptPath: &scriptPath, NodeKey: &nodeKey,
			Failure: &serverapi.RuntimeLiveWatchFailure{Reason: "failed"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	outcome := generic["outcomes"].([]any)[0].(map[string]any)
	for key, want := range map[string]string{"session_id": sessionID, "script_path": scriptPath, "node_key": nodeKey} {
		if outcome[key] != want {
			t.Fatalf("outcome[%q] = %#v", key, outcome[key])
		}
	}
}

func TestMalformedObservationProjectionReturnsError(t *testing.T) {
	if _, _, err := projectRunWatchJSON("session", serverapi.RuntimeLiveWatchResponse{
		SessionID: "session",
		Outcome:   serverapi.RuntimeLiveWatchOutcome{Kind: serverapi.RuntimeLiveWatchQuestion},
	}); err == nil {
		t.Fatal("Run malformed question unexpectedly projected successfully")
	}
	if _, _, err := projectTaskObservationJSON("task", serverapi.WorkflowTaskObservationResponse{
		TaskID: "task", TaskShortID: "T-1",
		Outcomes: []serverapi.WorkflowTaskObservationOutcome{{
			Kind: serverapi.WorkflowTaskObservationExecutionError,
		}},
	}); err == nil {
		t.Fatal("Task malformed failure unexpectedly projected successfully")
	}
}

func TestRunWaitJSONSeparatesFinalAndCleanupWarnings(t *testing.T) {
	var output bytes.Buffer
	result := app.RunPromptResult{Result: "done", Warnings: []string{"final warning"}}
	if code := emitRunWaitJSON(&output, "session", result, nil, context.Background(), func() error {
		return errors.New("close warning")
	}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var envelope struct {
		Outcomes []struct {
			Warnings []string `json:"warnings"`
		} `json:"outcomes"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Outcomes) != 1 || len(envelope.Outcomes[0].Warnings) != 1 ||
		envelope.Outcomes[0].Warnings[0] != "final warning" ||
		len(envelope.Warnings) != 1 || envelope.Warnings[0] != "close warning" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestProjectObservationErrorCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		exit int
	}{
		{"not found", serverapi.ErrWorkflowTaskNotFound, "target_not_found", 1},
		{"no active", serverapi.ErrRuntimeNoActiveRun, "no_active_execution", 1},
		{"timeout", context.DeadlineExceeded, "timeout", 1},
		{"unavailable", serverapi.ErrStreamUnavailable, "unavailable", 1},
		{"project unavailable", serverapi.ErrProjectUnavailable, "unavailable", 1},
		{"runtime", errors.New("failed"), "runtime", 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, code := projectObservationError(observationOperationTaskWait, nil, context.Background(), test.err)
			if envelope.Error == nil || envelope.Error.Code != test.code || code != test.exit {
				t.Fatalf("projection = %#v, code=%d", envelope, code)
			}
		})
	}
}

type observationFailingWriter struct{}

func (observationFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestObservationJSONWriteFailureIsReported(t *testing.T) {
	if code := emitObservationJSON(observationFailingWriter{}, observationJSONEnvelope{Status: "success"}, 0); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestObservationErrorCancellationPrecedence(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	envelope, exitCode := projectObservationError(observationOperationTaskWait, observationTargetTask("task-id"), canceled, context.Canceled)
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "interrupted" || exitCode != 130 || len(envelope.Outcomes) != 0 {
		t.Fatalf("observer cancellation = %#v, code=%d", envelope, exitCode)
	}
	target, exitCode := projectObservationError(observationOperationRunWait, observationTargetSession("session-id"), context.Background(), context.Canceled)
	if target.Status != "interrupted" || target.Error != nil || len(target.Outcomes) != 1 || exitCode != 130 {
		t.Fatalf("target interruption = %#v, code=%d", target, exitCode)
	}
	stream := errors.Join(serverapi.ErrStreamUnavailable, context.Canceled)
	envelope, exitCode = projectObservationError(observationOperationRunWait, observationTargetSession("session-id"), context.Background(), stream)
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "unavailable" || exitCode != 1 {
		t.Fatalf("stream cancellation = %#v, code=%d", envelope, exitCode)
	}
}

func TestRunWaitHardCutoverAndHeadlessEscape(t *testing.T) {
	previousWait, previousPrompt := runLiveWaitApp, runPromptApp
	defer func() { runLiveWaitApp, runPromptApp = previousWait, previousPrompt }()
	waitCalls, promptCalls := 0, 0
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		waitCalls++
		return app.RunPromptResult{}, nil
	}
	runPromptApp = func(context.Context, app.Options, string, time.Duration, serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		promptCalls++
		return app.RunPromptResult{}, nil
	}
	if code := runSubcommand([]string{"wait", "ordinary", "text"}); code != 2 {
		t.Fatalf("wait text exit = %d", code)
	}
	if waitCalls != 0 || promptCalls != 0 {
		t.Fatalf("calls after malformed Wait = wait:%d prompt:%d", waitCalls, promptCalls)
	}
	sessionID := runtimeids.NewSessionID().String()
	if code := runSubcommand([]string{"wait", sessionID}); code != 0 {
		t.Fatalf("wait command exit = %d", code)
	}
	if waitCalls != 1 {
		t.Fatalf("wait calls = %d, want 1", waitCalls)
	}
	if code := runSubcommand([]string{"--", "wait", "for", "CI"}); code != 0 {
		t.Fatalf("headless escape exit = %d", code)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
}

func TestRunWaitCanonicalValidationPrecedesRemote(t *testing.T) {
	previous := runLiveWaitApp
	defer func() { runLiveWaitApp = previous }()
	calls := 0
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		calls++
		return app.RunPromptResult{}, nil
	}
	if code := runSubcommand([]string{"wait", "legacy-session"}); code != 2 || calls != 0 {
		t.Fatalf("invalid Wait target exit=%d calls=%d", code, calls)
	}
}
