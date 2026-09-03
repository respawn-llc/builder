package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/runtimeids"

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

func TestCurrentNodeControllerTaskInterruptClosesWaitingQuestionAndRunningScope(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	fixture := newCurrentNodeQuestionFixture(t)
	question := currentNodeReferenceForControllerTest(t, "task-running-and-question", "node-question")
	running := currentNodeReferenceForControllerTest(t, "task-running-and-question", "node-running")
	request := askquestion.AskQuestionRequest{
		ToolCallID: "ask-running-and-question",
		StepID:     uuid.NewString(),
		Question:   "Keep waiting?",
	}
	pending := fixture.startPendingPrompt(t, question, request)
	fixture.waitForPendingPrompt(t, question.TaskID, request.ToolCallID)
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
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); live {
		t.Fatal("Task Interrupt left the waiting Question live")
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("waiting Question result = %v, want context cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Task Interrupt did not cancel the waiting Question")
	}
	if err := fixture.controller.EnsureTaskQuiescent(running.TaskID); err != nil {
		t.Fatalf("quiescence after Task Interrupt: %v", err)
	}
	if _, interrupted := fixture.store.interruption(question); !interrupted {
		t.Fatal("Task Interrupt did not persist interruption for the waiting Question")
	}
	if _, interrupted := fixture.store.interruption(running); !interrupted {
		t.Fatal("Task Interrupt did not persist interruption for the running Script")
	}
}

func TestCurrentNodeControllerTaskInterruptClosesDurablyInterruptedWaitingQuestion(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-interrupted-question", "node-question")
	request := askquestion.AskQuestionRequest{
		ToolCallID: "ask-interrupted-question",
		StepID:     uuid.NewString(),
		Question:   "Keep waiting?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ToolCallID)
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
	); err != nil {
		t.Fatalf("Interrupt durably interrupted waiting Question: %v", err)
	}
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); live {
		t.Fatal("Interrupt left the waiting Question live")
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("waiting Question result = %v, want context cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Interrupt did not cancel the waiting Question")
	}
	if err := fixture.controller.EnsureTaskQuiescent(reference.TaskID); err != nil {
		t.Fatalf("quiescence after waiting Question interrupt: %v", err)
	}
}

func TestCurrentNodeControllerTaskInterruptClosesDurablyInterruptedPendingApproval(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-interrupted-approval", "node-approval")
	request := askquestion.AskQuestionRequest{
		ToolCallID: "ask-interrupted-approval",
		StepID:     uuid.NewString(),
		Question:   "Approve?",
		Approval:   true,
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ToolCallID)
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
	); err != nil {
		t.Fatalf("Interrupt durably interrupted pending Approval: %v", err)
	}
	if _, live := fixture.authority.ExecutionByScope(pending.handle.Scope().ID()); live {
		t.Fatal("Interrupt left the pending Approval live")
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("pending Approval result = %v, want context cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Interrupt did not cancel the pending Approval")
	}
	if err := fixture.controller.EnsureTaskQuiescent(reference.TaskID); err != nil {
		t.Fatalf("quiescence after pending Approval interrupt: %v", err)
	}
}

