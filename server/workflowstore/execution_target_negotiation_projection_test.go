package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
)

func TestTaskExecutionTargetNegotiationRoundTripsFencesAndClearsOnCancellation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Negotiated task",
		Body:       "Body",
	})
	startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetActiveStartPlacementForTask: %v", err)
	}
	namedRef := "refs/heads/main"
	commit := "01cafe"
	startPlacementID := workflow.PlacementID(startPlacement.ID)
	negotiation := workflow.ExecutionTargetNegotiation{
		TaskID:            task.ID,
		Generation:        "target-negotiation-1",
		WorkflowID:        workflowID,
		SourceWorkspaceID: binding.WorkspaceID,
		Source: workflow.ExecutionTargetNegotiationSource{
			Kind:     workflow.ExecutionTargetNegotiationSourceNamedRef,
			NamedRef: &namedRef,
			Commit:   &commit,
		},
		Action: workflow.ExecutionTargetNegotiationAction{
			Kind:             workflow.ExecutionTargetNegotiationActionStart,
			StartPlacementID: &startPlacementID,
		},
	}
	if err := store.SaveTaskExecutionTargetNegotiation(ctx, negotiation); err != nil {
		t.Fatalf("SaveTaskExecutionTargetNegotiation: %v", err)
	}

	actual, err := store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if actual == nil ||
		actual.Generation != negotiation.Generation ||
		actual.Source.Kind != workflow.ExecutionTargetNegotiationSourceNamedRef ||
		actual.Source.NamedRef == nil ||
		*actual.Source.NamedRef != namedRef ||
		actual.Source.Commit == nil ||
		*actual.Source.Commit != commit ||
		actual.Action.Kind != workflow.ExecutionTargetNegotiationActionStart ||
		actual.Action.StartPlacementID == nil ||
		*actual.Action.StartPlacementID != startPlacementID {
		t.Fatalf("execution target negotiation = %+v, want durable start fence", actual)
	}
	if err := store.ValidateTaskExecutionTargetNegotiation(ctx, negotiation); err != nil {
		t.Fatalf("ValidateTaskExecutionTargetNegotiation: %v", err)
	}

	stale := negotiation
	stale.Source.Commit = stringPointer("02cafe")
	if err := store.ValidateTaskExecutionTargetNegotiation(ctx, stale); !errors.Is(err, ErrTaskExecutionTargetNegotiationChanged) {
		t.Fatalf("ValidateTaskExecutionTargetNegotiation stale error = %v, want %v", err, ErrTaskExecutionTargetNegotiationChanged)
	}
	assertTaskExecutionTargetInsertRejected(t, ctx, store, map[string]any{
		"task_id":              string(task.ID),
		"policy":               "none",
		"state":                "locked",
		"setup_state":          "not_applicable",
		"recovery_disposition": "available",
	})
	if err := store.ClearTaskExecutionTargetNegotiation(ctx, task.ID); err != nil {
		t.Fatalf("ClearTaskExecutionTargetNegotiation: %v", err)
	}
	insertTaskExecutionTarget(t, ctx, store, task.ID, map[string]any{
		"policy":               "none",
		"state":                "locked",
		"setup_state":          "not_applicable",
		"recovery_disposition": "available",
	})

	cancelledTask := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Cancelled negotiation",
		Body:       "Body",
	})
	cancelledStartPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(cancelledTask.ID))
	if err != nil {
		t.Fatalf("GetActiveStartPlacementForTask cancelled: %v", err)
	}
	cancelledNegotiation := negotiation
	cancelledNegotiation.TaskID = cancelledTask.ID
	cancelledNegotiation.Generation = "target-negotiation-cancelled"
	cancelledNegotiation.Action.StartPlacementID = placementPointer(workflow.PlacementID(cancelledStartPlacement.ID))
	if err := store.SaveTaskExecutionTargetNegotiation(ctx, cancelledNegotiation); err != nil {
		t.Fatalf("SaveTaskExecutionTargetNegotiation cancelled: %v", err)
	}
	if err := store.CancelTask(ctx, cancelledTask.ID, "cancelled"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	actual, err = store.GetTaskExecutionTargetNegotiation(ctx, cancelledTask.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after cancellation: %v", err)
	}
	if actual != nil {
		t.Fatalf("execution target negotiation after cancellation = %+v, want nil", actual)
	}
}

