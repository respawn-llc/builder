package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/toolspec"
)

func TestLiveRunWaitIdleReturnsNoActive(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.WaitForActiveRunResult(context.Background()); !errors.Is(err, ErrNoActiveLiveRun) {
		t.Fatalf("WaitForActiveRunResult idle error = %v, want ErrNoActiveLiveRun", err)
	}
}

func TestLiveRunPreparationFreezesFinalResultUntilCompletionTokenCommit(t *testing.T) {
	coordinator := newLiveRunCoordinator()
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	coordinator.beginStep(snapshot)
	handle, err := coordinator.captureWait(context.Background())
	if err != nil {
		t.Fatalf("capture wait: %v", err)
	}
	message := llm.Message{
		Role:    llm.RoleAssistant,
		Content: "frozen final",
		ToolCalls: []llm.ToolCall{{
			ID:    "call-1",
			Name:  "tool",
			Input: json.RawMessage(`{"before":true}`),
		}},
	}
	coordinator.recordAssistantFinalAnswer(snapshot.StepID, message)
	completed := *snapshot
	completed.Status = RunStatusCompleted
	completed.FinishedAt = startedAt.Add(time.Second)
	_, token := coordinator.finishStep(&completed, RunStatusCompleted, nil, false)
	if token == nil {
		t.Fatal("completion predicate did not prepare a token")
	}
	if !coordinator.hasActive() {
		t.Fatal("prepared live-run group cleared before token commit")
	}

	waitDone := make(chan struct {
		result LiveRunResult
		err    error
	}, 1)
	go func() {
		result, waitErr := handle.Wait()
		waitDone <- struct {
			result LiveRunResult
			err    error
		}{result: result, err: waitErr}
	}()
	select {
	case <-waitDone:
		t.Fatal("waiter released before completion token commit")
	default:
	}

	message.Content = "mutated after preparation"
	message.ToolCalls[0].Input[2] = 'X'
	if !coordinator.commitCompletion(token) {
		t.Fatal("completion token was not committed")
	}
	if coordinator.hasActive() {
		t.Fatal("prepared live-run group remained active after token commit")
	}
	if coordinator.commitCompletion(token) {
		t.Fatal("completion token committed more than once")
	}
	var waited struct {
		result LiveRunResult
		err    error
	}
	select {
	case waited = <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not release after completion token commit")
	}
	if waited.err != nil {
		t.Fatalf("wait frozen result: %v", waited.err)
	}
	result := waited.result
	if result.AssistantMessage.Content != "frozen final" {
		t.Fatalf("frozen final content = %q", result.AssistantMessage.Content)
	}
	if got := string(result.AssistantMessage.ToolCalls[0].Input); got != `{"before":true}` {
		t.Fatalf("frozen tool input = %q", got)
	}
	result.AssistantMessage.ToolCalls[0].Input[2] = 'Y'
	again, err := handle.Wait()
	if err != nil {
		t.Fatalf("read frozen result again: %v", err)
	}
	if got := string(again.AssistantMessage.ToolCalls[0].Input); got != `{"before":true}` {
		t.Fatalf("frozen result was aliased through a caller read: %q", got)
	}
}

func TestLiveRunPreparationClassifiesRuntimeFailure(t *testing.T) {
	coordinator := newLiveRunCoordinator()
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	coordinator.beginStep(snapshot)
	handle, err := coordinator.captureWait(context.Background())
	if err != nil {
		t.Fatalf("capture wait: %v", err)
	}
	diagnostic := errors.New("typed runtime failure")
	finished := *snapshot
	finished.Status = RunStatusFailed
	finished.FinishedAt = startedAt.Add(time.Second)
	_, token := coordinator.finishStep(&finished, RunStatusFailed, diagnostic, false)
	if token == nil {
		t.Fatal("failed run did not prepare a completion token")
	}
	if !coordinator.commitCompletion(token) {
		t.Fatal("failed run completion token was not committed")
	}
	result, waitErr := handle.Wait()
	if !errors.Is(waitErr, diagnostic) {
		t.Fatalf("wait error = %v, want runtime diagnostic", waitErr)
	}
	if result.ResultKind != LiveRunResultRuntimeFailure {
		t.Fatalf("result kind = %q, want runtime failure", result.ResultKind)
	}
	if result.FailureDiagnostic == nil {
		t.Fatal("runtime failure did not freeze a typed diagnostic")
	}
	if result.FailureDiagnostic.Code != LiveRunFailureCodeRuntime || result.FailureDiagnostic.Detail != diagnostic.Error() {
		t.Fatalf("failure diagnostic = %+v, want code=%q detail=%q", result.FailureDiagnostic, LiveRunFailureCodeRuntime, diagnostic)
	}
	result.FailureDiagnostic.Detail = "mutated after result read"
	again, err := handle.Wait()
	if !errors.Is(err, diagnostic) {
		t.Fatalf("read frozen failure result again: %v", err)
	}
	if again.FailureDiagnostic == nil || again.FailureDiagnostic.Detail != diagnostic.Error() {
		t.Fatalf("frozen failure diagnostic was aliased through a caller read: %+v", again.FailureDiagnostic)
	}
}

