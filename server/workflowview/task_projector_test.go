package workflowview

import (
	"database/sql"
	"reflect"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/serverapi"
)

func TestTaskProjectorPreservesFactsAcrossEndpointScenarios(t *testing.T) {
	projector := NewTaskProjector()
	definition := definitionSnapshot{
		api: serverapi.WorkflowDefinition{
			Workflow: serverapi.WorkflowRecord{ID: "workflow-1"},
			Nodes: []serverapi.WorkflowNode{
				{ID: "node-start", WorkflowID: "workflow-1", Key: "backlog", Kind: string(workflow.NodeKindStart), DisplayName: "Backlog"},
				{ID: "node-agent", WorkflowID: "workflow-1", Key: "agent", Kind: string(workflow.NodeKindAgent), DisplayName: "Agent"},
				{ID: "node-done", WorkflowID: "workflow-1", Key: "done", Kind: string(workflow.NodeKindTerminal), DisplayName: "Done"},
			},
		},
		nodeKinds: map[string]workflow.NodeKind{
			"node-start": workflow.NodeKindStart,
			"node-agent": workflow.NodeKindAgent,
			"node-done":  workflow.NodeKindTerminal,
		},
	}
	task := sqlitegen.TaskRecord{
		ID:              "task-1",
		ProjectID:       "project-1",
		WorkflowID:      "workflow-1",
		ShortID:         "WOR-1",
		Title:           "Task",
		Body:            "Body",
		CreatedAtUnixMs: 1,
		UpdatedAtUnixMs: 2,
	}
	activePlacement := func(id string, nodeID string, state string) sqlitegen.TaskNodePlacementRecord {
		return sqlitegen.TaskNodePlacementRecord{
			ID:     id,
			TaskID: task.ID,
			NodeID: sql.NullString{String: nodeID, Valid: true},
			State:  state,
		}
	}
	for _, test := range []struct {
		name                string
		status              serverapi.WorkflowTaskStatusKind
		done                bool
		mutateTask          func(*sqlitegen.TaskRecord)
		placements          []sqlitegen.TaskNodePlacementRecord
		runActions          taskRunActionFacts
		wantCanStart        bool
		wantCanInterrupt    bool
		wantCanResume       bool
		wantCanCancel       bool
		wantEffectiveNodeID string
	}{
		{name: "backlog", status: serverapi.WorkflowTaskStatusKindBacklog, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-start", "node-start", "active")}, wantCanStart: true, wantCanCancel: true, wantEffectiveNodeID: "node-start"},
		{name: "active", status: serverapi.WorkflowTaskStatusKindActive, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-agent", "node-agent", "active")}, wantCanCancel: true, wantEffectiveNodeID: "node-agent"},
		{name: "queued", status: serverapi.WorkflowTaskStatusKindQueued, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-agent", "node-agent", "active")}, wantCanCancel: true, wantEffectiveNodeID: "node-agent"},
		{name: "running", status: serverapi.WorkflowTaskStatusKindRunning, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-agent", "node-agent", "active")}, runActions: taskRunActionFacts{HasRunning: true}, wantCanInterrupt: true, wantCanCancel: true, wantEffectiveNodeID: "node-agent"},
		{name: "interrupted", status: serverapi.WorkflowTaskStatusKindInterrupted, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-agent", "node-agent", "active")}, runActions: taskRunActionFacts{HasInterrupted: true}, wantCanResume: true, wantCanCancel: true, wantEffectiveNodeID: "node-agent"},
		{name: "waiting question", status: serverapi.WorkflowTaskStatusKindWaitingQuestion, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-agent", "node-agent", "active")}, runActions: taskRunActionFacts{HasWaitingQuestion: true}, wantCanCancel: true, wantEffectiveNodeID: "node-agent"},
		{name: "parallel running and waiting question", status: serverapi.WorkflowTaskStatusKindWaitingQuestion, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-agent", "node-agent", "active")}, runActions: taskRunActionFacts{HasRunning: true, HasWaitingQuestion: true}, wantCanInterrupt: true, wantCanCancel: true, wantEffectiveNodeID: "node-agent"},
		{name: "waiting approval", status: serverapi.WorkflowTaskStatusKindWaitingApproval, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-agent", "node-agent", "waiting_approval")}, wantCanCancel: true, wantEffectiveNodeID: "node-agent"},
		{name: "done", status: serverapi.WorkflowTaskStatusKindDone, done: true, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-done", "node-done", "active")}, wantEffectiveNodeID: "node-done"},
		{name: "canceled", status: serverapi.WorkflowTaskStatusKindCanceled, done: true, mutateTask: func(task *sqlitegen.TaskRecord) {
			task.CanceledAtUnixMs = sql.NullInt64{Int64: 5, Valid: true}
		}, placements: []sqlitegen.TaskNodePlacementRecord{activePlacement("placement-start", "node-start", "active")}, wantEffectiveNodeID: "node-done"},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputTask := task
			if test.mutateTask != nil {
				test.mutateTask(&inputTask)
			}
			status, err := projector.DecodeStatus(TaskStatusInput{
				TaskID:             task.ID,
				Kind:               string(test.status),
				NodeIDsJSON:        `["` + test.wantEffectiveNodeID + `"]`,
				RunIDsJSON:         `[]`,
				AttentionTypesJSON: `[]`,
				Done:               test.done,
			})
			if err != nil {
				t.Fatalf("DecodeStatus: %v", err)
			}
			input := TaskFactsInput{Task: inputTask, Status: status, Placements: test.placements, RunActions: test.runActions, Definition: definition}
			var projected []TaskFacts
			for _, scenario := range []string{"detail", "board", "task_list"} {
				t.Run(scenario, func(t *testing.T) {
					projected = append(projected, projector.ProjectTaskFacts(input))
				})
			}
			if !reflect.DeepEqual(projected[0], projected[1]) || !reflect.DeepEqual(projected[1], projected[2]) {
				t.Fatalf("endpoint facts differ: %+v", projected)
			}
			facts := projected[0]
			if facts.Status.Kind != test.status || facts.Done != test.done || facts.Summary.Done != test.done {
				t.Fatalf("facts = %+v, want status %q done=%t", facts, test.status, test.done)
			}
			if facts.Actions.CanStart != test.wantCanStart ||
				facts.Actions.CanInterrupt != test.wantCanInterrupt ||
				facts.Actions.CanResume != test.wantCanResume ||
				facts.Actions.CanCancel != test.wantCanCancel {
				t.Fatalf("actions = %+v", facts.Actions)
			}
			if len(facts.EffectivePlacements) != 1 {
				t.Fatalf("effective placements = %+v", facts.EffectivePlacements)
			}
			nodeID, ok := taskNodePlacementNodeID(facts.EffectivePlacements[0])
			if !ok || nodeID != test.wantEffectiveNodeID {
				t.Fatalf("effective placement = %+v, want node %q", facts.EffectivePlacements[0], test.wantEffectiveNodeID)
			}
		})
	}
}

func TestTaskProjectorDecodesAttentionKindsAndDTOs(t *testing.T) {
	projector := NewTaskProjector()
	status, err := projector.DecodeStatus(TaskStatusInput{
		TaskID:             "task-1",
		Kind:               string(serverapi.WorkflowTaskStatusKindWaitingQuestion),
		NodeIDsJSON:        `["node-agent"]`,
		RunIDsJSON:         `["run-1"]`,
		AttentionTypesJSON: `["approval","question"]`,
	})
	if err != nil {
		t.Fatalf("DecodeStatus: %v", err)
	}
	if !reflect.DeepEqual(status.Status.AttentionTypes, []serverapi.WorkflowTaskAttentionKind{
		serverapi.WorkflowTaskAttentionKindApproval,
		serverapi.WorkflowTaskAttentionKindQuestion,
	}) {
		t.Fatalf("attention types = %+v", status.Status.AttentionTypes)
	}

	nodes := map[string]serverapi.WorkflowNode{
		"node-agent": {ID: "node-agent", Key: "agent", Kind: string(workflow.NodeKindAgent), DisplayName: "Agent", SubagentRole: "coder"},
	}
	placement := projector.ProjectPlacement(PlacementProjectionInput{
		Placement: sqlitegen.TaskNodePlacementRecord{
			ID:     "placement-1",
			TaskID: "task-1",
			NodeID: sql.NullString{String: "node-agent", Valid: true},
			State:  "active",
		},
		Nodes: nodes,
	})
	if placement.NodeKey != "agent" || placement.NodeDisplayName != "Agent" || placement.NodeKind != string(workflow.NodeKindAgent) {
		t.Fatalf("placement = %+v", placement)
	}
	run := projector.ProjectRun(RunProjectionInput{
		Run: sqlitegen.TaskRunRecord{
			ID:              "run-1",
			TaskID:          "task-1",
			PlacementID:     "placement-1",
			NodeID:          sql.NullString{String: "node-agent", Valid: true},
			SessionID:       sql.NullString{String: "session-1", Valid: true},
			StartedAtUnixMs: sql.NullInt64{Int64: 2, Valid: true},
		},
		Nodes:        nodes,
		SessionNames: map[string]string{"session-1": "Session"},
	})
	if run.Status != "running" || run.Role != "coder" || run.SessionName != "Session" {
		t.Fatalf("run = %+v", run)
	}
	transition, err := projector.ProjectTransition(TransitionProjectionInput{
		Transition: sqlitegen.TaskTransitionRecord{
			ID:                    "transition-1",
			TaskID:                "task-1",
			TransitionID:          "done",
			TransitionDisplayName: "Done",
			WorkflowRevisionSeen:  1,
			Actor:                 "agent",
			State:                 "applied",
			OutputValuesJson:      `{"summary":"complete"}`,
			CreatedAtUnixMs:       3,
		},
		Edges: []sqlitegen.TaskTransitionEdgeRecord{{
			ID:                     "edge-1",
			TaskTransitionID:       "transition-1",
			EdgeKey:                "done",
			State:                  "applied",
			ContextMode:            string(workflow.ContextModeNewSession),
			InputBindingsJson:      `[]`,
			OutputRequirementsJson: `[]`,
			WorkflowRevisionSeen:   1,
		}},
	})
	if err != nil {
		t.Fatalf("ProjectTransition: %v", err)
	}
	if transition.OutputValues["summary"] != "complete" || len(transition.Edges) != 1 {
		t.Fatalf("transition = %+v", transition)
	}
	comment := projector.ProjectComment(sqlitegen.TaskComment{
		ID:              "comment-1",
		TaskID:          "task-1",
		Body:            "Note",
		AuthorKind:      "user",
		AuthorID:        "nek",
		CreatedAtUnixMs: 4,
		UpdatedAtUnixMs: 5,
	})
	if comment.Body != "Note" || comment.Author != "user" || comment.AuthorID != "nek" {
		t.Fatalf("comment = %+v", comment)
	}
}
