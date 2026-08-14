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

	"github.com/google/uuid"
)

type failingResolutionPromptFeed struct {
	err error
}

func (failingResolutionPromptFeed) PromptPendingScope(
	sessionruntime.ExecutionScope,
	askquestion.AskQuestionRequest,
	time.Time,
) error {
	return nil
}

func (f failingResolutionPromptFeed) PromptResolvedScope(
	sessionruntime.ExecutionScope,
	string,
) error {
	return f.err
}

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
	pending := fixture.startPendingPrompt(t, question, request)
	fixture.waitForPendingPrompt(t, question.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})

	runningHandle := startLiveTestWorkflowScript(t, fixture.controller, fixture.authority, running, sessionruntime.ScriptExecutionRequest{
		Command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
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

func TestCurrentNodeControllerTaskInterruptRejectsDurablyInterruptedWaitingQuestion(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-interrupted-question", "node-question")
	request := askquestion.AskQuestionRequest{
		ID:       "ask-interrupted-question",
		StepID:   uuid.NewString(),
		Question: "Keep waiting?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})
	fixture.store.currentNodes = []workflow.CurrentNode{{
		Reference: reference,
		SessionID: &pending.sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
		},
	}}

	if err := fixture.controller.Interrupt(
		context.Background(),
		InterruptSelector{TaskID: reference.TaskID},
	); !errors.Is(err, sessionruntime.ErrWorkflowQuestionPending) {
		t.Fatalf("Interrupt durably interrupted waiting Question error = %v, want %v", err, sessionruntime.ErrWorkflowQuestionPending)
	}
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); !live {
		t.Fatal("Interrupt stopped the waiting Question")
	}
}

func TestCurrentNodeControllerTaskInterruptRejectsDurablyInterruptedPendingApproval(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-interrupted-approval", "node-approval")
	request := askquestion.AskQuestionRequest{
		ID:       "ask-interrupted-approval",
		StepID:   uuid.NewString(),
		Question: "Approve?",
		Approval: true,
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})
	fixture.store.currentNodes = []workflow.CurrentNode{{
		Reference: reference,
		SessionID: &pending.sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
		},
	}}

	if err := fixture.controller.Interrupt(
		context.Background(),
		InterruptSelector{TaskID: reference.TaskID},
	); !errors.Is(err, sessionruntime.ErrWorkflowApprovalPending) {
		t.Fatalf("Interrupt durably interrupted pending Approval error = %v, want %v", err, sessionruntime.ErrWorkflowApprovalPending)
	}
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); !live {
		t.Fatal("Interrupt stopped the pending Approval")
	}
}

func TestCurrentNodeControllerManualMoveCancelsWaitingQuestionAndStopsSibling(t *testing.T) {
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
	pending := fixture.startPendingPrompt(t, question, request)
	fixture.waitForPendingPrompt(t, question.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})
	runningHandle := startLiveTestWorkflowScript(t, fixture.controller, fixture.authority, running, sessionruntime.ScriptExecutionRequest{
		Command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
	})
	waitForRunningCurrentNode(t, fixture.authority, running)

	if err := fixture.controller.InterruptForManualMove(context.Background(), running.TaskID, nil); err != nil {
		t.Fatalf("InterruptForManualMove: %v", err)
	}
	if _, live := fixture.authority.ExecutionByScope(runningHandle.Scope().ID()); live {
		t.Fatal("manual move left the running sibling live")
	}
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); live {
		t.Fatal("manual move left the waiting Question live")
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("waiting Question result = %v, want context cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual move did not cancel the waiting Question")
	}
	if _, interrupted := fixture.store.interruption(running); !interrupted {
		t.Fatal("manual move did not persist the sibling interruption")
	}
	if _, interrupted := fixture.store.interruption(question); !interrupted {
		t.Fatal("manual move did not persist the waiting Question interruption")
	}
}