func TestTaskExecutionTargetNegotiationConditionalSaveFencesExpectedState(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowID,
		Title:      "Conditional negotiation",
		Body:       "Body",
	})
	startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetActiveStartPlacementForTask: %v", err)
	}
	negotiation := workflow.ExecutionTargetNegotiation{
		TaskID:            task.ID,
		Generation:        "generation-1",
		WorkflowID:        workflowID,
		SourceWorkspaceID: binding.WorkspaceID,
		Source: workflow.ExecutionTargetNegotiationSource{
			Kind: workflow.ExecutionTargetNegotiationSourceNonGit,
		},
		Action: workflow.ExecutionTargetNegotiationAction{
			Kind:             workflow.ExecutionTargetNegotiationActionStart,
			StartPlacementID: placementPointer(workflow.PlacementID(startPlacement.ID)),
		},
	}
	if err := store.SaveTaskExecutionTargetNegotiationIfExpected(ctx, nil, negotiation); err != nil {
		t.Fatalf("SaveTaskExecutionTargetNegotiationIfExpected absent: %v", err)
	}
	actual, err := store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if actual == nil || actual.Generation != negotiation.Generation {
		t.Fatalf("stored negotiation = %+v, want generation %q", actual, negotiation.Generation)
	}

	replacement := negotiation
	replacement.Generation = "generation-2"
	if err := store.SaveTaskExecutionTargetNegotiationIfExpected(ctx, nil, replacement); !errors.Is(err, ErrTaskExecutionTargetNegotiationChanged) {
		t.Fatalf("SaveTaskExecutionTargetNegotiationIfExpected stale absence error = %v, want %v", err, ErrTaskExecutionTargetNegotiationChanged)
	}
	actual, err = store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after stale absence: %v", err)
	}
	if actual == nil || actual.Generation != negotiation.Generation {
		t.Fatalf("stored negotiation after stale absence = %+v, want original", actual)
	}

	if err := store.SaveTaskExecutionTargetNegotiationIfExpected(ctx, &negotiation, replacement); err != nil {
		t.Fatalf("SaveTaskExecutionTargetNegotiationIfExpected replacement: %v", err)
	}
	actual, err = store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after replacement: %v", err)
	}
	if actual == nil || actual.Generation != replacement.Generation {
		t.Fatalf("stored negotiation after replacement = %+v, want generation %q", actual, replacement.Generation)
	}
	if err := store.SaveTaskExecutionTargetNegotiationIfExpected(ctx, &negotiation, negotiation); !errors.Is(err, ErrTaskExecutionTargetNegotiationChanged) {
		t.Fatalf("SaveTaskExecutionTargetNegotiationIfExpected stale replacement error = %v, want %v", err, ErrTaskExecutionTargetNegotiationChanged)
	}
}

