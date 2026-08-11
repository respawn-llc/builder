package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestCurrentNodeControllerTaskInterruptLeavesWaitingQuestionScopeNonQuiescent(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	fixture := newCurrentNodeQuestionFixture(t)
	question := currentNodeReferenceForControllerTest(t, "task-running-and-question", "node-question")
	running := currentNodeReferenceForControllerTest(t, "task-running-and-question", "node-running")
	request := askquestion.AskQuestionRequest{
		ID:       "ask-running-and-question",
		StepID:   uuid.NewString(),
		Question: "Keep waiting?",
	}
	pending, runningHandle := fixture.startPendingPromptWithScript(
		t,
		question,
		request,
		running,
		sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
	)
	fixture.waitForPendingPrompt(t, question.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})

	waitForRunningCurrentNode(t, fixture.authority, running)

	if err := fixture.controller.Interrupt(context.Background(), InterruptSelector{TaskID: running.TaskID}); err != nil {
		t.Fatalf("Task Interrupt: %v", err)
	}
	if _, live := fixture.authority.ExecutionByScope(runningHandle.Scope().ID()); live {
		t.Fatal("Task Interrupt left the actively executing Script live")
	}
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); !live {
		t.Fatal("Task Interrupt stopped the non-interruptible waiting Question")
	}
	if err := fixture.controller.EnsureTaskQuiescent(running.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("quiescence with waiting Question = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	if _, interrupted := fixture.store.interruption(question); interrupted {
		t.Fatal("Task Interrupt persisted interruption for the waiting Question")
	}
	if _, interrupted := fixture.store.interruption(running); !interrupted {
		t.Fatal("Task Interrupt did not persist interruption for the running Script")
	}
}

func TestCurrentNodeControllerManualMoveRejectsWaitingQuestionWithoutStoppingSibling(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	fixture := newCurrentNodeQuestionFixture(t)
	question := currentNodeReferenceForControllerTest(t, "task-manual-move-question", "node-question")
	running := currentNodeReferenceForControllerTest(t, "task-manual-move-question", "node-running")
	request := askquestion.AskQuestionRequest{
		ID:       "ask-manual-move-question",
		StepID:   uuid.NewString(),
		Question: "Keep waiting?",
	}
	pending, runningHandle := fixture.startPendingPromptWithScript(
		t,
		question,
		request,
		running,
		sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
	)
	fixture.waitForPendingPrompt(t, question.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})
	waitForRunningCurrentNode(t, fixture.authority, running)

	if err := fixture.controller.InterruptForManualMove(context.Background(), running.TaskID, nil); !errors.Is(err, sessionruntime.ErrWorkflowQuestionPending) {
		t.Fatalf("InterruptForManualMove error = %v, want pending-question blocker", err)
	}
	if _, live := fixture.authority.ExecutionByScope(runningHandle.Scope().ID()); !live {
		t.Fatal("manual move interruption stopped the running sibling")
	}
	if _, interrupted := fixture.store.interruption(running); interrupted {
		t.Fatal("manual move interruption persisted a sibling interruption")
	}
	runningHandle.RequestStop()
	_, _ = runningHandle.Wait(context.Background())
}

func TestCurrentNodeControllerManualMoveDispositionClassifiesLifecycle(t *testing.T) {
	t.Run("quiescent", func(t *testing.T) {
		fixture := newCurrentNodeQuestionFixture(t)
		disposition, err := fixture.controller.ManualMoveDisposition("task-disposition-quiescent")
		if err != nil {
			t.Fatalf("ManualMoveDisposition: %v", err)
		}
		if disposition != ManualMoveDispositionQuiescent {
			t.Fatalf("disposition = %q, want quiescent", disposition)
		}
	})

	t.Run("waiting question", func(t *testing.T) {
		fixture := newCurrentNodeQuestionFixture(t)
		reference := currentNodeReferenceForControllerTest(t, "task-disposition-question", "node-question")
		pending := fixture.startPendingPrompt(t, reference, askquestion.AskQuestionRequest{
			ID:       "ask-disposition-question",
			StepID:   uuid.NewString(),
			Question: "Wait?",
		})
		fixture.waitForPendingPrompt(t, reference.TaskID, "ask-disposition-question")
		t.Cleanup(func() {
			pending.handle.RequestStop()
			_, _ = pending.handle.Wait(context.Background())
		})
		disposition, err := fixture.controller.ManualMoveDisposition(reference.TaskID)
		if err != nil {
			t.Fatalf("ManualMoveDisposition: %v", err)
		}
		if disposition != ManualMoveDispositionWaitingQuestion {
			t.Fatalf("disposition = %q, want waiting_question", disposition)
		}
	})

	t.Run("pending session approval", func(t *testing.T) {
		fixture := newCurrentNodeQuestionFixture(t)
		reference := currentNodeReferenceForControllerTest(t, "task-disposition-approval", "node-approval")
		pending := fixture.startPendingPrompt(t, reference, askquestion.AskQuestionRequest{
			ID:       "ask-disposition-approval",
			StepID:   uuid.NewString(),
			Question: "Approve?",
			Approval: true,
		})
		fixture.waitForPendingPrompt(t, reference.TaskID, "ask-disposition-approval")
		t.Cleanup(func() {
			pending.handle.RequestStop()
			_, _ = pending.handle.Wait(context.Background())
		})
		disposition, err := fixture.controller.ManualMoveDisposition(reference.TaskID)
		if err != nil {
			t.Fatalf("ManualMoveDisposition: %v", err)
		}
		if disposition != ManualMoveDispositionLifecycleConflict {
			t.Fatalf("disposition = %q, want lifecycle_conflict", disposition)
		}
	})
}

