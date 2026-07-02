package workflowstore

import (
	"context"
	"database/sql"
	"errors"
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

func TestWorkflowGraphSaveAllowsUnrelatedEditsWhileTasksExist(t *testing.T) {
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
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("active-task graph save = %+v, want saved without broad active-work blockers", result)
		}
	})

	t.Run("claimed active run", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Claimed", Body: "Body"})
		started := startTask(t, ctx, store, task.ID)
		if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
			t.Fatalf("ClaimRun: %v", err)
		}
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph claimed active run: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("claimed active-run graph save = %+v, want saved without broad active-run blocker", result)
		}
	})

	t.Run("interrupted task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Interrupted", Body: "Body"})
		started := startTask(t, ctx, store, task.ID)
		if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
			t.Fatalf("InterruptRun: %v", err)
		}
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph interrupted task: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("interrupted-task graph save = %+v, want saved without broad active-work blockers", result)
		}
	})

	t.Run("pending approval", func(t *testing.T) {
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
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph pending approval: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("pending-approval graph save = %+v, want saved without pending_approvals blocker", result)
		}
	})

	t.Run("active task current node removal", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Active", Body: "Body"})
		startTask(t, ctx, store, task.ID)
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		agentID := workflow.NodeID("node-agent-" + string(workflowID))
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = removeWorkflowGraphSaveNode(req.Nodes, agentID)
		req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupsTouchingNode(def, req.TransitionGroups, agentID)
		req.Edges = removeWorkflowGraphSaveEdgesTouchingNode(def, req.Edges, agentID)

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph active task current node removal: %v", err)
		}
		if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "node_task_references") == 0 {
			t.Fatalf("active-task current-node removal = %+v, want node_task_references blocker", result)
		}
	})

	t.Run("active task source transition removal", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		agentID := workflow.NodeID("node-agent-" + string(workflowID))
		spareDoneID := workflow.NodeID("node-spare-done-" + string(workflowID))
		spareGroupID := workflow.TransitionGroupID("group-spare-done-" + string(workflowID))
		spareEdgeID := workflow.EdgeID("edge-spare-done-" + string(workflowID))
		if _, err := store.AddNode(ctx, NodeRecord{ID: spareDoneID, WorkflowID: workflowID, Key: "spare_done", Kind: workflow.NodeKindTerminal, DisplayName: "Spare Done"}); err != nil {
			t.Fatalf("AddNode spare terminal: %v", err)
		}
		if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: spareGroupID, WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "spare_done", DisplayName: "Spare Done"}); err != nil {
			t.Fatalf("AddTransitionGroup spare terminal: %v", err)
		}
		if _, err := store.AddEdge(ctx, EdgeRecord{ID: spareEdgeID, WorkflowID: workflowID, TransitionGroupID: spareGroupID, Key: "spare_done", TargetNodeID: spareDoneID, ContextMode: workflow.ContextModeNewSession}); err != nil {
			t.Fatalf("AddEdge spare terminal: %v", err)
		}
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Active", Body: "Body"})
		startTask(t, ctx, store, task.ID)
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
		req.Nodes = removeWorkflowGraphSaveNode(req.Nodes, spareDoneID)
		req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, spareGroupID)
		req.Edges = removeWorkflowGraphSaveEdge(req.Edges, spareEdgeID)
		preview, err := store.PreviewWorkflowGraphSave(ctx, req)
		if err != nil {
			t.Fatalf("PreviewWorkflowGraphSave active task source transition removal: %v", err)
		}

		result, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(req, preview.Impact))
		if err != nil {
			t.Fatalf("SaveWorkflowGraph active task source transition removal: %v", err)
		}
		if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "active_transition_contract_changed") == 0 {
			t.Fatalf("active-task source transition removal = %+v, want active_transition_contract_changed blocker", result)
		}
	})

	t.Run("interrupted task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Interrupted", Body: "Body"})
		started := startTask(t, ctx, store, task.ID)
		if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
			t.Fatalf("InterruptRun: %v", err)
		}
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph interrupted task: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("interrupted-task graph save = %+v, want saved without active-work blockers", result)
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
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, workflow.NodeIDOf(start), workflow.NodeKindAgent)

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
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, workflow.NodeIDOf(done), workflow.NodeKindAgent)

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
	startTask(t, ctx, store, task.ID)
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

