package workflowstore

import (
	"context"
	"database/sql"
	"testing"

	"core/server/workflow"
)

func TestWorkflowGraphSaveRejectsChangedConfirmationImpact(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if !preview.ConfirmationRequired || workflowGraphSaveBlockerCount(preview.Blockers, "confirmation_required") != 2 {
		t.Fatalf("preview graph save = %+v, want confirmation for removed edge and transition group", preview)
	}

	confirmed := confirmWorkflowGraphSaveRequest(req, preview.Impact)
	confirmed.ExpectedRemovedEdgeCount++
	result, err := store.SaveWorkflowGraph(ctx, confirmed)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph changed impact: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "impact_changed") != 1 {
		t.Fatalf("changed-impact graph save = %+v, want impact_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after changed-impact save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after changed-impact save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveBlocksActiveWorkButAllowsBacklogAndTerminalTasks(t *testing.T) {
	t.Run("backlog only task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Backlog", Body: "Body"})
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph backlog-only: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("backlog-only graph save = %+v, want saved without active-work blockers", result)
		}
	})

	t.Run("active task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Active", Body: "Body"})
		startTask(t, ctx, store, task.ID)
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph active task: %v", err)
		}
		if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "active_node_placements") == 0 {
			t.Fatalf("active-task graph save = %+v, want active_node_placements blocker", result)
		}
	})

	t.Run("terminal only task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Terminal", Body: "Body"})
		started := startTask(t, ctx, store, task.ID)
		completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph terminal-only: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("terminal-only graph save = %+v, want saved without active-work blockers", result)
		}
	})
}

func TestWorkflowGraphSaveEditPolicyBlocksStartNodeChanges(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, start.ID, workflow.NodeKindAgent)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph start kind change: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "start_node_changed") != 1 {
		t.Fatalf("start-node graph save = %+v, want start_node_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked start-node save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked start-node save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveEditPolicyBlocksLastTerminalRemovalOrKindChange(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, done.ID, workflow.NodeKindAgent)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph terminal kind change: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "last_terminal_changed") != 1 {
		t.Fatalf("last-terminal graph save = %+v, want last_terminal_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked last-terminal save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked last-terminal save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveEditPolicyBlocksTaskReferencedNodeKindChange(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, agentID, workflow.NodeKindJoin)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph referenced node kind change: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "task_referenced_node_kind_changed") == 0 {
		t.Fatalf("task-referenced kind-change graph save = %+v, want task_referenced_node_kind_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked kind-change save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked kind-change save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowPerEntityMutationsUseGraphEditPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("active work blocks mutation", func(t *testing.T) {
		store, binding := newTestStore(t)
		workflowID := createValidWorkflow(t, ctx, store)
		if _, err := store.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		if _, err := store.StartTask(ctx, task.ID); err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		agentID := workflow.NodeID("node-agent-" + string(workflowID))
		_, err = store.AddNode(ctx, NodeRecord{ID: "node-blocked-active", WorkflowID: workflowID, Key: "blocked_active", Kind: workflow.NodeKindAgent, DisplayName: "Blocked", SubagentRole: "coder", PromptTemplate: "Noop."})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
			t.Fatalf("AddNode active error = %v, want active_node_placements policy blocker", err)
		}
		if err := store.DeleteNode(ctx, agentID); !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
			t.Fatalf("DeleteNode active error = %v, want active_node_placements policy blocker", err)
		}
		if err := store.DeleteEdge(ctx, workflow.EdgeID("edge-start-"+string(workflowID))); !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
			t.Fatalf("DeleteEdge active error = %v, want active_node_placements policy blocker", err)
		}
	})

	t.Run("start node kind change blocks update", func(t *testing.T) {
		store, _ := newTestStore(t)
		workflowID := createValidWorkflow(t, ctx, store)
		def, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		start := nodeByKind(t, def, workflow.NodeKindStart)
		_, err = store.UpdateNode(ctx, NodeRecord{ID: start.ID, WorkflowID: workflowID, Key: start.Key, Kind: workflow.NodeKindAgent, DisplayName: start.DisplayName})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "start_node_changed") {
			t.Fatalf("UpdateNode start error = %v, want start_node_changed policy blocker", err)
		}
		if err := store.DeleteNode(ctx, start.ID); !workflowGraphEditPolicyErrorHasBlocker(err, "start_node_changed") {
			t.Fatalf("DeleteNode start error = %v, want start_node_changed policy blocker", err)
		}
	})

	t.Run("last terminal kind change blocks update", func(t *testing.T) {
		store, _ := newTestStore(t)
		workflowID := createValidWorkflow(t, ctx, store)
		def, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		_, err = store.UpdateNode(ctx, NodeRecord{ID: done.ID, WorkflowID: workflowID, Key: done.Key, Kind: workflow.NodeKindAgent, DisplayName: done.DisplayName})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "last_terminal_changed") {
			t.Fatalf("UpdateNode terminal error = %v, want last_terminal_changed policy blocker", err)
		}
		if err := store.DeleteNode(ctx, done.ID); !workflowGraphEditPolicyErrorHasBlocker(err, "last_terminal_changed") {
			t.Fatalf("DeleteNode terminal error = %v, want last_terminal_changed policy blocker", err)
		}
	})
}

