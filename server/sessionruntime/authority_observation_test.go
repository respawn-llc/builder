package sessionruntime

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

func TestAuthorityAllWorkflowExecutionSnapshotTracksExactTargetPromptAndRetirement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	feed := make(authorityPromptFeed, 2)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		PromptFeed:      feed,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	workflowRef := WorkflowExecutionRef{
		TaskID:     "task-observed-authority",
		RunID:      "run-observed-authority",
		Generation: 3,
	}
	request := tools.AskQuestionRequest{
		ID:       "ask-observed-authority",
		StepID:   uuid.NewString(),
		Question: "Continue?",
	}
	startPrompt := make(chan struct{})
	releaseRunner := make(chan struct{})
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    ptrAuthorityTestRuntimePlan(t, fixture),
		Workflow:   &workflowRef,
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, scope ExecutionScope, _ AgentRuntimeBridge) error {
			<-startPrompt
			if _, err := authority.AwaitPromptResponse(ctx, scope.ID(), request); err != nil {
				return err
			}
			<-releaseRunner
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}

	registered, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot after registration: %v", err)
	}
	requireAuthoritySnapshotExecution(t, registered, workflowRef, sessionID, false, 0)

	registered.Executions[0].Execution.Agent.SessionID = runtimeids.SessionID{}
	unchanged, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot after caller mutation: %v", err)
	}
	requireAuthoritySnapshotExecution(t, unchanged, workflowRef, sessionID, false, 0)

	var readers sync.WaitGroup
	readErrs := make(chan error, 32)
	for range 32 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 64 {
				snapshot, err := authority.AllWorkflowExecutionSnapshot()
				if err != nil {
					readErrs <- err
					return
				}
				if len(snapshot.Executions) != 1 || snapshot.Executions[0].Execution.Ref != workflowRef {
					readErrs <- &authoritySnapshotReadError{snapshot: snapshot}
					return
				}
			}
		}()
	}
	readers.Wait()
	close(readErrs)
	for err := range readErrs {
		t.Fatalf("concurrent observation read: %v", err)
	}

	close(startPrompt)
	pending := <-feed
	if pending.scopeID != handle.Scope().ID() || pending.requestID != request.ID {
		t.Fatalf("pending prompt = %+v, want scope %s request %s", pending, handle.Scope().ID(), request.ID)
	}
	promptPending, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot while pending: %v", err)
	}
	requireAuthoritySnapshotExecution(t, promptPending, workflowRef, sessionID, true, 1)
	if promptPending.ExecutionMapRevision != unchanged.ExecutionMapRevision {
		t.Fatalf("prompt mutation changed execution-map revision from %d to %d", unchanged.ExecutionMapRevision, promptPending.ExecutionMapRevision)
	}

	if err := authority.SubmitPromptResponse(sessionID, tools.AskQuestionResponse{
		RequestID: request.ID,
		Answer:    "yes",
	}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	resolved := <-feed
	if !resolved.resolved || resolved.scopeID != handle.Scope().ID() || resolved.requestID != request.ID {
		t.Fatalf("resolved prompt = %+v, want resolved scope %s request %s", resolved, handle.Scope().ID(), request.ID)
	}
	promptResolved, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot after prompt resolution: %v", err)
	}
	requireAuthoritySnapshotExecution(t, promptResolved, workflowRef, sessionID, false, 2)

	close(releaseRunner)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait execution retirement: %v", err)
	}
	retired, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot after retirement: %v", err)
	}
	if len(retired.Executions) != 0 {
		t.Fatalf("retired executions = %+v, want empty", retired.Executions)
	}
	if retired.ExecutionMapRevision <= promptResolved.ExecutionMapRevision {
		t.Fatalf("retired execution-map revision = %d, want > %d", retired.ExecutionMapRevision, promptResolved.ExecutionMapRevision)
	}
}