func TestLiveRunPreparationFailureOverridesEarlierFinalAnswer(t *testing.T) {
	coordinator := newLiveRunCoordinator()
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	coordinator.beginStep(snapshot)
	handle, err := coordinator.captureWait(context.Background())
	if err != nil {
		t.Fatalf("capture wait: %v", err)
	}
	coordinator.recordAssistantFinalAnswer(snapshot.StepID, llm.Message{Role: llm.RoleAssistant, Content: "provisional final"})
	diagnostic := errors.New("terminal failure")
	finished := *snapshot
	finished.Status = RunStatusFailed
	finished.FinishedAt = startedAt.Add(time.Second)
	_, token := coordinator.finishStep(&finished, RunStatusFailed, diagnostic, false)
	if token == nil {
		t.Fatal("failed run did not prepare a completion token")
	}
	if !coordinator.commitCompletion(token) {
		t.Fatal("completion token was not committed")
	}
	result, waitErr := handle.Wait()
	if !errors.Is(waitErr, diagnostic) {
		t.Fatalf("wait error = %v, want terminal diagnostic", waitErr)
	}
	if result.ResultKind != LiveRunResultRuntimeFailure {
		t.Fatalf("result kind = %q, want runtime failure", result.ResultKind)
	}
}

func TestLiveRunPreparationInterruptionOverridesEarlierFinalAnswer(t *testing.T) {
	coordinator := newLiveRunCoordinator()
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	coordinator.beginStep(snapshot)
	handle, err := coordinator.captureWait(context.Background())
	if err != nil {
		t.Fatalf("capture wait: %v", err)
	}
	coordinator.recordAssistantFinalAnswer(snapshot.StepID, llm.Message{Role: llm.RoleAssistant, Content: "provisional final"})
	finished := *snapshot
	finished.Status = RunStatusInterrupted
	finished.FinishedAt = startedAt.Add(time.Second)
	_, token := coordinator.finishStep(&finished, RunStatusInterrupted, context.Canceled, false)
	if token == nil {
		t.Fatal("interrupted run did not prepare a completion token")
	}
	if !coordinator.commitCompletion(token) {
		t.Fatal("completion token was not committed")
	}
	result, waitErr := handle.Wait()
	if !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("wait error = %v, want context canceled", waitErr)
	}
	if result.ResultKind != LiveRunResultInterrupted {
		t.Fatalf("result kind = %q, want interruption", result.ResultKind)
	}
}

func TestLiveRunPreparationClassifiesNoFinalOutcomes(t *testing.T) {
	testCases := []struct {
		name       string
		activeKind ActiveKind
		status     RunStatus
		err        error
		wantKind   LiveRunResultKind
		wantReason LiveRunNoFinalAnswerReason
	}{
		{
			name:       "completed task without final answer",
			activeKind: ActiveKindUserTurn,
			status:     RunStatusCompleted,
			wantKind:   LiveRunResultCompletedNoFinal,
			wantReason: LiveRunNoFinalAnswerReasonUnknown,
		},
		{
			name:       "interruption",
			activeKind: ActiveKindUserTurn,
			status:     RunStatusInterrupted,
			err:        context.Canceled,
			wantKind:   LiveRunResultInterrupted,
			wantReason: LiveRunNoFinalAnswerReasonUnknown,
		},
		{
			name:       "successful workflow completion",
			activeKind: ActiveKindWorkflowTurn,
			status:     RunStatusCompleted,
			wantKind:   LiveRunResultWorkflowCompleted,
			wantReason: LiveRunNoFinalAnswerReasonWorkflow,
		},
		{
			name:       "non task activity",
			activeKind: ActiveKindBackground,
			status:     RunStatusCompleted,
			wantKind:   LiveRunResultNonTaskActivity,
			wantReason: LiveRunNoFinalAnswerReasonBackground,
		},
		{
			name:       "goal loop activity",
			activeKind: ActiveKindGoalLoop,
			status:     RunStatusCompleted,
			wantKind:   LiveRunResultNonTaskActivity,
			wantReason: LiveRunNoFinalAnswerReasonGoalLoop,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			coordinator := newLiveRunCoordinator()
			startedAt := time.Now().UTC()
			snapshot := &RunSnapshot{
				RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
				StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
				Status:     RunStatusRunning,
				ActiveKind: testCase.activeKind,
				StartedAt:  startedAt,
			}
			coordinator.beginStep(snapshot)
			handle, err := coordinator.captureWait(context.Background())
			if err != nil {
				t.Fatalf("capture wait: %v", err)
			}
			finished := *snapshot
			finished.Status = testCase.status
			finished.FinishedAt = startedAt.Add(time.Second)
			_, token := coordinator.finishStep(&finished, testCase.status, testCase.err, false)
			if token == nil {
				t.Fatal("no-final outcome did not prepare a completion token")
			}
			if !coordinator.commitCompletion(token) {
				t.Fatal("completion token was not committed")
			}
			result, waitErr := handle.Wait()
			if testCase.err != nil {
				if !errors.Is(waitErr, testCase.err) {
					t.Fatalf("wait error = %v, want %v", waitErr, testCase.err)
				}
			} else if !errors.Is(waitErr, ErrLiveRunNoFinalAnswer) {
				t.Fatalf("wait error = %v, want ErrLiveRunNoFinalAnswer", waitErr)
			}
			if result.ResultKind != testCase.wantKind || result.NoFinalReason != testCase.wantReason {
				t.Fatalf("result = %+v, want kind=%q reason=%q", result, testCase.wantKind, testCase.wantReason)
			}
		})
	}
}

