package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"core/server/workflow"
)

func TestTaskStartReplacesBacklogCurrentNodeWithFirstExecutableCurrentNode(t *testing.T) {
	type fixture struct {
		store      *Store
		ctx        context.Context
		task       TaskRecord
		workflowID workflow.WorkflowID
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
					targetID:   workflow.NodeID("node-agent-" + string(workflowID)),
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