func TestWorkflowGraphSaveAllowsRemovedCompletedEdgeReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	edgeRemoval := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	edgeRemoval.Edges = removeWorkflowGraphSaveEdge(edgeRemoval.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	edgeRemoval.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(edgeRemoval.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	preview, err := store.PreviewWorkflowGraphSave(ctx, edgeRemoval)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave edge removal: %v", err)
	}
	if workflowGraphSaveBlockerCount(preview.Blockers, "edge_task_references") != 0 {
		t.Fatalf("edge removal preview = %+v, want no edge task-reference blocker", preview)
	}
	edgeSaved, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(edgeRemoval, preview.Impact))
	if err != nil {
		t.Fatalf("SaveWorkflowGraph edge removal: %v", err)
	}
	if !edgeSaved.Saved || workflowGraphSaveBlockerCount(edgeSaved.Blockers, "edge_task_references") != 0 {
		t.Fatalf("edge removal graph save = %+v, want saved without edge task-reference blocker", edgeSaved)
	}
}

func TestWorkflowGraphSaveBlocksRemovedPendingEdgeReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, workflow.EdgeID("edge-done-approval-"+string(workflowID)))
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave pending edge removal: %v", err)
	}
	blocked, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(req, preview.Impact))
	if err != nil {
		t.Fatalf("SaveWorkflowGraph pending edge removal: %v", err)
	}
	if blocked.Saved || workflowGraphSaveBlockerCount(blocked.Blockers, "edge_task_references") == 0 {
		t.Fatalf("pending edge removal graph save = %+v, want edge task-reference blocker", blocked)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked pending edge save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked pending edge save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveBlocksRemovedNodeTaskReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	nodeRemoval := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	nodeRemoval.Nodes = removeWorkflowGraphSaveNode(nodeRemoval.Nodes, agentID)
	nodeRemoval.TransitionGroups = removeWorkflowGraphSaveTransitionGroupsTouchingNode(def, nodeRemoval.TransitionGroups, agentID)
	nodeRemoval.Edges = removeWorkflowGraphSaveEdgesTouchingNode(def, nodeRemoval.Edges, agentID)
	nodeBlocked, err := store.SaveWorkflowGraph(ctx, nodeRemoval)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph node removal: %v", err)
	}
	if nodeBlocked.Saved || workflowGraphSaveBlockerCount(nodeBlocked.Blockers, "node_task_references") == 0 {
		t.Fatalf("node removal graph save = %+v, want node task-reference blocker", nodeBlocked)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked graph save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked graph save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveBlocksRemovedParallelBranchEdgeReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	removedEdgeID := workflow.EdgeID("edge-split-a-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, removedEdgeID)
	blocked, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph parallel branch edge removal: %v", err)
	}
	if blocked.Saved || workflowGraphSaveBlockerCount(blocked.Blockers, "edge_task_references") == 0 {
		t.Fatalf("parallel branch edge removal = %+v, want edge task-reference blocker", blocked)
	}
	var branchEdgeID sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT parallel_branch_edge_id FROM task_node_placements WHERE id = ?`, string(result.PlacementIDs[0])).Scan(&branchEdgeID); err != nil {
		t.Fatalf("query branch placement edge after blocked graph save: %v", err)
	}
	if !branchEdgeID.Valid || branchEdgeID.String == "" {
		t.Fatalf("branch placement edge after blocked graph save = %+v, want preserved reference", branchEdgeID)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked parallel branch save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked parallel branch save = %d, want %d", unchanged.Version, record.Version)
	}
}