func TestPreparedLiveRunGroupRejectsNewQueueMutationsAndWaitsForExistingAdmission(t *testing.T) {
	coordinator := newLiveRunCoordinator()
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	coordinator.beginStep(snapshot)
	admission, err := coordinator.beginAdmission()
	if err != nil {
		t.Fatalf("begin admission: %v", err)
	}
	completed := *snapshot
	completed.Status = RunStatusCompleted
	completed.FinishedAt = startedAt.Add(time.Second)
	_, token := coordinator.finishStep(&completed, RunStatusCompleted, nil, false)
	if token != nil {
		t.Fatal("completion prepared before an already admitted queue item finished")
	}
	if !coordinator.hasActive() {
		t.Fatal("live-run group cleared before the admitted queue item finished")
	}
	queuedID := runtimeids.NewQueueItemID()
	finished, admissionToken, err := coordinator.finishAdmission(admission, queuedID, nil)
	if err != nil || !finished || admissionToken != nil {
		t.Fatalf("finish existing admission finished=%t token=%v err=%v", finished, admissionToken, err)
	}
	token = coordinator.completeQueueItems(map[runtimeids.QueueItemID]struct{}{queuedID: {}})
	if token == nil {
		t.Fatal("completion did not prepare after the admitted queue item drained")
	}

	_, err = coordinator.beginAdmission()
	assertLiveRunClosingRejection(t, err)
	_, err = coordinator.beginQueueItemPublication(runtimeids.NewQueueItemID(), nil)
	assertLiveRunClosingRejection(t, err)
	if !coordinator.commitCompletion(token) {
		t.Fatal("prepared completion token was not committed")
	}
}

func TestInterruptedLiveRunWaitsForPreexistingAdmissionBeforePreparing(t *testing.T) {
	coordinator := newLiveRunCoordinator()
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	coordinator.beginStep(snapshot)
	handle, err := coordinator.captureWait(context.Background())
	if err != nil {
		t.Fatalf("capture wait: %v", err)
	}
	admission, err := coordinator.beginAdmission()
	if err != nil {
		t.Fatalf("begin admission: %v", err)
	}
	interrupted, _, _, token := coordinator.interrupt()
	if !interrupted {
		t.Fatal("interrupt did not stop the live run")
	}
	if token != nil {
		t.Fatal("interrupt prepared before the admitted queue item resolved")
	}
	waitDone := make(chan struct{}, 1)
	go func() {
		_, _ = handle.Wait()
		waitDone <- struct{}{}
	}()
	select {
	case <-waitDone:
		t.Fatal("waiter released before the admitted queue item resolved")
	default:
	}

	accepted, token, err := coordinator.finishAdmission(admission, runtimeids.NewQueueItemID(), nil)
	if accepted {
		t.Fatal("interrupted live run accepted a pre-existing admission")
	}
	assertLiveRunClosingRejection(t, err)
	if token == nil {
		t.Fatal("resolving the final admission did not prepare a token")
	}
	select {
	case <-waitDone:
		t.Fatal("waiter released before the resulting token commit")
	default:
	}
	if !coordinator.commitCompletion(token) {
		t.Fatal("completion token was not committed")
	}
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not release after token commit")
	}
}

func TestFailedLiveRunWaitsForPreexistingAdmissionBeforePreparing(t *testing.T) {
	coordinator := newLiveRunCoordinator()
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	coordinator.beginStep(snapshot)
	handle, err := coordinator.captureWait(context.Background())
	if err != nil {
		t.Fatalf("capture wait: %v", err)
	}
	admission, err := coordinator.beginAdmission()
	if err != nil {
		t.Fatalf("begin admission: %v", err)
	}
	diagnostic := errors.New("runtime failed while admission was pending")
	finished := *snapshot
	finished.Status = RunStatusFailed
	finished.FinishedAt = startedAt.Add(time.Second)
	_, token := coordinator.finishStep(&finished, RunStatusFailed, diagnostic, false)
	if token != nil {
		t.Fatal("failure prepared before the admitted queue item resolved")
	}
	if interrupted, _, _, token := coordinator.interrupt(); interrupted || token != nil {
		t.Fatalf("stop rewrote failed live run: interrupted=%t token=%v", interrupted, token)
	}
	accepted, token, err := coordinator.finishAdmission(admission, runtimeids.NewQueueItemID(), nil)
	if accepted {
		t.Fatal("failed live run accepted a pre-existing admission")
	}
	assertLiveRunClosingRejection(t, err)
	if token == nil {
		t.Fatal("resolving the final admission did not prepare a token")
	}
	if !coordinator.commitCompletion(token) {
		t.Fatal("completion token was not committed")
	}
	result, waitErr := handle.Wait()
	if !errors.Is(waitErr, diagnostic) {
		t.Fatalf("wait error = %v, want failure diagnostic", waitErr)
	}
	if result.FailureDiagnostic == nil || result.FailureDiagnostic.Code != LiveRunFailureCodeRuntime || result.FailureDiagnostic.Detail != diagnostic.Error() {
		t.Fatalf("failure diagnostic = %+v", result.FailureDiagnostic)
	}
}

func TestQueueUserMessageForActiveRunReturnsClosingRejection(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	eng.liveRun.beginStep(snapshot)
	completed := *snapshot
	completed.Status = RunStatusCompleted
	completed.FinishedAt = startedAt.Add(time.Second)
	_, token := eng.liveRun.finishStep(&completed, RunStatusCompleted, nil, false)
	if token == nil {
		t.Fatal("completion did not prepare a token")
	}
	_, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "too late", liveRunTestRequestID(t), nil)
	if accepted {
		t.Fatal("prepared live run accepted new queued work")
	}
	assertLiveRunClosingRejection(t, err)
	if !eng.liveRun.commitCompletion(token) {
		t.Fatal("completion token was not committed")
	}
}

