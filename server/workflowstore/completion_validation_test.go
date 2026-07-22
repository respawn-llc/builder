package workflowstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"core/server/workflow"
)

func startCompletionValidationWorkflow(t *testing.T, create func(*testing.T, context.Context, *Store) workflow.WorkflowID) (context.Context, *Store, TaskRecord, StartTaskResult) {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	workflowID := create(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	return ctx, store, task, startTask(t, ctx, store, task.ID)
}

func createCompletionValidationWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	return createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
}

func createAmbiguousCompletionValidationWorkflow(t *testing.T, ctx context.Context, store *Store, parameters []workflow.Parameter) workflow.WorkflowID {
	t.Helper()
	workflowID := createValidWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		groupID := workflow.TransitionGroupID("group-blocked-" + string(workflowID))
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{ID: groupID, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(nodeByKey(t, def, "agent")), TransitionID: "blocked", DisplayName: "Blocked"})
		req.Edges = append(req.Edges, EdgeRecord{ID: workflow.EdgeID("edge-blocked-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: groupID, Key: "blocked", TargetNodeID: workflow.NodeIDOf(nodeByKind(t, def, workflow.NodeKindTerminal)), ContextMode: workflow.ContextModeNewSession, Parameters: parameters})
	})
	return workflowID
}

func TestStartTaskAllowsCrossRoleContinueSessionContextMode(t *testing.T) {
	startCompletionValidationWorkflow(t, func(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
		return createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "reviewer")
	})
}

func TestCompleteRunValidatesOutputRequirements(t *testing.T) {
	ctx, store, task, started := startCompletionValidationWorkflow(t, createCompletionValidationWorkflow)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "  "}}); !completionHasCode(err, CompletionCodeRequiredOutputMissing) {
		t.Fatalf("expected missing required output error, got %v", err)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions after rejected completion: %v", err)
	}
	if len(transitions) != 1 || transitions[0].TransitionID != "start" {
		t.Fatalf("rejected completion left partial transition rows: %+v", transitions)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after rejected completion: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt != nil || runs[0].InterruptedAt != nil {
		t.Fatalf("rejected completion mutated run outcome: %+v", runs)
	}
}

func TestCompleteRunInfersSingleTransitionID(t *testing.T) {
	ctx, store, _, started := startCompletionValidationWorkflow(t, createValidWorkflow)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID})
}

func TestCompleteRunRejectsMissingTransitionIDWhenAmbiguous(t *testing.T) {
	ctx, store, _, started := startCompletionValidationWorkflow(t, func(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
		return createAmbiguousCompletionValidationWorkflow(t, ctx, store, nil)
	})
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID}); !completionHasCode(err, CompletionCodeTransitionIDRequired) {
		t.Fatalf("expected missing transition id error, got %v", err)
	}
}

func TestCompleteRunRejectsUnknownOutputField(t *testing.T) {
	ctx, store, _, started := startCompletionValidationWorkflow(t, createValidWorkflow)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", OutputValues: map[string]string{"extra": "nope"}}); !completionHasCode(err, CompletionCodeUnknownOutputField) {
		t.Fatalf("expected unknown output error, got %v", err)
	}
}

func TestCompleteRunRejectsParameterDeclaredOnlyByAnotherTransition(t *testing.T) {
	ctx, store, _, started := startCompletionValidationWorkflow(t, func(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
		return createAmbiguousCompletionValidationWorkflow(t, ctx, store, []workflow.Parameter{{Key: "blocked_reason", Description: "Why the task is blocked."}})
	})

	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", OutputValues: map[string]string{"blocked_reason": "blocked"}}); !completionHasCode(err, CompletionCodeUnknownOutputField) {
		t.Fatalf("expected selected-transition unknown output error, got %v", err)
	}
}

func TestCompleteRunReturnsStructuredValidationIssues(t *testing.T) {
	ctx, store, _, started := startCompletionValidationWorkflow(t, createCompletionValidationWorkflow)
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"extra": "nope"}})
	var validation CompletionValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want CompletionValidationError", err, err)
	}
	if !validation.HasCode(CompletionCodeUnknownOutputField) || !validation.HasCode(CompletionCodeRequiredOutputMissing) {
		t.Fatalf("validation issues = %+v, want unknown_output_field and required_output_missing", validation.Issues)
	}
}

func TestCompleteRunRejectsOversizedCompletionFields(t *testing.T) {
	ctx, store, _, started := startCompletionValidationWorkflow(t, createCompletionValidationWorkflow)
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": strings.Repeat("a", workflow.MaxOutputValueBytes+1)}})
	if !completionHasCode(err, CompletionCodeOutputTooLarge) {
		t.Fatalf("expected oversized output error, got %v", err)
	}
	_, err = store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", Commentary: strings.Repeat("a", workflow.MaxCommentaryBytes+1), OutputValues: map[string]string{"prior_summary": "done"}})
	if !completionHasCode(err, CompletionCodeCommentaryTooLarge) {
		t.Fatalf("expected oversized commentary error, got %v", err)
	}
}
