package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestTaskStartReplacesBacklogCurrentNodeWithFirstExecutableCurrentNode(t *testing.T) {
	type fixture struct {
		store      *Store
		ctx        context.Context
		task       TaskRecord
		workflowID runtimeids.WorkflowID
		targetID   workflow.NodeID
	}

	tests := []struct {
		name   string
		create func(*testing.T) fixture
	}{
		{
			name: "agent",
			create: func(t *testing.T) fixture {
				t.Helper()
				ctx, store, binding := newTestStoreContext(t)
				workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
				return fixture{
					store:      store,
					ctx:        ctx,
					task:       createDefaultTask(t, ctx, store, binding.ProjectID),
					workflowID: workflowID,
					targetID:   workflow.NodeID("node-agent-" + workflowID.String()),
				}
			},
		},
		{
			name: "script",
			create: func(t *testing.T) fixture {
				t.Helper()
				scripts := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
				return fixture{
					store:      scripts.store,
					ctx:        scripts.ctx,
					task:       scripts.task,
					workflowID: scripts.workflowID,
					targetID:   scripts.scriptID,
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.create(t)
			definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
			if err != nil {
				t.Fatalf("GetDefinition: %v", err)
			}
			start := nodeByKind(t, definition, workflow.NodeKindStart)
			backlog, err := workflow.NewCurrentNodeReference(fixture.task.ID, workflow.NodeIDOf(start), nil)
			if err != nil {
				t.Fatalf("NewCurrentNodeReference backlog: %v", err)
			}
			before, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.task.ID)
			if err != nil {
				t.Fatalf("ListCurrentNodes before start: %v", err)
			}
			if len(before) != 1 || !before[0].Reference.Equal(backlog) || before[0].Scheduling != nil || before[0].SessionID != nil {
				t.Fatalf("current nodes before start = %+v, want one unbound backlog node", before)
			}

			started, err := fixture.store.StartTask(fixture.ctx, fixture.task.ID)
			if err != nil {
				t.Fatalf("StartTask: %v", err)
			}
			target, err := workflow.NewCurrentNodeReference(fixture.task.ID, fixture.targetID, nil)
			if err != nil {
				t.Fatalf("NewCurrentNodeReference target: %v", err)
			}
			if len(started.Mutation.Removed) != 1 || !started.Mutation.Removed[0].Equal(backlog) {
				t.Fatalf("StartTask removed = %+v, want backlog current node", started.Mutation.Removed)
			}
			if len(started.Mutation.Created) != 1 ||
				!started.Mutation.Created[0].Reference.Equal(target) ||
				started.Mutation.Created[0].SessionID != nil ||
				started.Mutation.Created[0].Scheduling == nil ||
				started.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
				t.Fatalf("StartTask created = %+v, want one ready unbound target current node", started.Mutation.Created)
			}

			after, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.task.ID)
			if err != nil {
				t.Fatalf("ListCurrentNodes after start: %v", err)
			}
			if len(after) != 1 ||
				!after[0].Reference.Equal(target) ||
				after[0].SessionID != nil ||
				after[0].Scheduling == nil ||
				after[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
				t.Fatalf("current nodes after start = %+v, want one ready unbound target node", after)
			}
		})
	}
}

func TestAdmitCurrentNodeMovesReadyNodeToRestartMarker(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := store.AdmitCurrentNode(ctx, started.Mutation.Created[0].Reference); err != nil {
		t.Fatalf("AdmitCurrentNode: %v", err)
	}
	nodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Scheduling == nil || nodes[0].Scheduling.State != workflow.CurrentNodeSchedulingAdmitted {
		t.Fatalf("current nodes = %+v, want one admitted node in workflow %q", nodes, workflowID)
	}
	if err := store.AdmitCurrentNode(ctx, started.Mutation.Created[0].Reference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second AdmitCurrentNode error = %v, want stale-ready absence", err)
	}
}