func assertLiveRunClosingRejection(t *testing.T, err error) {
	t.Helper()
	var rejection *LiveRunGroupClosingError
	if !errors.As(err, &rejection) {
		t.Fatalf("error = %v, want typed closing rejection", err)
	}
}

func TestCapturedActiveRunResultSurvivesFastCompletion(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "fast final", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	var handle *LiveRunWaitHandle
	var captureErr error
	assistant, err := eng.SubmitUserMessageWithHooks(context.Background(), "fast prompt", func() {
		handle, captureErr = eng.CaptureActiveRunResult(context.Background())
	}, nil)
	if err != nil {
		t.Fatalf("SubmitUserMessageWithHooks: %v", err)
	}
	if assistant.Content != "fast final" {
		t.Fatalf("assistant content = %q, want fast final", assistant.Content)
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live wait after fast completion: %v", err)
	}
	if result.AssistantMessage.Content != "fast final" {
		t.Fatalf("captured final = %q, want fast final", result.AssistantMessage.Content)
	}
}

func TestCapturedActiveRunResultReportsWorkAfterTwoSuccessfulLocalToolStarts(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("working",
			llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"true"}`)},
			llm.ToolCall{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"true"}`)},
		),
		finalTextResponse("done"),
	}}
	eng := mustNewFakeToolEngine(t, store, client, Config{Model: "gpt-5"}, toolspec.ToolExecCommand)

	var handle *LiveRunWaitHandle
	var captureErr error
	if _, err := eng.SubmitUserMessageWithHooks(context.Background(), "run two tools", func() {
		handle, captureErr = eng.CaptureActiveRunResult(context.Background())
	}, nil); err != nil {
		t.Fatalf("SubmitUserMessageWithHooks: %v", err)
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live wait: %v", err)
	}
	if !result.WorkPerformed {
		t.Fatal("two accepted local tool starts reported no work performed")
	}
}

func TestCapturedActiveRunResultDoesNotReportWorkAfterOneSuccessfulLocalToolStart(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("working",
			llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"true"}`)},
		),
		finalTextResponse("done"),
	}}
	eng := mustNewFakeToolEngine(t, store, client, Config{Model: "gpt-5"}, toolspec.ToolExecCommand)

	var handle *LiveRunWaitHandle
	var captureErr error
	if _, err := eng.SubmitUserMessageWithHooks(context.Background(), "run one tool", func() {
		handle, captureErr = eng.CaptureActiveRunResult(context.Background())
	}, nil); err != nil {
		t.Fatalf("SubmitUserMessageWithHooks: %v", err)
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live wait: %v", err)
	}
	if result.WorkPerformed {
		t.Fatal("one accepted local tool start reported work performed")
	}
}

func TestCapturedActiveRunResultCountsUnknownToolStartsAsWork(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("trying tools",
			llm.ToolCall{ID: "unknown-1", Name: "definitely_unknown", Input: json.RawMessage(`{}`)},
			llm.ToolCall{ID: "unknown-2", Name: "still_unknown", Input: json.RawMessage(`{}`)},
		),
		finalTextResponse("done"),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	var handle *LiveRunWaitHandle
	var captureErr error
	if _, err := eng.SubmitUserMessageWithHooks(context.Background(), "try unknown tools", func() {
		handle, captureErr = eng.CaptureActiveRunResult(context.Background())
	}, nil); err != nil {
		t.Fatalf("SubmitUserMessageWithHooks: %v", err)
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live wait: %v", err)
	}
	if !result.WorkPerformed {
		t.Fatal("two accepted unknown-tool starts reported no work performed")
	}
}

func TestCapturedActiveRunResultCountsStartsForToolsThatLaterFail(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("trying tools",
			llm.ToolCall{ID: "failing-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"false"}`)},
			llm.ToolCall{ID: "failing-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"false"}`)},
		),
		finalTextResponse("done"),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: toolErrorHandler{},
	}), Config{Model: "gpt-5"})

	var handle *LiveRunWaitHandle
	var captureErr error
	if _, err := eng.SubmitUserMessageWithHooks(context.Background(), "run failing tools", func() {
		handle, captureErr = eng.CaptureActiveRunResult(context.Background())
	}, nil); err == nil {
		t.Fatal("SubmitUserMessageWithHooks unexpectedly hid tool execution errors")
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, waitErr := handle.Wait()
	if waitErr == nil {
		t.Fatal("captured live wait unexpectedly hid tool execution errors")
	}
	if !result.WorkPerformed {
		t.Fatal("two accepted starts for tools that failed reported no work performed")
	}
}

type toolErrorHandler struct{}

func (toolErrorHandler) Call(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{}, errors.New("tool execution failed")
}

func TestCapturedActiveRunResultCountsHostedToolStartsAsWork(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"hosted-1","status":"completed","action":{"type":"search","query":"one"}}`),
				},
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"hosted-2","status":"completed","action":{"type":"search","query":"two"}}`),
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		finalTextResponse("done"),
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
	})

	var handle *LiveRunWaitHandle
	var captureErr error
	if _, err := eng.SubmitUserMessageWithHooks(context.Background(), "search twice", func() {
		handle, captureErr = eng.CaptureActiveRunResult(context.Background())
	}, nil); err != nil {
		t.Fatalf("SubmitUserMessageWithHooks: %v", err)
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live wait: %v", err)
	}
	if !result.WorkPerformed {
		t.Fatal("two accepted hosted tool starts reported no work performed")
	}
}

