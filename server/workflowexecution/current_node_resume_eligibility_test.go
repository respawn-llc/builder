package workflowexecution

import (
	"context"
	"errors"
	"testing"

	"core/server/runtime"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestCurrentNodeControllerSessionOwnershipRequiresCurrentNodeBinding(t *testing.T) {
	taskID := workflow.TaskID("task-current-session-ownership")
	reference := currentNodeReferenceForControllerTest(t, string(taskID), "node-current")
	sessionID, err := runtimeids.ParseSessionID("550e8400-e29b-41d4-a716-446655440302")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	store := &currentNodeControllerStore{
		interrupted:   []workflow.CurrentNode{{Reference: reference}},
		sessionTaskID: &taskID,
		sessionAssociation: &workflowstore.TaskSessionAssociation{
			SessionID:   sessionID,
			CurrentNode: reference,
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

	owned, err := controller.SessionHasCurrentWorkflowTask(context.Background(), sessionID.String())
	if err != nil || !owned {
		t.Fatalf("current Workflow ownership = %t, %v, want true", owned, err)
	}
	store.mu.Lock()
	store.interrupted = nil
	store.mu.Unlock()
	owned, err = controller.SessionHasCurrentWorkflowTask(context.Background(), sessionID.String())
	if err != nil || owned {
		t.Fatalf("historical Workflow ownership = %t, %v, want false", owned, err)
	}
}

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

func TestCurrentNodeControllerRetainedResumeRejectsInvalidSelectedNodeBeforeSiblingMutation(t *testing.T) {
	taskID := workflow.TaskID("task-selected-resume-invalid")
	selected := currentNodeReferenceForControllerTest(t, string(taskID), "node-selected")
	sibling := currentNodeReferenceForControllerTest(t, string(taskID), "node-sibling")
	sessionID, err := runtimeids.ParseSessionID("550e8400-e29b-41d4-a716-446655440301")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{
			{Reference: selected},
			{Reference: sibling},
		},
		resumeClassifications: []workflowstore.CurrentNodeResumeClassification{
			{
				CurrentNode: workflow.CurrentNode{Reference: selected},
				Diagnostics: []workflowstore.CurrentNodeResumeValidationDiagnostic{{
					Code:           workflowstore.CurrentNodeResumeParameterNotMaterializedCode,
					CurrentNode:    selected,
					EnteringEdgeID: workflow.EdgeID("edge-selected"),
					ParameterKey:   "reviewer",
				}},
			},
			{CurrentNode: workflow.CurrentNode{Reference: sibling}},
		},
		sessionTaskID: &taskID,
		sessionAssociation: &workflowstore.TaskSessionAssociation{
			SessionID:   sessionID,
			CurrentNode: selected,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 2)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	continuation, err := NewWorkflowSessionContinuationFromInput(WorkflowSessionTextInput{Text: "continue"})
	if err != nil {
		t.Fatalf("NewWorkflowSessionContinuation: %v", err)
	}

	_, err = controller.ReactivateWorkflowSessionWithAcceptance(
		context.Background(),
		sessionID,
		func(commit func() (bool, error)) (bool, error) { return commit() },
		context.Background(),
		continuation,
	)
	var validationErr *workflowstore.CurrentNodeResumeValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("retained Resume error = %T %v, want selected validation error", err, err)
	}
	if len(store.resumed) != 0 {
		t.Fatalf("resumed Current Nodes = %v, want none before selected validation succeeds", store.resumed)
	}
}

func TestCurrentNodeControllerRetainedResumeAcceptanceRejectsBeforeSiblingMutation(t *testing.T) {
	tests := []struct {
		name       string
		acceptance runtime.CommandAcceptance
	}{
		{
			name: "not committed",
			acceptance: func(func() (bool, error)) (bool, error) {
				return false, nil
			},
		},
		{
			name: "returns error",
			acceptance: func(func() (bool, error)) (bool, error) {
				return false, errors.New("acceptance rejected")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			taskID := workflow.TaskID("task-selected-resume-acceptance")
			selected := currentNodeReferenceForControllerTest(t, string(taskID), "node-selected")
			sibling := currentNodeReferenceForControllerTest(t, string(taskID), "node-sibling")
			sessionID, err := runtimeids.ParseSessionID("550e8400-e29b-41d4-a716-446655440303")
			if err != nil {
				t.Fatalf("ParseSessionID: %v", err)
			}
			store := &currentNodeControllerStore{
				interrupted: []workflow.CurrentNode{
					{Reference: selected},
					{Reference: sibling},
				},
				resumeClassifications: []workflowstore.CurrentNodeResumeClassification{
					{CurrentNode: workflow.CurrentNode{Reference: selected}},
					{CurrentNode: workflow.CurrentNode{Reference: sibling}},
				},
				sessionTaskID: &taskID,
				sessionAssociation: &workflowstore.TaskSessionAssociation{
					SessionID:   sessionID,
					CurrentNode: selected,
				},
			}
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
			controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 2)
			t.Cleanup(func() {
				if err := controller.Close(); err != nil {
					t.Errorf("close controller: %v", err)
				}
				if err := authority.Close(context.Background()); err != nil {
					t.Errorf("close authority: %v", err)
				}
			})
			continuation, err := NewWorkflowSessionContinuationFromInput(WorkflowSessionTextInput{Text: "continue"})
			if err != nil {
				t.Fatalf("NewWorkflowSessionContinuation: %v", err)
			}

			_, err = controller.ReactivateWorkflowSessionWithAcceptance(
				context.Background(),
				sessionID,
				test.acceptance,
				context.Background(),
				continuation,
			)
			if err == nil {
				t.Fatal("retained Resume acceptance unexpectedly succeeded")
			}
			if len(store.resumed) != 0 {
				t.Fatalf("resumed Current Nodes = %v, want none after selected acceptance rejection", store.resumed)
			}
		})
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