func TestCurrentNodeControllerManualMoveCleansUpAfterQuestionResolutionPublicationFailure(t *testing.T) {
	publicationFailure := errors.New("publish Question resolution")
	fixture := newCurrentNodeQuestionFixtureWithPromptFeed(
		t,
		failingResolutionPromptFeed{err: publicationFailure},
	)
	reference := currentNodeReferenceForControllerTest(
		t,
		"task-manual-move-publication-failure",
		"node-question",
	)
	request := askquestion.AskQuestionRequest{
		ID:       "ask-manual-move-publication-failure",
		StepID:   uuid.NewString(),
		Question: "Keep waiting?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})

	err := fixture.controller.InterruptForManualMove(context.Background(), reference.TaskID, nil)
	if !errors.Is(err, publicationFailure) {
		t.Fatalf("InterruptForManualMove error = %v, want publication failure", err)
	}
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); live {
		t.Fatal("manual move publication failure left the waiting Question live")
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("waiting Question result = %v, want context cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual move publication failure did not cancel the waiting Question")
	}
	if _, interrupted := fixture.store.interruption(reference); !interrupted {
		t.Fatal("manual move publication failure did not persist interruption")
	}
	if err := fixture.controller.InterruptForManualMove(
		context.Background(),
		reference.TaskID,
		nil,
	); err != nil {
		t.Fatalf("second Manual Move remained fenced: %v", err)
	}
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
		if disposition != ManualMoveDispositionAutoInterruptible {
			t.Fatalf("disposition = %q, want auto_interruptible", disposition)
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

	outcome, err := fixture.answerPromptBatch(
		context.Background(),
		pending,
		request.StepID,
		"different-ask-id",
		currentNodeQuestionAnswer("yes"),
	)
	if err != nil || outcome != sessionruntime.PromptAnswerOutcomeSkipped {
		t.Fatalf("unknown prompt batch = (%q, %v), want skipped", outcome, err)
	}
	outcome, err = fixture.answerPromptBatch(
		context.Background(),
		pending,
		request.StepID,
		request.ID,
		currentNodeQuestionAnswer("yes"),
	)
	if err != nil || outcome != sessionruntime.PromptAnswerOutcomeResolved {
		t.Fatalf("exact prompt batch = (%q, %v), want resolved", outcome, err)
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
	if calls := fixture.store.bindingCalls(); len(calls) != 0 {
		t.Fatalf("batch answer consulted Workflow durable bindings: %+v", calls)
	}
}

func TestCurrentNodeControllerReleasesTaskMutationLaneAfterAcceptingAnswer(t *testing.T) {
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
		_, err := fixture.answerPromptBatch(
			context.Background(),
			currentNodePendingPrompt{sessionID: sessionID},
			stepID,
			firstID,
			currentNodeQuestionAnswer("one"),
		)
		answerDone <- err
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
		_, err := fixture.answerPromptBatch(
			context.Background(),
			independent,
			independentRequest.StepID,
			independentRequest.ID,
			currentNodeQuestionAnswer("independent"),
		)
		independentDone <- err
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
		if err != nil {
			t.Fatalf("first prompt batch: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt batch waited for prepared successor")
	}

	allowSuccessor <- struct{}{}
	fixture.waitForPendingPrompt(t, reference.TaskID, secondID)
	outcome, err := fixture.answerPromptBatch(
		context.Background(),
		currentNodePendingPrompt{sessionID: sessionID},
		stepID,
		secondID,
		currentNodeQuestionAnswer("two"),
	)
	if err != nil || outcome != sessionruntime.PromptAnswerOutcomeResolved {
		t.Fatalf("second prompt batch = (%q, %v), want resolved", outcome, err)
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

	_, err := fixture.answerPromptBatch(
		context.Background(),
		pending,
		request.StepID,
		request.ID,
		currentNodeQuestionAnswer("yes"),
	)
	var invariantErr sessionruntime.PromptBatchInvariantError
	if !errors.As(err, &invariantErr) {
		t.Fatalf("AnswerWorkflowQuestion error = %v, want PromptBatchInvariantError", err)
	}
	select {
	case result := <-pending.result:
		t.Fatalf("malformed batch mutated pending prompt: %+v", result)
	default:
	}
	if count := fixture.pendingPromptCount(reference.TaskID, request.ID); count != 1 {
		t.Fatalf("malformed batch retained %d pending prompts, want 1", count)
	}
	if err := pending.handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop pending prompt: %v", err)
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("stopped prompt result error = %v, want cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stopped prompt")
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

	outcome, err := fixture.answerPromptBatch(
		context.Background(),
		pending,
		request.StepID,
		request.ID,
		currentNodeQuestionAnswer("yes"),
	)
	if err != nil || outcome != sessionruntime.PromptAnswerOutcomeResolved {
		t.Fatalf("prompt batch = (%q, %v), want resolved", outcome, err)
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
	if calls := fixture.store.bindingCalls(); len(calls) != 0 {
		t.Fatalf("batch answer consulted Workflow durable bindings: %+v", calls)
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
	first := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-a"), request)
	fixture.waitForPendingPrompt(t, taskID, request.ID)
	second := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-b"), request)
	fixture.waitForAmbiguousPendingPrompt(t, taskID, request.ID)

	if count := fixture.pendingPromptCount(taskID, request.ID); count != 2 {
		t.Fatalf("matching pending prompts = %d, want 2 exact executions", count)
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
