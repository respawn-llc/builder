package workflowstore

import (
	"errors"
	"strings"
	"testing"

	"core/server/workflow"
)

func TestStartTaskAllowsCrossRoleContinueSessionContextMode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "reviewer")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)

	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask invalid workflow backlog: %v", err)
	}
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
}

func TestCompleteRunValidatesOutputRequirements(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
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
	if len(runs) != 1 || runs[0].CompletedAt != 0 || runs[0].InterruptedAt != 0 {
		t.Fatalf("rejected completion mutated run outcome: %+v", runs)
	}
}

func TestCompleteRunInfersSingleTransitionID(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID})
}

func TestCompleteRunRejectsMissingTransitionIDWhenAmbiguous(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-blocked-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(agent), TransitionID: "blocked", DisplayName: "Blocked"}); err != nil {
		t.Fatalf("AddTransitionGroup blocked: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-blocked-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-blocked-" + string(workflowID)), Key: "blocked", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge blocked: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID}); !completionHasCode(err, CompletionCodeTransitionIDRequired) {
		t.Fatalf("expected missing transition id error, got %v", err)
	}
}

func TestCompleteRunRejectsUnknownOutputField(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", OutputValues: map[string]string{"extra": "nope"}}); !completionHasCode(err, CompletionCodeUnknownOutputField) {
		t.Fatalf("expected unknown output error, got %v", err)
	}
}

func TestCompleteRunRejectsParameterDeclaredOnlyByAnotherTransition(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	blockedGroup := workflow.TransitionGroupID("group-blocked-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: blockedGroup, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(agent), TransitionID: "blocked", DisplayName: "Blocked"}); err != nil {
		t.Fatalf("AddTransitionGroup blocked: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-blocked-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: blockedGroup,
		Key:               "blocked",
		TargetNodeID:      workflow.NodeIDOf(done),
		ContextMode:       workflow.ContextModeNewSession,
		Parameters:        []workflow.Parameter{{Key: "blocked_reason", Description: "Why the task is blocked."}},
	}); err != nil {
		t.Fatalf("AddEdge blocked: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", OutputValues: map[string]string{"blocked_reason": "blocked"}}); !completionHasCode(err, CompletionCodeUnknownOutputField) {
		t.Fatalf("expected selected-transition unknown output error, got %v", err)
	}
}

func TestCompleteRunReturnsStructuredValidationIssues(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
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
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": strings.Repeat("a", workflow.MaxOutputValueBytes+1)}})
	if !completionHasCode(err, CompletionCodeOutputTooLarge) {
		t.Fatalf("expected oversized output error, got %v", err)
	}
	_, err = store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", Commentary: strings.Repeat("a", workflow.MaxCommentaryBytes+1), OutputValues: map[string]string{"prior_summary": "done"}})
	if !completionHasCode(err, CompletionCodeCommentaryTooLarge) {
		t.Fatalf("expected oversized commentary error, got %v", err)
	}
}