func TestAuthorityAllWorkflowExecutionSnapshotRevisionDetectsSameValueABA(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	workflowRef := WorkflowExecutionRef{TaskID: "task-observation-aba", RunID: "run-observation-aba", Generation: 1}
	start := func() ExecutionHandle {
		t.Helper()
		grace := 50 * time.Millisecond
		handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
			Workflow: &workflowRef,
			Command: ScriptCommand{
				Path:              sleepPath,
				Args:              []string{"30"},
				CancellationGrace: &grace,
			},
		})
		if err != nil {
			t.Fatalf("StartScriptExecution: %v", err)
		}
		return handle
	}

	firstHandle := start()
	first, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot first: %v", err)
	}
	if len(first.Executions) != 1 ||
		first.Executions[0].Execution.Ref != workflowRef ||
		first.Executions[0].Execution.Script == nil ||
		first.Executions[0].Execution.Script.Path != sleepPath {
		t.Fatalf("first snapshot = %+v", first)
	}
	if err := firstHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop first script: %v", err)
	}
	retired, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot after first retirement: %v", err)
	}
	if len(retired.Executions) != 0 || retired.ExecutionMapRevision <= first.ExecutionMapRevision {
		t.Fatalf("retired snapshot = %+v, want empty with revision > %d", retired, first.ExecutionMapRevision)
	}

	secondHandle := start()
	t.Cleanup(func() { _ = secondHandle.Stop(context.Background()) })
	second, err := authority.AllWorkflowExecutionSnapshot()
	if err != nil {
		t.Fatalf("AllWorkflowExecutionSnapshot second: %v", err)
	}
	if len(second.Executions) != 1 ||
		second.Executions[0].Execution.Ref != first.Executions[0].Execution.Ref ||
		second.Executions[0].Execution.Agent != nil ||
		second.Executions[0].Execution.Script == nil ||
		first.Executions[0].Execution.Script == nil ||
		second.Executions[0].Execution.Script.Path != first.Executions[0].Execution.Script.Path ||
		second.Executions[0].Execution.WaitingQuestion != first.Executions[0].Execution.WaitingQuestion ||
		second.Executions[0].PromptRevision != first.Executions[0].PromptRevision {
		t.Fatalf("ABA executions = %+v, want same observed values as %+v", second.Executions, first.Executions)
	}
	if second.ExecutionMapRevision <= retired.ExecutionMapRevision {
		t.Fatalf("ABA execution-map revision = %d, want > %d", second.ExecutionMapRevision, retired.ExecutionMapRevision)
	}
}

func requireAuthoritySnapshotExecution(
	t *testing.T,
	snapshot AllWorkflowExecutionSnapshot,
	ref WorkflowExecutionRef,
	sessionID runtimeids.SessionID,
	pending bool,
	promptRevision WorkflowExecutionPromptRevision,
) {
	t.Helper()
	if snapshot.ExecutionMapRevision == 0 {
		t.Fatal("execution-map revision is zero after workflow execution registration")
	}
	if len(snapshot.Executions) != 1 {
		t.Fatalf("executions = %+v, want exactly one", snapshot.Executions)
	}
	observation := snapshot.Executions[0]
	execution := observation.Execution
	if execution.Ref != ref ||
		execution.Agent == nil ||
		execution.Agent.SessionID != sessionID ||
		execution.Script != nil ||
		execution.WaitingQuestion != pending ||
		observation.PromptRevision != promptRevision {
		t.Fatalf("execution = %+v, want ref=%+v agent session=%s pending=%t prompt revision=%d",
			execution, ref, sessionID, pending, promptRevision)
	}
}

type authoritySnapshotReadError struct {
	snapshot AllWorkflowExecutionSnapshot
}

func (e *authoritySnapshotReadError) Error() string {
	return "all-workflow authority snapshot did not retain the registered exact execution"
}

func ptrAuthorityTestRuntimePlan(t *testing.T, fixture sessionRuntimeFixture) *AgentRuntimePlan {
	t.Helper()
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	return &plan
}