func TestCurrentNodeControllerAnswersOnlyDurablyBoundExactPromptScope(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-question", "node-question")
	request := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)

	if err := fixture.answerWorkflowQuestion(
		context.Background(),
		reference.TaskID,
		"different-ask-id",
		currentNodeQuestionAnswer("yes"),
		nil,
	); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("unknown prompt answer error = %v, want prompt not found", err)
	}
	if err := fixture.answerWorkflowQuestion(
		context.Background(),
		reference.TaskID,
		request.ID,
		currentNodeQuestionAnswer("yes"),
		nil,
	); err != nil {
		t.Fatalf("AnswerWorkflowQuestion: %v", err)
	}
	select {
	case result := <-pending.result:
		if result.err != nil || result.resolution == nil {
			t.Fatalf("prompt result = %+v, want exact successful answer", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for prompt response")
	}
	if _, err := pending.handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait prompt execution: %v", err)
	}
	if calls := fixture.store.bindingCalls(); len(calls) != 1 || calls[0].sessionID != pending.sessionID || !calls[0].reference.Equal(reference) {
		t.Fatalf("binding validation calls = %+v, want exact session/current-node binding", calls)
	}
}

func TestCurrentNodeControllerReleasesMutationPermitAfterAcceptingAnswer(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-question-permit", "node-question")
	firstID := uuid.NewString()
	secondID := uuid.NewString()
	runID := uuid.NewString()
	stepID := uuid.NewString()
	first := askquestion.AskQuestionRequest{
		ID:       firstID,
		StepID:   stepID,
		RunID:    runID,
		Origin:   askquestion.AskQuestionOriginModelTool,
		Question: "First?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               runID,
			StepID:              stepID,
			PromptID:            firstID,
			BatchPromptIDs:      []string{firstID, secondID},
			CandidateOrdinal:    0,
			PreparedPromptCount: 2,
		},
	}
	second := first
	second.ID = secondID
	second.Question = "Second?"
	second.QuestionBatch = &askquestion.AskQuestionBatchMetadata{
		Origin:              first.QuestionBatch.Origin,
		RunID:               runID,
		StepID:              stepID,
		PromptID:            secondID,
		BatchPromptIDs:      []string{firstID, secondID},
		CandidateOrdinal:    1,
		PreparedPromptCount: 2,
	}
	firstResult := make(chan currentNodePromptResult, 1)
	secondResult := make(chan currentNodePromptResult, 1)
	allowSuccessor := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case allowSuccessor <- struct{}{}:
		default:
		}
	})
	handle, sessionID := fixture.startQuestionExecution(t, reference, func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
		resolution, err := fixture.authority.AwaitPromptResolution(ctx, scope.ID(), first)
		firstResult <- currentNodePromptResult{resolution: resolution, err: err}
		if err != nil {
			return err
		}
		<-allowSuccessor
		resolution, err = fixture.authority.AwaitPromptResolution(ctx, scope.ID(), second)
		secondResult <- currentNodePromptResult{resolution: resolution, err: err}
		return err
	})
	fixture.waitForPendingPrompt(t, reference.TaskID, firstID)
	independentReference := currentNodeReferenceForControllerTest(t, "task-question-independent", "node-question")
	independentRequest := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Independent?",
	}
	independent := fixture.startPendingPrompt(t, independentReference, independentRequest)
	fixture.waitForPendingPrompt(t, independentReference.TaskID, independentRequest.ID)

	answerDone := make(chan error, 1)
	go func() {
		answerDone <- fixture.answerWorkflowQuestion(
			context.Background(),
			reference.TaskID,
			firstID,
			currentNodeQuestionAnswer("one"),
			nil,
		)
	}()
	select {
	case result := <-firstResult:
		if result.err != nil || result.resolution == nil {
			t.Fatalf("first prompt result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for accepted first answer")
	}

	independentDone := make(chan error, 1)
	go func() {
		independentDone <- fixture.answerWorkflowQuestion(
			context.Background(),
			independentReference.TaskID,
			independentRequest.ID,
			currentNodeQuestionAnswer("independent"),
			nil,
		)
	}()
	select {
	case err := <-independentDone:
		if err != nil {
			t.Fatalf("independent AnswerWorkflowQuestion: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accepted answer blocked an independent workflow question while waiting for its successor")
	}
	select {
	case result := <-independent.result:
		if result.err != nil || result.resolution == nil {
			t.Fatalf("independent prompt result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for independent prompt response")
	}
	if _, err := independent.handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait independent prompt execution: %v", err)
	}
	select {
	case err := <-answerDone:
		t.Fatalf("answer returned before successor became pending: %v", err)
	default:
	}

	allowSuccessor <- struct{}{}
	fixture.waitForPendingPrompt(t, reference.TaskID, secondID)
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("AnswerWorkflowQuestion: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer did not return after successor became pending")
	}
	if err := fixture.authority.SubmitPromptResolution(
		sessionID,
		secondID,
		currentNodeQuestionAnswer("two"),
		nil,
	); err != nil {
		t.Fatalf("submit second prompt: %v", err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil || result.resolution == nil {
			t.Fatalf("second prompt result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second prompt response")
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait prompt execution: %v", err)
	}
}

func TestCurrentNodeControllerMalformedPreparedBatchResolvesAwaiterWithInvariantError(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-question-invalid-batch", "node-question")
	request := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		RunID:    uuid.NewString(),
		Origin:   askquestion.AskQuestionOriginModelTool,
		Question: "Proceed?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               uuid.NewString(),
			StepID:              uuid.NewString(),
			PromptID:            uuid.NewString(),
			BatchPromptIDs:      []string{uuid.NewString()},
			CandidateOrdinal:    0,
			PreparedPromptCount: 1,
		},
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)

	err := fixture.answerWorkflowQuestion(
		context.Background(),
		reference.TaskID,
		request.ID,
		currentNodeQuestionAnswer("yes"),
		nil,
	)
	var invariantErr sessionruntime.PromptBatchInvariantError
	if !errors.As(err, &invariantErr) {
		t.Fatalf("AnswerWorkflowQuestion error = %v, want PromptBatchInvariantError", err)
	}
	select {
	case result := <-pending.result:
		if !errors.As(result.err, &invariantErr) {
			t.Fatalf("prompt await error = %v, want PromptBatchInvariantError", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt awaiter remained blocked after malformed prepared batch")
	}
	if _, err := pending.handle.Wait(context.Background()); !errors.As(err, &invariantErr) {
		t.Fatalf("prompt execution error = %v, want PromptBatchInvariantError", err)
	}
	if _, err := fixture.authority.ResolvePendingWorkflowPrompt(reference.TaskID, request.ID); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("resolved malformed prompt error = %v, want prompt not found", err)
	}
}

func TestCurrentNodeControllerRejectsOwnershipMismatchWithoutPromptDelivery(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-question-mismatch", "node-question")
	request := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)
	fixture.store.setBindingError(workflowstore.ErrSessionNotCurrentWorkflowNode)

	err := fixture.answerWorkflowQuestion(
		context.Background(),
		reference.TaskID,
		request.ID,
		currentNodeQuestionAnswer("yes"),
		nil,
	)
	if !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("ownership mismatch answer error = %v, want prompt not found", err)
	}
	select {
	case result := <-pending.result:
		t.Fatalf("ownership mismatch delivered response %+v", result)
	default:
	}
	if err := pending.handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop undelivered prompt execution: %v", err)
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("undelivered prompt result error = %v, want cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for canceled undelivered prompt")
	}
}