func TestCurrentNodeControllerArbitratesPendingApproval(t *testing.T) {
	for _, test := range []struct {
		name, id                  string
		manualMove, approvalFirst bool
	}{
		{"Task Interrupt/Approval first", "task-approval-first", false, true},
		{"Task Interrupt/operation first", "task-operation-first", false, false},
		{"Manual Move/Approval first", "move-approval-first", true, true},
		{"Manual Move/operation first", "move-operation-first", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			feed := &currentNodeApprovalFeed{resolutionStarted: make(chan string, 1), resolutionRelease: make(chan struct{})}
			t.Cleanup(feed.release)
			fixture := newCurrentNodeQuestionFixtureWithPromptFeed(t, feed)
			reference := currentNodeReferenceForControllerTest(t, test.id, "node-approval")
			request := askquestion.AskQuestionRequest{
				ToolCallID: "approval-" + test.id, StepID: uuid.NewString(), Question: "Allow access?", Approval: true,
				ApprovalOptions: []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}},
			}
			stepID, err := runtimeids.ParseStepID(request.StepID)
			if err != nil {
				t.Fatalf("ParseStepID: %v", err)
			}
			pending := fixture.startPendingPrompt(t, reference, request)
			fixture.waitForPendingPrompt(t, reference.TaskID, request.ToolCallID)
			t.Cleanup(func() { _ = pending.handle.Stop(context.Background()) })

			var sibling sessionruntime.ExecutionHandle
			if !test.manualMove && test.approvalFirst {
				shellPath, lookupErr := exec.LookPath("sh")
				if lookupErr != nil {
					t.Skipf("sh executable unavailable: %v", lookupErr)
				}
				siblingReference := currentNodeReferenceForControllerTest(t, test.id, "node-running-sibling")
				sibling = startLiveTestWorkflowScript(t, fixture.controller, fixture.authority, siblingReference, sessionruntime.ScriptExecutionRequest{
					Command: sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"}},
				})
				waitForRunningCurrentNode(t, fixture.authority, siblingReference)
				t.Cleanup(func() { _ = sibling.Stop(context.Background()) })
			}

			answerDone := make(chan currentNodeApprovalAnswerResult, 1)
			submitAnswer := func() {
				results, answerErr := fixture.authority.ResolvePromptBatch(context.Background(), pending.sessionID, stepID, []sessionruntime.PromptAnswerCommand{{
					ToolCallID: clientui.ToolCallID(request.ToolCallID),
					Payload:    sessionruntime.PromptApprovalAnswerCommand{Answer: askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce}},
				}})
				answerDone <- currentNodeApprovalAnswerResult{results: results, err: answerErr}
			}
			operationDone := make(chan error, 1)
			operation := func() error {
				if test.manualMove {
					return fixture.controller.InterruptForManualMove(context.Background(), reference.TaskID, nil)
				}
				return fixture.controller.Interrupt(context.Background(), InterruptSelector{TaskID: reference.TaskID})
			}

			if test.approvalFirst {
				go submitAnswer()
				feed.waitForResolution(t, request.ToolCallID)
				go func() { operationDone <- operation() }()
				select {
				case err := <-operationDone:
					t.Fatalf("operation completed before Approval finalization: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
			} else {
				go func() { operationDone <- operation() }()
				feed.waitForResolution(t, request.ToolCallID)
				go submitAnswer()
				select {
				case answer := <-answerDone:
					t.Fatalf("Approval completed before operation finalization: %+v", answer)
				case <-time.After(100 * time.Millisecond):
				}
			}
			feed.release()

			var consumer currentNodePromptResult
			select {
			case consumer = <-pending.result:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for Approval consumer")
			}
			if test.approvalFirst {
				approval, ok := consumer.resolution.(askquestion.AskQuestionApproval)
				if consumer.err != nil || !ok || approval.Decision != askquestion.AskQuestionApprovalDecisionAllowOnce || approval.Commentary != nil {
					t.Fatalf("Approval consumer result = (%+v, %v), want Allow once", consumer.resolution, consumer.err)
				}
			} else if !errors.Is(consumer.err, context.Canceled) || consumer.resolution != nil {
				t.Fatalf("operation-first Approval consumer = (%+v, %v), want cancellation", consumer.resolution, consumer.err)
			}

			var answer currentNodeApprovalAnswerResult
			select {
			case answer = <-answerDone:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for Approval answer")
			}
			wantOutcome := sessionruntime.PromptAnswerOutcomeSkipped
			if test.approvalFirst {
				wantOutcome = sessionruntime.PromptAnswerOutcomeResolved
			}
			if answer.err != nil || len(answer.results) != 1 || answer.results[0].ToolCallID != clientui.ToolCallID(request.ToolCallID) || answer.results[0].Outcome != wantOutcome {
				t.Fatalf("Approval answer = (%+v, %v), want %s", answer.results, answer.err, wantOutcome)
			}
			select {
			case operationErr := <-operationDone:
				if operationErr != nil {
					t.Fatalf("workflow operation: %v", operationErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for workflow operation")
			}
			if sibling != nil {
				if _, live := fixture.authority.ExecutionByScope(sibling.Scope().ID()); live {
					t.Fatal("Task Interrupt left the running sibling live")
				}
			}
			if count := feed.resolved.Load(); count != 1 {
				t.Fatalf("Approval resolution publications = %d, want 1", count)
			}
		})
	}
}

type currentNodeApprovalAnswerResult struct {
	results []sessionruntime.PromptAnswerResult
	err     error
}

type currentNodeApprovalFeed struct {
	resolutionStarted chan string
	resolutionRelease chan struct{}
	releaseOnce       sync.Once
	resolved          atomic.Int32
}

