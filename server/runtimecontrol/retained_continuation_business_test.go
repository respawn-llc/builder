package runtimecontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type retainedContinuationExecutionHandle struct{}

type runtimeControlWorkflowReactivatorFunc func(
	context.Context,
	runtimeids.SessionID,
	runtime.CommandAcceptance,
	context.Context,
	*workflowexecution.WorkflowSessionContinuation,
) (workflowexecution.WorkflowSessionContinuationResult, error)

func (f runtimeControlWorkflowReactivatorFunc) ReactivateWorkflowSessionWithAcceptance(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	accept runtime.CommandAcceptance,
	acceptedCtx context.Context,
	continuation *workflowexecution.WorkflowSessionContinuation,
) (workflowexecution.WorkflowSessionContinuationResult, error) {
	return f(ctx, sessionID, accept, acceptedCtx, continuation)
}

func (retainedContinuationExecutionHandle) Scope() sessionruntime.ExecutionScope { return sessionruntime.ExecutionScope{} }
func (retainedContinuationExecutionHandle) RequestStop() bool                    { return false }
func (retainedContinuationExecutionHandle) Stop(context.Context) error            { return nil }
func (retainedContinuationExecutionHandle) Wait(context.Context) (sessionruntime.ExecutionResult, error) {
	return sessionruntime.ExecutionResult{}, nil
}
func (retainedContinuationExecutionHandle) Close(context.Context) error { return nil }

func newRetainedContinuationService(
	t *testing.T,
	reactivator runtimeControlWorkflowReactivatorFunc,
) (*session.Store, *Service) {
	t.Helper()
	store, engine, service := newRuntimeControlTestService(
		t,
		&runtimeControlFakeClient{},
		nil,
		runtime.Config{},
	)
	execution := runtimeControlExactExecution(t)
	execution.CompletionMode = workflowruntime.CompletionModeUnstructuredOutput
	binding, err := engine.BindCurrentNodeExecution(execution)
	if err != nil {
		t.Fatalf("bind retained Workflow activation: %v", err)
	}
	t.Cleanup(func() {
		if err := binding.Close(); err != nil && !errors.Is(err, runtime.ErrEngineClosed) {
			t.Errorf("close retained Workflow activation: %v", err)
		}
	})
	service.WithWorkflowSessionReactivator(reactivator)
	return store, service
}

func TestSetThinkingLevelPersistsForDormantRetainedSession(t *testing.T) {
	store, _, service := newRuntimeControlTestService(
		t,
		&runtimeControlFakeClient{},
		nil,
		runtime.Config{},
	)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}
	resource, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("new Session resource reference: %v", err)
	}
	if _, err := service.authority.ReleaseRuntime(context.Background(), sessionruntime.RuntimeReleaseRequest{
		Resource: resource,
		OwnerID:  "runtimecontrol-test",
		Policy:   sessionruntime.RuntimeReleaseClose,
	}); err != nil {
		t.Fatalf("release retained Session runtime: %v", err)
	}
	if _, live := service.authority.SessionExecution(sessionID); live {
		t.Fatal("Thinking mutation test retained a live runtime")
	}

	if err := service.SetThinkingLevel(context.Background(), serverapi.RuntimeSetThinkingLevelRequest{
		SessionID: store.Meta().SessionID,
		Level:     "high",
	}); err != nil {
		t.Fatalf("SetThinkingLevel for dormant Session: %v", err)
	}
	reopened, err := session.Open(store.Dir(), runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("reopen dormant Session: %v", err)
	}
	if settings := reopened.Meta().ChatSettings; settings == nil || settings.Thinking == nil || *settings.Thinking != "high" {
		t.Fatalf("dormant Session Chat settings = %+v, want Thinking high", settings)
	}
}

func acceptedRetainedContinuationReactivator(
	turn runtime.UserTurnResult,
	exactErr error,
	diagnostics []workflowexecution.WorkflowSessionResumeDiagnostic,
) runtimeControlWorkflowReactivatorFunc {
	return func(
		_ context.Context,
		_ runtimeids.SessionID,
		accept runtime.CommandAcceptance,
		_ context.Context,
		continuation *workflowexecution.WorkflowSessionContinuation,
	) (workflowexecution.WorkflowSessionContinuationResult, error) {
		committed, err := accept(func() (bool, error) {
			return true, nil
		})
		if err != nil {
			return workflowexecution.WorkflowSessionContinuationResult{}, err
		}
		if !committed {
			return workflowexecution.WorkflowSessionContinuationResult{}, errors.New("retained continuation was not accepted")
		}
		continuation.RecordTurn(turn, nil)
		continuation.RecordExact(sessionruntime.ExecutionResult{}, exactErr)
		return workflowexecution.WorkflowSessionContinuationResult{
			Handle:             retainedContinuationExecutionHandle{},
			SiblingDiagnostics: diagnostics,
		}, nil
	}
}