func TestCurrentNodeControllerRejectsAmbiguousPromptScope(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	taskID := workflow.TaskID("task-question-ambiguous")
	request := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	pending := fixture.startPendingPrompts(t, []workflow.CurrentNodeReference{
		currentNodeReferenceForControllerTest(t, string(taskID), "node-question-a"),
		currentNodeReferenceForControllerTest(t, string(taskID), "node-question-b"),
	}, request)
	first, second := pending[0], pending[1]
	fixture.waitForAmbiguousPendingPrompt(t, taskID, request.ID)

	err := fixture.answerWorkflowQuestion(
		context.Background(),
		taskID,
		request.ID,
		currentNodeQuestionAnswer("yes"),
		nil,
	)
	if !errors.Is(err, sessionruntime.ErrWorkflowPromptAmbiguous) {
		t.Fatalf("ambiguous prompt answer error = %v, want prompt ambiguity", err)
	}
	if calls := fixture.store.bindingCalls(); len(calls) != 0 {
		t.Fatalf("ambiguous prompt checked durable bindings = %+v, want none", calls)
	}
	for _, pending := range []currentNodePendingPrompt{first, second} {
		select {
		case result := <-pending.result:
			t.Fatalf("ambiguous prompt delivered response %+v", result)
		default:
		}
		if err := pending.handle.Stop(context.Background()); err != nil {
			t.Fatalf("stop ambiguous prompt execution: %v", err)
		}
		select {
		case result := <-pending.result:
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("ambiguous prompt result error = %v, want cancellation", result.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for canceled ambiguous prompt")
		}
	}
}