func TestTaskExecutionTargetNegotiationBlocksLegacyInitiatingMutations(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	t.Run("start", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Negotiated start",
			Body:       "Body",
		})
		startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
		if err != nil {
			t.Fatalf("GetActiveStartPlacementForTask: %v", err)
		}
		saveTaskExecutionTargetNegotiation(t, ctx, store, workflow.ExecutionTargetNegotiation{
			TaskID:            task.ID,
			Generation:        "start-guard",
			WorkflowID:        workflowID,
			SourceWorkspaceID: binding.WorkspaceID,
			Source: workflow.ExecutionTargetNegotiationSource{
				Kind: workflow.ExecutionTargetNegotiationSourceNonGit,
			},
			Action: workflow.ExecutionTargetNegotiationAction{
				Kind:             workflow.ExecutionTargetNegotiationActionStart,
				StartPlacementID: placementPointer(workflow.PlacementID(startPlacement.ID)),
			},
		})

		if _, err := store.StartTask(ctx, task.ID); !errors.Is(err, ErrTaskExecutionTargetNegotiationInProgress) {
			t.Fatalf("StartTask error = %v, want %v", err, ErrTaskExecutionTargetNegotiationInProgress)
		}
		assertTaskInitiatingState(t, ctx, store, task.ID, 1, 0, 0)
	})

	t.Run("executable manual move", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Negotiated manual move",
			Body:       "Body",
		})
		started := startTask(t, ctx, store, task.ID)
		completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		agent := nodeByKey(t, definition, "agent")
		_, err = store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
		if err == nil {
			t.Fatal("GetActiveStartPlacementForTask unexpectedly found a start placement")
		}
		placements, err := store.ListPlacements(ctx, task.ID)
		if err != nil {
			t.Fatalf("ListPlacements: %v", err)
		}
		activePlacement := placements[len(placements)-1]
		if activePlacement.State != "completed" {
			t.Fatalf("terminal placement = %+v, want completed", activePlacement)
		}
		saveTaskExecutionTargetNegotiation(t, ctx, store, workflow.ExecutionTargetNegotiation{
			TaskID:            task.ID,
			Generation:        "move-guard",
			WorkflowID:        workflowID,
			SourceWorkspaceID: binding.WorkspaceID,
			Source: workflow.ExecutionTargetNegotiationSource{
				Kind: workflow.ExecutionTargetNegotiationSourceNonGit,
			},
			Action: workflow.ExecutionTargetNegotiationAction{
				Kind:                  workflow.ExecutionTargetNegotiationActionManualMove,
				MoveSourcePlacementID: placementPointer(activePlacement.ID),
				MoveTargetNodeID:      nodePointer(workflow.NodeIDOf(agent)),
			},
		})

		if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(agent)}); !errors.Is(err, ErrTaskExecutionTargetNegotiationInProgress) {
			t.Fatalf("ManualMoveTask error = %v, want %v", err, ErrTaskExecutionTargetNegotiationInProgress)
		}
		assertTaskInitiatingState(t, ctx, store, task.ID, 3, 2, 1)
	})

	t.Run("executable approval", func(t *testing.T) {
		approvalWorkflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
		requireApprovalOnWorkflowEdge(t, ctx, store, approvalWorkflowID, "next")
		linkWorkflow(t, ctx, store, binding.ProjectID, approvalWorkflowID, false)
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: approvalWorkflowID,
			Title:      "Negotiated approval",
			Body:       "Body",
		})
		started := startTask(t, ctx, store, task.ID)
		pending := completeRun(t, ctx, store, CompleteRunRequest{
			RunID:        started.RunID,
			TransitionID: "next",
			OutputValues: map[string]string{"prior_summary": "plan complete"},
		})
		if pending.State != "pending_approval" {
			t.Fatalf("pending completion = %+v, want pending approval", pending)
		}
		saveTaskExecutionTargetNegotiation(t, ctx, store, workflow.ExecutionTargetNegotiation{
			TaskID:            task.ID,
			Generation:        "approval-guard",
			WorkflowID:        approvalWorkflowID,
			SourceWorkspaceID: binding.WorkspaceID,
			Source: workflow.ExecutionTargetNegotiationSource{
				Kind: workflow.ExecutionTargetNegotiationSourceNonGit,
			},
			Action: workflow.ExecutionTargetNegotiationAction{
				Kind:                 workflow.ExecutionTargetNegotiationActionApproval,
				ApprovalTransitionID: transitionPointer(pending.TransitionID),
			},
		})

		if _, err := store.ApproveTransition(ctx, pending.TransitionID); !errors.Is(err, ErrTaskExecutionTargetNegotiationInProgress) {
			t.Fatalf("ApproveTransition error = %v, want %v", err, ErrTaskExecutionTargetNegotiationInProgress)
		}
		assertTaskInitiatingState(t, ctx, store, task.ID, 2, 2, 1)
		transitions, err := store.ListTransitions(ctx, task.ID)
		if err != nil {
			t.Fatalf("ListTransitions: %v", err)
		}
		if transitions[len(transitions)-1].State != "pending_approval" {
			t.Fatalf("approval transition after guard = %+v, want pending approval", transitions[len(transitions)-1])
		}
	})
}