func TestCapturedActiveRunResultDoesNotCombineSingleToolStartsAcrossSteps(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	eng.pauseQueuedUserAutoDrain()
	t.Cleanup(eng.resumeQueuedUserAutoDrain)

	var handle *LiveRunWaitHandle
	var queued QueuedUserMessage
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		var err error
		handle, err = eng.CaptureActiveRunResult(context.Background())
		if err != nil {
			return err
		}
		var accepted bool
		queued, accepted, err = eng.QueueUserMessageForActiveRun(context.Background(), "successor", liveRunTestRequestID(t), nil)
		if err != nil {
			return err
		}
		if !accepted {
			return errors.New("successor was not accepted into the live-run group")
		}
		return eng.publishLiveExecutionToolStart(stepID, llm.ToolCall{ID: "step-1-call", Name: string(toolspec.ToolExecCommand)}, nil)
	}); err != nil {
		t.Fatalf("first step: %v", err)
	}
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		return eng.publishLiveExecutionToolStart(stepID, llm.ToolCall{ID: "step-2-call", Name: string(toolspec.ToolExecCommand)}, nil)
	}); err != nil {
		t.Fatalf("second step: %v", err)
	}
	eng.completeLiveRunQueueItems(map[string]struct{}{queued.ID: {}})

	result, err := handle.Wait()
	if !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("captured live wait error = %v, want ErrLiveRunNoFinalAnswer", err)
	}
	if result.WorkPerformed {
		t.Fatal("single accepted starts in separate steps were combined into work performed")
	}
}

func TestCapturedActiveRunResultPreservesQualifyingWorkAcrossSuccessorSteps(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	eng.pauseQueuedUserAutoDrain()
	t.Cleanup(eng.resumeQueuedUserAutoDrain)

	var handle *LiveRunWaitHandle
	var queued QueuedUserMessage
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		var err error
		handle, err = eng.CaptureActiveRunResult(context.Background())
		if err != nil {
			return err
		}
		var accepted bool
		queued, accepted, err = eng.QueueUserMessageForActiveRun(context.Background(), "successor", liveRunTestRequestID(t), nil)
		if err != nil {
			return err
		}
		if !accepted {
			return errors.New("successor was not accepted into the live-run group")
		}
		if err := eng.publishLiveExecutionToolStart(stepID, llm.ToolCall{ID: "step-1-call-1", Name: string(toolspec.ToolExecCommand)}, nil); err != nil {
			return err
		}
		return eng.publishLiveExecutionToolStart(stepID, llm.ToolCall{ID: "step-1-call-2", Name: string(toolspec.ToolExecCommand)}, nil)
	}); err != nil {
		t.Fatalf("first step: %v", err)
	}
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
		return nil
	}); err != nil {
		t.Fatalf("second step: %v", err)
	}
	eng.completeLiveRunQueueItems(map[string]struct{}{queued.ID: {}})

	result, err := handle.Wait()
	if !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("captured live wait error = %v, want ErrLiveRunNoFinalAnswer", err)
	}
	if !result.WorkPerformed {
		t.Fatal("qualifying work from an earlier step was lost after a successor step")
	}
}

func TestRecoveryToolStartsRestoreProjectionWithoutCountingWork(t *testing.T) {
	store := mustCreateTestSession(t)
	var recoveredStarts []string
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.Kind == EventToolCallStarted && event.ToolCall != nil {
				recoveredStarts = append(recoveredStarts, event.ToolCall.ID)
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle

	var handle *LiveRunWaitHandle
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		var err error
		handle, err = eng.CaptureActiveRunResult(context.Background())
		if err != nil {
			return err
		}
		if err := eng.steer(stepID, steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{ID: "recovered-local", Name: string(toolspec.ToolExecCommand)},
					{ID: "recovered-hosted", Name: string(toolspec.ToolWebSearch)},
				},
			}},
		)); err != nil {
			return err
		}
		return eng.seedTranscriptLiveToolsFromDanglingToolCalls(stepID)
	}); err != nil {
		t.Fatalf("recovery projection step: %v", err)
	}

	result, err := handle.Wait()
	if !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("captured live wait error = %v, want ErrLiveRunNoFinalAnswer", err)
	}
	if result.WorkPerformed {
		t.Fatal("reconstructed tool starts counted as live work")
	}
	if len(recoveredStarts) != 2 {
		t.Fatalf("recovered start events = %v, want both local and hosted starts", recoveredStarts)
	}
	if liveTools := eng.transcriptRuntimeState().AbortLiveTools(); len(liveTools) != 2 {
		t.Fatalf("recovered live-tool projection = %+v, want two restored tools", liveTools)
	}
}

func TestTerminalWorkflowQueueFailureCompletesTaggedLiveItems(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.pauseQueuedUserAutoDrain()
	t.Cleanup(eng.resumeQueuedUserAutoDrain)
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindWorkflowTurn,
		StartedAt:  startedAt,
	}
	eng.liveRun.beginStep(snapshot)
	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer after workflow", liveRunTestRequestID(t), nil)
	if err != nil || !accepted || item.ID == "" {
		t.Fatalf("QueueUserMessageForActiveRun item=%+v accepted=%t err=%v", item, accepted, err)
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	completed := *snapshot
	completed.Status = RunStatusCompleted
	completed.FinishedAt = startedAt.Add(time.Second)
	eng.liveRun.finishStep(&completed, RunStatusCompleted, nil, false)
	eng.mu.Lock()
	eng.workflowTerminal = WorkflowTerminalState{
		Completed:   true,
		RunID:       "workflow-run",
		Generation:  1,
		Source:      WorkflowCompletionSourceTool,
		CompletedAt: time.Now().UTC(),
	}
	eng.mu.Unlock()

	if !eng.failQueuedUserWorkIfTerminal() {
		t.Fatal("terminal workflow did not fail queued user work")
	}
	if _, err := handle.Wait(); !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("live wait error = %v, want no-final after terminal queue failure", err)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group stayed active after terminal queue failure")
	}
}