func (*currentNodeApprovalFeed) PromptPendingScope(sessionruntime.ExecutionScope, askquestion.AskQuestionRequest, time.Time) error {
	return nil
}

func (f *currentNodeApprovalFeed) PromptResolvedScope(_ sessionruntime.ExecutionScope, toolCallID string) error {
	f.resolutionStarted <- toolCallID
	<-f.resolutionRelease
	f.resolved.Add(1)
	return nil
}

func (f *currentNodeApprovalFeed) waitForResolution(t *testing.T, toolCallID string) {
	t.Helper()
	select {
	case got := <-f.resolutionStarted:
		if got != toolCallID {
			t.Fatalf("resolved Tool Call ID = %q, want %q", got, toolCallID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Approval resolution publication")
	}
}

func (f *currentNodeApprovalFeed) release() {
	f.releaseOnce.Do(func() { close(f.resolutionRelease) })
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
		ToolCallID: "ask-manual-move-question",
		StepID:     uuid.NewString(),
		Question:   "Keep waiting?",
	}
	pending := fixture.startPendingPrompt(t, question, request)
	fixture.waitForPendingPrompt(t, question.TaskID, request.ToolCallID)
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
		ToolCallID: "ask-manual-move-publication-failure",
		StepID:     uuid.NewString(),
		Question:   "Keep waiting?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ToolCallID)
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

func TestCurrentNodeControllerAnswersOnlyDurablyBoundExactPromptScope(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-question", "node-question")
	request := askquestion.AskQuestionRequest{
		ToolCallID: uuid.NewString(),
		StepID:     uuid.NewString(),
		Question:   "Proceed?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ToolCallID)

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
		request.ToolCallID,
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
		ToolCallID: firstID,
		StepID:     stepID,
		RunID:      runID,
		Origin:     askquestion.AskQuestionOriginModelTool,
		Question:   "First?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               runID,
			StepID:              stepID,
			ToolCallID:          firstID,
			BatchToolCallIDs:    []string{firstID, secondID},
			CandidateOrdinal:    0,
			PreparedPromptCount: 2,
		},
	}
	second := first
	second.ToolCallID = secondID
	second.Question = "Second?"
	second.QuestionBatch = &askquestion.AskQuestionBatchMetadata{
		Origin:              first.QuestionBatch.Origin,
		RunID:               runID,
		StepID:              stepID,
		ToolCallID:          secondID,
		BatchToolCallIDs:    []string{firstID, secondID},
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
		ToolCallID: uuid.NewString(),
		StepID:     uuid.NewString(),
		Question:   "Independent?",
	}
	independent := fixture.startPendingPrompt(t, independentReference, independentRequest)
	fixture.waitForPendingPrompt(t, independentReference.TaskID, independentRequest.ToolCallID)

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
			independentRequest.ToolCallID,
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
		ToolCallID: uuid.NewString(),
		StepID:     uuid.NewString(),
		RunID:      uuid.NewString(),
		Origin:     askquestion.AskQuestionOriginModelTool,
		Question:   "Proceed?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               uuid.NewString(),
			StepID:              uuid.NewString(),
			ToolCallID:          uuid.NewString(),
			BatchToolCallIDs:    []string{uuid.NewString()},
			CandidateOrdinal:    0,
			PreparedPromptCount: 1,
		},
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ToolCallID)

	_, err := fixture.answerPromptBatch(
		context.Background(),
		pending,
		request.StepID,
		request.ToolCallID,
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
	if count := fixture.pendingPromptCount(reference.TaskID, request.ToolCallID); count != 1 {
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
		ToolCallID: uuid.NewString(),
		StepID:     uuid.NewString(),
		Question:   "Proceed?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ToolCallID)
	fixture.store.setBindingError(workflowstore.ErrSessionNotCurrentWorkflowNode)

	outcome, err := fixture.answerPromptBatch(
		context.Background(),
		pending,
		request.StepID,
		request.ToolCallID,
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
		ToolCallID: uuid.NewString(),
		StepID:     uuid.NewString(),
		Question:   "Proceed?",
	}
	first := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-a"), request)
	fixture.waitForPendingPrompt(t, taskID, request.ToolCallID)
	second := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-b"), request)
	fixture.waitForAmbiguousPendingPrompt(t, taskID, request.ToolCallID)

	if count := fixture.pendingPromptCount(taskID, request.ToolCallID); count != 2 {
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