func TestWorkflowGraphSaveAllowsTransitionInvocationMetadataWhilePendingApprovalExists(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if pending.State != "pending_approval" {
		t.Fatalf("setup transition state = %q, want pending_approval", pending.State)
	}
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	doneEdge := edgeByKey(t, def, "done")
	doneGroupID := doneEdge.TransitionGroupID
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.TransitionGroups = mutateWorkflowGraphSaveTransitionGroup(req.TransitionGroups, doneGroupID, func(group *TransitionGroupRecord) {
		group.DisplayName = "Done when approved"
		group.Description = "Updated approval copy."
	})
	req.Edges = mutateWorkflowGraphSaveEdge(req.Edges, doneEdge.ID, func(edge *EdgeRecord) {
		edge.RequiresApproval = false
		edge.ContextMode = workflow.ContextModeNewSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}
	})

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph metadata update with pending approval: %v", err)
	}
	if !result.Saved || len(result.Blockers) != 0 {
		t.Fatalf("metadata update with pending approval = %+v, want saved without blockers", result)
	}
	approval, err := store.ApproveTransition(ctx, pending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition after metadata update: %v", err)
	}
	if approval.State != "approved" {
		t.Fatalf("approval after metadata update = %+v, want original pending snapshot to approve", approval)
	}
}

func TestWorkflowGraphSaveAllowsTransitionPromptMetadataWhileRunActive(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	startEdge := edgeByKey(t, def, "start")
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Edges = mutateWorkflowGraphSaveEdge(req.Edges, startEdge.ID, func(edge *EdgeRecord) {
		edge.PromptTemplate = "Updated prompt for future runs."
	})

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph prompt update with active run: %v", err)
	}
	if !result.Saved || len(result.Blockers) != 0 {
		t.Fatalf("prompt update with active run = %+v, want saved without blockers", result)
	}
}

func TestWorkflowGraphSaveBlocksTransitionContractChangeWhileSourceRunUnresolved(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	doneEdge := edgeByKey(t, def, "done")
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Edges = mutateWorkflowGraphSaveEdge(req.Edges, doneEdge.ID, func(edge *EdgeRecord) {
		edge.TargetNodeID = agentID
	})

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph source-run transition contract update: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "active_transition_contract_changed") == 0 {
		t.Fatalf("source-run transition contract update = %+v, want active_transition_contract_changed blocker", result)
	}
}

func TestWorkflowGraphSaveBlocksTransitionContractChangeWhileSourceRunInterrupted(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	doneEdge := edgeByKey(t, def, "done")
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Edges = mutateWorkflowGraphSaveEdge(req.Edges, doneEdge.ID, func(edge *EdgeRecord) {
		edge.TargetNodeID = agentID
	})

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph interrupted source-run transition contract update: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "active_transition_contract_changed") == 0 {
		t.Fatalf("interrupted source-run transition contract update = %+v, want active_transition_contract_changed blocker", result)
	}
}

func TestWorkflowGraphSaveBlocksUnsafeTransitionContractChangeWithPendingApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if pending.State != "pending_approval" {
		t.Fatalf("setup transition state = %q, want pending_approval", pending.State)
	}
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	doneEdge := edgeByKey(t, def, "done")
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Edges = mutateWorkflowGraphSaveEdge(req.Edges, doneEdge.ID, func(edge *EdgeRecord) {
		edge.TargetNodeID = agentID
	})

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph unsafe transition contract update: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "active_transition_contract_changed") == 0 {
		t.Fatalf("unsafe transition contract update = %+v, want active_transition_contract_changed blocker", result)
	}
}

func TestWorkflowGraphSaveBlocksEdgeGroupChangeWithHistoricalReference(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	doneEdge := edgeByKey(t, def, "done")
	startGroupID := workflow.TransitionGroupID("group-start-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Edges = mutateWorkflowGraphSaveEdge(req.Edges, doneEdge.ID, func(edge *EdgeRecord) {
		edge.TransitionGroupID = startGroupID
	})

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph edge group update: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "task_referenced_edge_group_changed") == 0 {
		t.Fatalf("edge group update = %+v, want task_referenced_edge_group_changed blocker", result)
	}
}