func TestTryInterruptActiveRunNoopsAfterStepLeavesActiveState(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.liveRun.beginStep(&RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  time.Now().UTC(),
	})

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil {
		t.Fatalf("TryInterruptActiveRun: %v", err)
	}
	if stopped {
		t.Fatal("stop reported stopped after active step was already gone")
	}
}

func TestTryInterruptActiveRunCancelsCompactionStep(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepCtxSeen := make(chan context.Context, 1)
	done := make(chan error, 1)
	eng.ensureOrchestrationCollaborators()
	go func() {
		done <- eng.stepLifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindCompaction}, func(ctx context.Context, stepID string) error {
			stepCtxSeen <- ctx
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	var stepCtx context.Context
	select {
	case stepCtx = <-stepCtxSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for compaction step")
	}

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil {
		t.Fatalf("TryInterruptActiveRun: %v", err)
	}
	if !stopped {
		t.Fatal("compaction live stop reported idle")
	}
	select {
	case <-stepCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("compaction step context was not canceled")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("compaction step error = %v, want context canceled", err)
	}
}

func TestTryInterruptActiveRunDoesNotCancelMaintenanceWhileDroppingTaggedItems(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  startedAt,
	}
	eng.liveRun.beginStep(snapshot)
	eng.ensureOrchestrationCollaborators()
	queueItemID := runtimeids.NewQueueItemID()
	eng.messageFlow.QueueUserMessageWithID(QueuedUserMessage{ID: queueItemID.String(), Text: "steer pending", ClientRequestID: liveRunTestRequestID(t).String()})
	eng.liveRun.mu.Lock()
	eng.liveRun.current.trackQueuedItemForLiveRun(queueItemID)
	delete(eng.liveRun.current.publishingItems, queueItemID)
	eng.liveRun.mu.Unlock()
	completed := *snapshot
	completed.Status = RunStatusCompleted
	completed.FinishedAt = startedAt.Add(time.Second)
	eng.liveRun.finishStep(&completed, RunStatusCompleted, nil, false)

	stepCtxSeen := make(chan context.Context, 1)
	releaseMaintenance := make(chan struct{})
	maintenanceDone := make(chan error, 1)
	go func() {
		maintenanceDone <- eng.stepLifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindRuntimeMaintenance}, func(ctx context.Context, stepID string) error {
			stepCtxSeen <- ctx
			<-releaseMaintenance
			return nil
		})
	}()
	var stepCtx context.Context
	select {
	case stepCtx = <-stepCtxSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for maintenance step")
	}

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil {
		t.Fatalf("TryInterruptActiveRun: %v", err)
	}
	if !stopped {
		t.Fatal("live stop with pending tagged items reported idle")
	}
	select {
	case <-stepCtx.Done():
		t.Fatal("live stop canceled maintenance step")
	default:
	}
	close(releaseMaintenance)
	if err := <-maintenanceDone; err != nil {
		t.Fatalf("maintenance step: %v", err)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group stayed active after dropping tagged item")
	}
}

func TestEmitRunStateStepOpensActiveLiveRunGroup(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live run step")
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("EmitRunState=true step did not open active live-run group")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("live run step: %v", err)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group stayed active after idle completion")
	}
}

func TestMaintenanceStepDoesNotOpenActiveLiveRunGroup(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: false, ActiveKind: ActiveKindRuntimeMaintenance}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for maintenance step")
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("EmitRunState=false step opened active live-run group")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("maintenance step: %v", err)
	}
}

func TestQueueUserMessageForActiveRunRejectsIdleWithoutBeforeQueue(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	called := false

	_, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", liveRunTestRequestID(t), func() error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNoActiveLiveRun) || accepted {
		t.Fatalf("idle active-run queue accepted=%t err=%v, want no-active rejection", accepted, err)
	}
	if called {
		t.Fatal("beforeQueue was called for idle active-run queue")
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("idle active-run queue mutated queued user work")
	}
}

func TestQueueUserMessageForActiveRunAdmissionKeepsGroupOpenAcrossStepFinish(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.pauseQueuedUserAutoDrain()
	t.Cleanup(func() {
		if err := eng.Close(); err != nil {
			t.Errorf("close engine: %v", err)
		}
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	stepStarted := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(stepStarted)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-stepStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}

	beforeStarted := make(chan struct{})
	releaseBefore := make(chan struct{})
	queueDone := make(chan error, 1)
	go func() {
		item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", liveRunTestRequestID(t), func() error {
			close(beforeStarted)
			<-releaseBefore
			return nil
		})
		if err == nil && (!accepted || item.ID == "" || item.Text != "steer") {
			err = errors.New("unexpected accepted queue item")
		}
		queueDone <- err
	}()
	select {
	case <-beforeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for beforeQueue")
	}

	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("step: %v", err)
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group closed while admitted steering was blocked in beforeQueue")
	}
	close(releaseBefore)
	if err := <-queueDone; err != nil {
		t.Fatalf("active-run queue: %v", err)
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group closed before tagged queued steering could drain")
	}
}