func TestRecoverExecutableCurrentNodesInterruptsReadyAndAdmittedButPreservesApprovalSources(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	readyTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	ready := startTask(t, ctx, store, readyTask.ID).Mutation.Created[0]
	admittedTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	admitted := startTask(t, ctx, store, admittedTask.ID).Mutation.Created[0]
	if err := store.AdmitCurrentNode(ctx, admitted.Reference); err != nil {
		t.Fatalf("AdmitCurrentNode: %v", err)
	}

	approvalWorkflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, approvalWorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, approvalWorkflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID).RequiresApproval = true
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, approvalWorkflowID, false)
	approvalTask := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &approvalWorkflowID,
		Title:      "Approval task",
		Body:       "Preserve pending Approval",
	})
	approvalSource := startTask(t, ctx, store, approvalTask.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       approvalSource.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "preserve approval"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("completion did not create pending Approval")
	}

	reason := workflow.CurrentNodeInterruptionReason("workflow_startup_recovery")
	recovered, err := store.RecoverExecutableCurrentNodes(ctx, reason, workflow.CurrentNodeInterruptionDetail{Code: string(reason)})
	if err != nil {
		t.Fatalf("RecoverExecutableCurrentNodes: %v", err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered current nodes = %d, want ready and admitted nodes only", len(recovered))
	}
	for _, expected := range []workflow.CurrentNodeReference{ready.Reference, admitted.Reference} {
		nodes, err := store.ListCurrentNodes(ctx, expected.TaskID)
		if err != nil {
			t.Fatalf("ListCurrentNodes(%q): %v", expected.TaskID, err)
		}
		if len(nodes) != 1 ||
			nodes[0].Scheduling == nil ||
			nodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted ||
			nodes[0].Scheduling.Interruption == nil ||
			nodes[0].Scheduling.Interruption.Reason != reason {
			t.Fatalf("recovered current nodes for %q = %+v, want startup interruption", expected.TaskID, nodes)
		}
	}
	approvalNodes, err := store.ListCurrentNodes(ctx, approvalTask.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes approval task: %v", err)
	}
	if len(approvalNodes) != 1 ||
		!approvalNodes[0].Reference.Equal(approvalSource.Reference) ||
		approvalNodes[0].Scheduling == nil ||
		approvalNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("approval source after recovery = %+v, want frozen ready source", approvalNodes)
	}
}

func TestCurrentNodeStartContextDerivesContinuationFromOutgoingEdges(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		for index := range req.Edges {
			if req.Edges[index].TransitionGroupID == workflow.TransitionGroupID("group-done-"+workflowID.String()) {
				req.Edges[index].ContextMode = workflow.ContextModeContinueSession
			}
		}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	input, err := store.ResolveCurrentNodeStartContext(ctx, started.Mutation.Created[0].Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
	}
	if input.ContextMode != workflow.ContextModeNewSession {
		t.Fatalf("entering context mode = %q, want new_session", input.ContextMode)
	}
	if !input.HasContinueSessionOutgoingEdge {
		t.Fatal("current-node start context did not derive its continuation fact from outgoing edges")
	}
}

func TestResolveCurrentNodeStartContextAppliesPreviousTargetOrNewEffectiveMode(t *testing.T) {
	t.Run("missing prior target session starts new", func(t *testing.T) {
		fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTargetOrNew)
		target := completeReworkCurrentNodeForStartContextTest(t, fixture)

		start, err := fixture.store.ResolveCurrentNodeStartContext(fixture.ctx, target.Reference)
		if err != nil {
			t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
		}
		if start.EnteringEdge.ContextMode != workflow.ContextModeContinueSession ||
			workflow.CanonicalContextSource(start.EnteringEdge.ContextSource).Kind != workflow.ContextSourcePreviousTargetOrNew {
			t.Fatalf("configured entering context = %+v, want previous_target_or_new continuation", start.EnteringEdge)
		}
		if start.ContextMode != workflow.ContextModeNewSession || start.SourceSessionID != nil {
			t.Fatalf("effective start context = mode %q session %v, want new_session without source", start.ContextMode, start.SourceSessionID)
		}
	})

	t.Run("retained prior target session continues", func(t *testing.T) {
		fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTargetOrNew)
		sessionID := associateTaskSessionForTest(
			t,
			fixture.ctx,
			fixture.store,
			fixture.binding,
			fixture.cfg,
			fixture.review.Reference,
			time.UnixMilli(1_700_000_000_000).UTC(),
		)
		target := completeReworkCurrentNodeForStartContextTest(t, fixture)

		start, err := fixture.store.ResolveCurrentNodeStartContext(fixture.ctx, target.Reference)
		if err != nil {
			t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
		}
		if start.ContextMode != workflow.ContextModeContinueSession ||
			start.SourceSessionID == nil ||
			*start.SourceSessionID != sessionID {
			t.Fatalf("effective start context = mode %q session %v, want continuation from %q", start.ContextMode, start.SourceSessionID, sessionID)
		}
	})

	t.Run("other continuation source still requires retained session", func(t *testing.T) {
		fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTargetOrNew)
		target := completeReworkCurrentNodeForStartContextTest(t, fixture)
		saveWorkflowGraphFixture(t, fixture.ctx, fixture.store, fixture.workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
			edge := workflowGraphSaveEdgeRecord(t, req.Edges, *target.EnteredByEdgeID)
			edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}
		})

		if _, err := fixture.store.ResolveCurrentNodeStartContext(fixture.ctx, target.Reference); err == nil {
			t.Fatal("ResolveCurrentNodeStartContext accepted continuation without a retained session")
		}
	})
}

func completeReworkCurrentNodeForStartContextTest(t *testing.T, fixture reworkContextCompletionFixture) workflow.CurrentNode {
	t.Helper()
	result, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.audit.Reference,
		TransitionID: "rework",
		OutputValues: map[string]string{"summary": "review again"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode audit: %v", err)
	}
	if len(result.Mutation.Created) != 1 {
		t.Fatalf("CompleteCurrentNode created = %+v, want one rework target", result.Mutation.Created)
	}
	return result.Mutation.Created[0]
}