func TestWorkflowPerEntityMutationsUseGraphEditPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("active work allows unrelated mutation but node delete still respects task history", func(t *testing.T) {
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
		if _, err := store.AddNode(ctx, NodeRecord{ID: "node-allowed-active", WorkflowID: workflowID, Key: "allowed_active", Kind: workflow.NodeKindAgent, DisplayName: "Allowed", SubagentRole: "coder", PromptTemplate: "Noop."}); err != nil {
			t.Fatalf("AddNode active work: %v", err)
		}
		if err := store.DeleteNode(ctx, agentID); !errors.Is(err, ErrNodeHasTaskHistory) {
			t.Fatalf("DeleteNode active error = %v, want task-history guard", err)
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
		_, err = store.UpdateNode(ctx, NodeRecord{ID: workflow.NodeIDOf(start), WorkflowID: workflowID, Key: workflow.NodeKey(start), Kind: workflow.NodeKindAgent, DisplayName: workflow.NodeDisplayName(start)})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "start_node_changed") {
			t.Fatalf("UpdateNode start error = %v, want start_node_changed policy blocker", err)
		}
		if err := store.DeleteNode(ctx, workflow.NodeIDOf(start)); !workflowGraphEditPolicyErrorHasBlocker(err, "start_node_changed") {
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
		_, err = store.UpdateNode(ctx, NodeRecord{ID: workflow.NodeIDOf(done), WorkflowID: workflowID, Key: workflow.NodeKey(done), Kind: workflow.NodeKindAgent, DisplayName: workflow.NodeDisplayName(done)})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "last_terminal_changed") {
			t.Fatalf("UpdateNode terminal error = %v, want last_terminal_changed policy blocker", err)
		}
		if err := store.DeleteNode(ctx, workflow.NodeIDOf(done)); !workflowGraphEditPolicyErrorHasBlocker(err, "last_terminal_changed") {
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

func TestWorkflowGraphSaveDetachesRemovedHistoricalNodeReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	nodeRemoval := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	nodeRemoval.Nodes = removeWorkflowGraphSaveNode(nodeRemoval.Nodes, agentID)
	nodeRemoval.TransitionGroups = removeWorkflowGraphSaveTransitionGroupsTouchingNode(def, nodeRemoval.TransitionGroups, agentID)
	nodeRemoval.Edges = removeWorkflowGraphSaveEdgesTouchingNode(def, nodeRemoval.Edges, agentID)
	preview, err := store.PreviewWorkflowGraphSave(ctx, nodeRemoval)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave node removal: %v", err)
	}
	if preview.Impact.NodeTaskReferenceCount == 0 {
		t.Fatalf("historical node removal preview impact = %+v, want detached node references counted", preview.Impact)
	}
	if workflowGraphSaveBlockerCount(preview.Blockers, "node_task_references") != 0 {
		t.Fatalf("historical node removal preview = %+v, want no current node task-reference blocker", preview)
	}
	nodeSaved, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(nodeRemoval, preview.Impact))
	if err != nil {
		t.Fatalf("SaveWorkflowGraph node removal: %v", err)
	}
	if !nodeSaved.Saved || workflowGraphSaveBlockerCount(nodeSaved.Blockers, "node_task_references") != 0 {
		t.Fatalf("node removal graph save = %+v, want saved with detached historical node references", nodeSaved)
	}
	updated, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after historical node removal: %v", err)
	}
	for _, node := range updated.Nodes {
		if workflow.NodeIDOf(node) == agentID {
			t.Fatalf("removed historical node %q still appears in workflow definition", agentID)
		}
	}
	placements, err := store.queries.ListTaskNodePlacements(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("ListTaskNodePlacements after historical node removal: %v", err)
	}
	foundDetachedHistoricalPlacement := false
	for _, placement := range placements {
		if placement.State == "completed" && !placement.NodeID.Valid {
			foundDetachedHistoricalPlacement = true
		}
	}
	if !foundDetachedHistoricalPlacement {
		t.Fatalf("placements after historical node removal = %+v, want completed detached placement", placements)
	}
}

func TestWorkflowGraphSaveBlocksHistoricalReferencedNodeKindChange(t *testing.T) {
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
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, agentID, workflow.NodeKindTerminal)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph historical node kind change: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "task_referenced_node_kind_changed") == 0 {
		t.Fatalf("historical referenced kind-change graph save = %+v, want task_referenced_node_kind_changed blocker", result)
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