func TestQueueUserMessageForActiveRunRollsBackBeforeQueueError(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for step")
	}

	beforeErr := errors.New("history failed")
	if _, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", liveRunTestRequestID(t), func() error { return beforeErr }); !errors.Is(err, beforeErr) || accepted {
		t.Fatalf("beforeQueue error accepted=%t err=%v, want rollback error", accepted, err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("rollback left queued user work")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("step: %v", err)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("rollback kept live-run group open after step completion")
	}
}

func TestQueueUserMessageForActiveRunStopCancelsBlockedAdmissionBeforeQueueMutation(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle

	started := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}

	beforeStarted := make(chan struct{})
	releaseBefore := make(chan struct{})
	queueDone := make(chan struct {
		item     QueuedUserMessage
		accepted bool
		err      error
	}, 1)
	go func() {
		item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "must not queue", liveRunTestRequestID(t), func() error {
			close(beforeStarted)
			<-releaseBefore
			return nil
		})
		queueDone <- struct {
			item     QueuedUserMessage
			accepted bool
			err      error
		}{item: item, accepted: accepted, err: err}
	}()
	select {
	case <-beforeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for beforeQueue")
	}
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("stopped active step: %v", err)
	}

	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	replacementDone := make(chan error, 1)
	go func() {
		replacementDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(replacementStarted)
			<-releaseReplacement
			return nil
		})
	}()
	select {
	case <-replacementStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for replacement active step")
	}
	replacementHandle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("capture replacement live run: %v", err)
	}

	close(releaseBefore)
	queued := <-queueDone
	if queued.accepted || queued.item.ID != "" {
		t.Fatalf("blocked admission result = item=%+v accepted=%t err=%v, want rejection without queue metadata", queued.item, queued.accepted, queued.err)
	}
	assertLiveRunClosingRejection(t, queued.err)
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped blocked admission queued stale work into replacement run")
	}
	close(releaseReplacement)
	if err := <-replacementDone; err != nil {
		t.Fatalf("replacement active step: %v", err)
	}
	if _, err := replacementHandle.Wait(); !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("replacement live run result error = %v, want ErrLiveRunNoFinalAnswer", err)
	}
	waitEngineLifecycleTasks(t, eng)
	if got := client.callCount(); got != 0 {
		t.Fatalf("stopped blocked admission executed %d model calls", got)
	}
}

func TestWaitForActiveRunResultReturnsAssistantFinalAnswer(t *testing.T) {
	store := mustCreateTestSession(t)
	modelEntered := make(chan struct{})
	releaseModel := make(chan struct{})
	client := &hookClient{
		response: llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "final answer", Phase: llm.MessagePhaseFinal}},
		beforeReturn: func() error {
			close(modelEntered)
			<-releaseModel
			return nil
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "hello")
		submitDone <- err
	}()

	select {
	case <-modelEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for model")
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	close(releaseModel)
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	waited, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live run result: %v", err)
	}
	if waited.ResultKind != LiveRunResultAssistantFinalAnswer || waited.AssistantMessage.Content != "final answer" {
		t.Fatalf("wait result = %+v", waited)
	}
}

func TestCapturedActiveRunResultCompletesLateTaggedQueuedDrain(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "initial work handled", Phase: llm.MessagePhaseFinal}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "queued work handled", Phase: llm.MessagePhaseFinal}},
	}}
	transitions := make(chan StepLifecycleTransition)
	releaseTransition := make(chan struct{})
	sink := &callbackStepLifecycleSink{onTransition: func(transition StepLifecycleTransition) error {
		transitions <- transition
		<-releaseTransition
		return nil
	}}
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5", StepLifecycle: sink})
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "hello")
		submitDone <- err
	}()
	if got := <-transitions; got != StepLifecycleTransitionBegan {
		t.Fatalf("first transition = %q, want began", got)
	}
	releaseTransition <- struct{}{}
	if got := <-transitions; got != StepLifecycleTransitionEnded {
		t.Fatalf("second transition = %q, want ended", got)
	}
	queued := eng.QueueUserMessageForAutoDrain("steer into drain", "initial-drain")
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelWait()
	handle, err := eng.CaptureActiveRunResult(waitCtx)
	if queued.ID == "" || err != nil {
		t.Fatalf("queued=%+v capture error=%v", queued, err)
	}
	releaseTransition <- struct{}{}
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if got := <-transitions; got != StepLifecycleTransitionBegan {
		t.Fatalf("queued transition = %q, want began", got)
	}
	late, accepted, queueErr := eng.QueueUserMessageForActiveRun(context.Background(), "steer admitted after drain snapshot", liveRunTestRequestID(t), nil)
	if late.ID == "" || !accepted || queueErr != nil {
		t.Fatalf("late queued=%+v accepted=%t queue error=%v", late, accepted, queueErr)
	}
	releaseTransition <- struct{}{}
	if got := <-transitions; got != StepLifecycleTransitionEnded {
		t.Fatalf("final transition = %q, want ended", got)
	}
	releaseTransition <- struct{}{}
	waited, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live run result: %v", err)
	}
	if waited.AssistantMessage.Content != "queued work handled" {
		t.Fatalf("wait result = %+v, want queued work handled", waited)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group remained active after draining late tagged steering")
	}
}

func TestWaitForActiveRunResultReturnsNoFinalAnswerForShellRun(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserShell}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shell run")
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := handle.Wait()
		waitDone <- err
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("shell run: %v", err)
	}
	if err := <-waitDone; !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("WaitForActiveRunResult shell error = %v, want ErrLiveRunNoFinalAnswer", err)
	}
}