func TestSubmitUserTurnRetainedLifecycleNoOpRejectsInputAndHistory(t *testing.T) {
	taskID := workflow.TaskID("task-retained-no-op")
	store, service := newRetainedContinuationService(t, runtimeControlWorkflowReactivatorFunc(
		func(
			_ context.Context,
			_ runtimeids.SessionID,
			_ runtime.CommandAcceptance,
			_ context.Context,
			_ *workflowexecution.WorkflowSessionContinuation,
		) (workflowexecution.WorkflowSessionContinuationResult, error) {
			return workflowexecution.WorkflowSessionContinuationResult{}, &workflowexecution.TaskResumeConflictError{
				TaskID: string(taskID),
				State:  workflowexecution.TaskResumeConflictCurrentNodeNotInterrupted,
			}
		},
	))

	_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "retained-no-op", "continue"))
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("SubmitUserTurn error = %v, want not-accepted error", err)
	}
	var conflict *workflowexecution.TaskResumeConflictError
	if !errors.As(err, &conflict) || conflict.TaskID != string(taskID) {
		t.Fatalf("SubmitUserTurn error = %T %v, want retained lifecycle conflict", err, err)
	}
	if countPromptHistoryEvents(t, store, "continue") != 0 {
		t.Fatal("rejected retained continuation recorded prompt history")
	}
}

func TestSubmitUserTurnRetainedCancellationBeforeAcceptanceRejects(t *testing.T) {
	entered := make(chan struct{})
	store, service := newRetainedContinuationService(t, runtimeControlWorkflowReactivatorFunc(
		func(
			ctx context.Context,
			_ runtimeids.SessionID,
			_ runtime.CommandAcceptance,
			_ context.Context,
			_ *workflowexecution.WorkflowSessionContinuation,
		) (workflowexecution.WorkflowSessionContinuationResult, error) {
			close(entered)
			<-ctx.Done()
			return workflowexecution.WorkflowSessionContinuationResult{}, ctx.Err()
		},
	))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(ctx, runtimeControlUserTurnRequest(store, "retained-pre-cancel", "continue"))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retained continuation did not reach Resume preparation")
	}
	cancel()
	err := <-done
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-acceptance cancellation error = %v, want canceled not-accepted error", err)
	}
	if countPromptHistoryEvents(t, store, "continue") != 0 {
		t.Fatal("pre-acceptance cancellation recorded prompt history")
	}
}

func TestSubmitUserTurnRetainedCancellationAfterAcceptanceKeepsHistory(t *testing.T) {
	accepted := make(chan struct{})
	store, service := newRetainedContinuationService(t, runtimeControlWorkflowReactivatorFunc(
		func(
			_ context.Context,
			_ runtimeids.SessionID,
			accept runtime.CommandAcceptance,
			_ context.Context,
			_ *workflowexecution.WorkflowSessionContinuation,
		) (workflowexecution.WorkflowSessionContinuationResult, error) {
			committed, err := accept(func() (bool, error) {
				close(accepted)
				return true, nil
			})
			if err != nil || !committed {
				return workflowexecution.WorkflowSessionContinuationResult{}, errors.Join(err, errors.New("retained continuation was not accepted"))
			}
			return workflowexecution.WorkflowSessionContinuationResult{
				Handle: retainedContinuationExecutionHandle{},
			}, nil
		},
	))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(ctx, runtimeControlUserTurnRequest(store, "retained-post-cancel", "continue"))
		done <- err
	}()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("retained continuation was not accepted")
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-acceptance cancellation error = %v, want context cancellation", err)
	}
	if errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("post-acceptance cancellation was reported as not accepted: %v", err)
	}
	if countPromptHistoryEvents(t, store, "continue") != 1 {
		t.Fatal("post-acceptance cancellation did not retain prompt history")
	}
}

func TestSubmitUserTurnRetainedSelectedOutcomeAndSiblingDiagnostics(t *testing.T) {
	selectedExecutionFailure := errors.New("selected execution failed")
	diagnostics := []workflowexecution.WorkflowSessionResumeDiagnostic{{
		Reference: workflow.CurrentNodeReference{TaskID: "sibling-task", NodeID: "sibling-node"},
		Cause:     errors.New("sibling Resume failed"),
	}}
	store, service := newRetainedContinuationService(t, acceptedRetainedContinuationReactivator(
		runtime.UserTurnResult{
			Kind: runtime.UserTurnResultAssistantFinal,
			FinalAnswer: &llm.Message{Content: textutil.Value("selected final")},
		},
		selectedExecutionFailure,
		diagnostics,
	))
	response, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "retained-outcome", "continue"))
	if !errors.Is(err, selectedExecutionFailure) {
		t.Fatalf("SubmitUserTurn error = %v, want %v", err, selectedExecutionFailure)
	}
	if response.ResultKind != clientui.UserTurnResultKindNoFinal {
		t.Fatalf("SubmitUserTurn result kind = %q, want no-final result", response.ResultKind)
	}
	if countPromptHistoryEvents(t, store, "continue") != 1 {
		t.Fatal("accepted retained continuation did not record prompt history once")
	}
}