func TestTaskExecutionTargetNegotiationCascadesOnTaskAndWorkflowDeletion(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	t.Run("task deletion", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Delete negotiated task",
			Body:       "Body",
		})
		startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
		if err != nil {
			t.Fatalf("GetActiveStartPlacementForTask: %v", err)
		}
		saveTaskExecutionTargetNegotiation(t, ctx, store, workflow.ExecutionTargetNegotiation{
			TaskID:            task.ID,
			Generation:        "task-delete",
			WorkflowID:        workflowID,
			SourceWorkspaceID: binding.WorkspaceID,
			Source: workflow.ExecutionTargetNegotiationSource{
				Kind: workflow.ExecutionTargetNegotiationSourceNonGit,
			},
			Action: workflow.ExecutionTargetNegotiationAction{
				Kind:             workflow.ExecutionTargetNegotiationActionStart,
				StartPlacementID: placementPointer(workflow.PlacementID(startPlacement.ID)),
			},
		})

		if _, err := store.DeleteTask(ctx, task.ID); err != nil {
			t.Fatalf("DeleteTask: %v", err)
		}
		negotiation, err := store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
		}
		if negotiation != nil {
			t.Fatalf("negotiation after task deletion = %+v, want nil", negotiation)
		}
	})

	t.Run("workflow deletion", func(t *testing.T) {
		workflowID := createValidWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, false)
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Delete negotiated workflow",
			Body:       "Body",
		})
		startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
		if err != nil {
			t.Fatalf("GetActiveStartPlacementForTask: %v", err)
		}
		saveTaskExecutionTargetNegotiation(t, ctx, store, workflow.ExecutionTargetNegotiation{
			TaskID:            task.ID,
			Generation:        "workflow-delete",
			WorkflowID:        workflowID,
			SourceWorkspaceID: binding.WorkspaceID,
			Source: workflow.ExecutionTargetNegotiationSource{
				Kind: workflow.ExecutionTargetNegotiationSourceNonGit,
			},
			Action: workflow.ExecutionTargetNegotiationAction{
				Kind:             workflow.ExecutionTargetNegotiationActionStart,
				StartPlacementID: placementPointer(workflow.PlacementID(startPlacement.ID)),
			},
		})
		impact, err := store.PreviewWorkflowDelete(ctx, workflowID)
		if err != nil {
			t.Fatalf("PreviewWorkflowDelete: %v", err)
		}

		deleted, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(impact))
		if err != nil {
			t.Fatalf("DeleteWorkflow: %v", err)
		}
		if !deleted.Deleted {
			t.Fatalf("DeleteWorkflow result = %+v, want deleted", deleted)
		}
		negotiation, err := store.GetTaskExecutionTargetNegotiation(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
		}
		if negotiation != nil {
			t.Fatalf("negotiation after workflow deletion = %+v, want nil", negotiation)
		}
	})
}

func saveTaskExecutionTargetNegotiation(t *testing.T, ctx context.Context, store *Store, negotiation workflow.ExecutionTargetNegotiation) {
	t.Helper()
	if err := store.SaveTaskExecutionTargetNegotiation(ctx, negotiation); err != nil {
		t.Fatalf("SaveTaskExecutionTargetNegotiation: %v", err)
	}
}

func assertTaskInitiatingState(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, wantPlacements, wantTransitions, wantRuns int) {
	t.Helper()
	placements, err := store.ListPlacements(ctx, taskID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != wantPlacements {
		t.Fatalf("placements = %+v, want %d", placements, wantPlacements)
	}
	transitions, err := store.ListTransitions(ctx, taskID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != wantTransitions {
		t.Fatalf("transitions = %+v, want %d", transitions, wantTransitions)
	}
	runs, err := store.ListRuns(ctx, taskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != wantRuns {
		t.Fatalf("runs = %+v, want %d", runs, wantRuns)
	}
}

func stringPointer(value string) *string {
	return &value
}

func placementPointer(value workflow.PlacementID) *workflow.PlacementID {
	return &value
}

func nodePointer(value workflow.NodeID) *workflow.NodeID {
	return &value
}

func transitionPointer(value workflow.TransitionID) *workflow.TransitionID {
	return &value
}
