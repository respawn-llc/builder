package workflowexecution

import (
	"context"
	"errors"
	"testing"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
)

func TestCurrentNodeControllerResumeEligibilityRejectsTaskWithoutInterruptedExecutableCurrentNodes(t *testing.T) {
	taskID := workflow.TaskID("task-resume-eligibility-empty")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	_, err := controller.PreflightTaskResume(context.Background(), taskID)

	var conflict *TaskResumeConflictError
	if !errors.As(err, &conflict) || conflict.TaskID != string(taskID) {
		t.Fatalf("PreflightTaskResume error = %T %v, want conflict for %q", err, err, taskID)
	}
	if len(store.resumed) != 0 {
		t.Fatalf("resume mutations = %v, want none", store.resumed)
	}
}

func TestCurrentNodeControllerResumeEligibilityReturnsAllInvalidClassificationErrors(t *testing.T) {
	taskID := workflow.TaskID("task-resume-eligibility-invalid")
	reference := currentNodeReferenceForControllerTest(t, string(taskID), "node-review")
	store := &currentNodeControllerStore{
		resumeClassifications: []workflowstore.CurrentNodeResumeClassification{{
			CurrentNode: workflow.CurrentNode{Reference: reference},
			Diagnostics: []workflowstore.CurrentNodeResumeValidationDiagnostic{{
				Code:           workflowstore.CurrentNodeResumeParameterNotMaterializedCode,
				CurrentNode:    reference,
				EnteringEdgeID: workflow.EdgeID("edge-review"),
				ParameterKey:   "reviewer",
			}},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	_, err := controller.PreflightTaskResume(context.Background(), taskID)

	var validationErr *workflowstore.CurrentNodeResumeValidationError
	if !errors.As(err, &validationErr) || len(validationErr.Diagnostics) != 1 {
		t.Fatalf("PreflightTaskResume error = %T %v, want typed validation error", err, err)
	}
	if len(store.resumed) != 0 {
		t.Fatalf("resume mutations = %v, want none", store.resumed)
	}
}

func TestCurrentNodeControllerResumeEligibilityAcceptsMixedValidAndInvalidClassifications(t *testing.T) {
	taskID := workflow.TaskID("task-resume-eligibility-mixed")
	invalidReference := currentNodeReferenceForControllerTest(t, string(taskID), "node-invalid")
	validReference := currentNodeReferenceForControllerTest(t, string(taskID), "node-valid")
	store := &currentNodeControllerStore{
		resumeClassifications: []workflowstore.CurrentNodeResumeClassification{
			{
				CurrentNode: workflow.CurrentNode{Reference: invalidReference},
				Diagnostics: []workflowstore.CurrentNodeResumeValidationDiagnostic{{
					Code:           workflowstore.CurrentNodeResumeParameterNotMaterializedCode,
					CurrentNode:    invalidReference,
					EnteringEdgeID: workflow.EdgeID("edge-invalid"),
					ParameterKey:   "reviewer",
				}},
			},
			{CurrentNode: workflow.CurrentNode{Reference: validReference}},
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	preflight, err := controller.PreflightTaskResume(context.Background(), taskID)
	if err != nil {
		t.Fatalf("PreflightTaskResume: %v", err)
	}
	if preflight.Outcome != TaskResumePreflightResumable ||
		len(preflight.CurrentNodes) != 1 ||
		!preflight.CurrentNodes[0].Reference.Equal(validReference) {
		t.Fatalf("PreflightTaskResume result = %+v, want resumable %v", preflight, validReference)
	}
	if len(store.resumed) != 0 {
		t.Fatalf("resume mutations = %v, want none", store.resumed)
	}
}

func TestCurrentNodeControllerResumeEligibilityRejectsUnavailableTaskBeforeStorePreflight(t *testing.T) {
	taskID := workflow.TaskID("task-resume-eligibility-unavailable")
	reference := currentNodeReferenceForControllerTest(t, string(taskID), "node-review")
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	controller.mu.Lock()
	fence, err := controller.interrupts.beginTask(taskID)
	if err != nil {
		controller.mu.Unlock()
		t.Fatalf("begin Task interrupt fence: %v", err)
	}
	controller.interrupts.addCurrentNode(fence, key)
	controller.mu.Unlock()

	_, err = controller.PreflightTaskResume(context.Background(), taskID)

	if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("PreflightTaskResume error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	if store.preflightResumeCalls != 0 {
		t.Fatalf("store preflight calls = %d, want none", store.preflightResumeCalls)
	}
	if len(store.resumed) != 0 {
		t.Fatalf("resume mutations = %v, want none", store.resumed)
	}
}