func TestTryInterruptActiveRunCancelsActiveStepAndWaiters(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := handle.Wait()
		waitDone <- err
	}()
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active step error = %v, want context canceled", err)
	}
	if err := <-waitDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context canceled", err)
	}
}

func TestTryInterruptActiveRunFailsAcceptedSteeringWhileStepRuns(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}

	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "do not run", liveRunTestRequestID(t), nil)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active step error = %v, want context canceled", err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped accepted steering remained queued")
	}
	assertStoppedQueuedStatus(t, statuses, item.ID)
}

func TestTryInterruptActiveRunFailsAcceptedSteeringInTaggedQueueGap(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	eng.pauseQueuedUserAutoDrain()

	started := make(chan struct{})
	releaseStep := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}
	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "do not drain", liveRunTestRequestID(t), nil)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}
	close(releaseStep)
	if err := <-done; err != nil {
		t.Fatalf("active step: %v", err)
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group closed before stop in tagged queue gap")
	}
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want tagged-gap stop", stopped, err)
	}
	eng.resumeQueuedUserAutoDrain()
	waitEngineLifecycleTasks(t, eng)
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped tagged-gap steering remained queued")
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("stopped tagged-gap steering executed %d model calls", got)
	}
	assertStoppedQueuedStatus(t, statuses, item.ID)
}

func TestTryInterruptActiveRunDefersPublishingQueueItemFailureUntilAcceptedStatus(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle

	started := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}

	item := QueuedUserMessage{ID: runtimeids.NewQueueItemID().String(), Text: "race-safe steer", ClientRequestID: "req-publishing"}
	tagged, publicationErr := eng.liveRun.beginQueueItemPublication(mustQueueItemID(item.ID), func(queueItemID string) {
		eng.markQueuedUserInjectionForAutoDrain(queueItemID)
	})
	if publicationErr != nil || !tagged || item.ID == "" {
		t.Fatalf("publishing queue setup tagged=%t item=%+v err=%v", tagged, item, publicationErr)
	}
	item = eng.messageFlow.QueueUserMessageWithID(item)
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	if !eng.HasQueuedUserWork() || !eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("publishing item was failed before acceptance was emitted")
	}

	eng.emitQueuedUserMessageStatus(item, QueuedUserMessageAccepted, "", false)
	queueItemID := mustQueueItemID(item.ID)
	if !eng.liveRun.finishQueueItemPublication(queueItemID) {
		t.Fatal("publishing item was not marked stopped after concurrent stop")
	}
	eng.failStoppedLiveRunQueueItems(map[runtimeids.QueueItemID]struct{}{queueItemID: {}})
	if eng.HasQueuedUserWork() || eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("stopped publishing item left queued work or stale auto-drain id")
	}
	assertQueuedStatusOrder(t, statuses, item.ID, []QueuedUserMessageStatus{QueuedUserMessageAccepted, QueuedUserMessageFailed})
	assertStoppedQueuedStatus(t, statuses, item.ID)
	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("active step: %v", err)
	}
}

func TestDroppedStoppedLiveRunQueueItemsClearAutoDrainState(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	item := QueuedUserMessage{ID: runtimeids.NewQueueItemID().String(), Text: "stopped drained", ClientRequestID: "req-stopped"}
	queueItemID := mustQueueItemID(item.ID)
	eng.markQueuedUserInjectionForAutoDrain(item.ID)
	eng.liveRun.mu.Lock()
	eng.liveRun.markStoppedQueueItemsLocked(map[runtimeids.QueueItemID]struct{}{queueItemID: {}})
	eng.liveRun.mu.Unlock()

	remaining := eng.dropStoppedLiveRunQueueItems([]queuedUserSteeringIntent{{message: item}})
	if len(remaining) != 0 {
		t.Fatalf("remaining stopped drained items = %+v, want none", remaining)
	}
	if eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("stopped drained item left stale auto-drain id")
	}
	assertStoppedQueuedStatus(t, statuses, item.ID)

	idle := eng.QueueUserMessage("idle after stopped drain")
	if eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("later idle queue was marked for auto-drain by stale stopped state")
	}
	if !eng.DiscardQueuedUserMessage(idle.ID) {
		t.Fatal("later idle queue was not left pending")
	}
}

func TestTryInterruptActiveRunIdleNoops(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || stopped {
		t.Fatalf("TryInterruptActiveRun idle stopped=%t err=%v, want no-op", stopped, err)
	}
}

func assertStoppedQueuedStatus(t *testing.T, statuses []QueuedUserMessageStatusEvent, queueItemID string) {
	t.Helper()
	for _, status := range statuses {
		if status.QueueItemID == queueItemID && status.Status == QueuedUserMessageFailed && status.FailureReason == QueuedUserMessageFailureStopped {
			return
		}
	}
	t.Fatalf("missing stopped failure for queue item %q in statuses %+v", queueItemID, statuses)
}

func assertQueuedStatusOrder(t *testing.T, statuses []QueuedUserMessageStatusEvent, queueItemID string, want []QueuedUserMessageStatus) {
	t.Helper()
	got := make([]QueuedUserMessageStatus, 0, len(statuses))
	for _, status := range statuses {
		if status.QueueItemID == queueItemID {
			got = append(got, status.Status)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("status order for %q = %+v, want %+v", queueItemID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status order for %q = %+v, want %+v", queueItemID, got, want)
		}
	}
}

func liveRunTestRequestID(t *testing.T) runtimeids.RuntimeClientRequestID {
	t.Helper()
	id, err := runtimeids.ParseRuntimeClientRequestID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("parse live-run test request id: %v", err)
	}
	return id
}
