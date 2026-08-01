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
	pending := fixture.startPendingPrompt(t, question, request)
	fixture.waitForPendingPrompt(t, question.TaskID, request.ID)
	t.Cleanup(func() {
		pending.handle.RequestStop()
		_, _ = pending.handle.Wait(context.Background())
	})

	lease, err := fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-test",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: running,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	runningHandle, err := fixture.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	runningKey, err := running.Key()
	if err != nil {
		t.Fatalf("running Current Node key: %v", err)
	}
	fixture.controller.mu.Lock()
	fixture.controller.live[lease.ScopeID()] = currentNodeLiveScope{reference: running, lease: lease}
	fixture.controller.liveByNode[runningKey] = lease.ScopeID()
	fixture.controller.mu.Unlock()
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

func TestCurrentNodeControllerObservesExactExecutionsAndQuiescenceTogether(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-observation", "node-observation")
	request := askquestion.AskQuestionRequest{
		ID:       "observation-question",
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)
	t.Cleanup(func() {
		_ = pending.handle.Stop(context.Background())
	})

	observation, err := fixture.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	executions := observation.Executions[reference.TaskID].Executions
	if len(executions) != 1 ||
		!executions[0].Ref.CurrentNode.Equal(reference) ||
		!executions[0].HasPendingPromptKind(sessionruntime.PendingPromptKindQuestion) {
		t.Fatalf("observed executions = %+v", executions)
	}
	if observation.Quiescence[reference.TaskID] {
		t.Fatal("waiting Question execution was observed as quiescent")
	}

	if err := pending.handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop pending execution: %v", err)
	}
	afterRetirement, err := fixture.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions after retirement: %v", err)
	}
	if len(afterRetirement.Executions[reference.TaskID].Executions) != 0 {
		t.Fatalf("retired executions = %+v", afterRetirement.Executions[reference.TaskID].Executions)
	}
	if !afterRetirement.Quiescence[reference.TaskID] {
		t.Fatal("retired execution was observed as non-quiescent")
	}
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

	if err := fixture.controller.AnswerWorkflowQuestion(context.Background(), reference.TaskID, "different-ask-id", askquestion.AskQuestionResponse{RequestID: "different-ask-id", Answer: "yes"}, nil); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("unknown prompt answer error = %v, want prompt not found", err)
	}
	if err := fixture.controller.AnswerWorkflowQuestion(context.Background(), reference.TaskID, request.ID, askquestion.AskQuestionResponse{RequestID: request.ID, Answer: "yes"}, nil); err != nil {
		t.Fatalf("AnswerWorkflowQuestion: %v", err)
	}
	select {
	case result := <-pending.result:
		if result.err != nil || result.response.RequestID != request.ID || result.response.Answer != "yes" {
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

	err := fixture.controller.AnswerWorkflowQuestion(context.Background(), reference.TaskID, request.ID, askquestion.AskQuestionResponse{RequestID: request.ID, Answer: "yes"}, nil)
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
	first := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-a"), request)
	fixture.waitForPendingPrompt(t, taskID, request.ID)
	second := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-b"), request)
	fixture.waitForAmbiguousPendingPrompt(t, taskID, request.ID)

	err := fixture.controller.AnswerWorkflowQuestion(context.Background(), taskID, request.ID, askquestion.AskQuestionResponse{RequestID: request.ID, Answer: "yes"}, nil)
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
