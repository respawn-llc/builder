package workflowstore

import (
	"testing"

	"core/server/workflow"
)

func TestWorkflowGraphSaveAppliesExpectedRevisionAndRemovalConfirmation(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	unconfirmed := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	unconfirmed.Edges = removeWorkflowGraphSaveEdge(unconfirmed.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	unconfirmed.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(unconfirmed.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	blocked, err := store.SaveWorkflowGraph(ctx, unconfirmed)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph unconfirmed: %v", err)
	}
	if blocked.Saved || workflowGraphSaveBlockerCount(blocked.Blockers, "confirmation_required") != 2 {
		t.Fatalf("unconfirmed graph save = %+v, want confirmation blocker for one removed edge and transition group", blocked)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after unconfirmed save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after unconfirmed save = %d, want %d", unchanged.Version, record.Version)
	}

	confirmed := unconfirmed
	confirmed.Confirmed = true
	confirmed = confirmWorkflowGraphSaveRequest(confirmed, blocked.Impact)
	saved, err := store.SaveWorkflowGraph(ctx, confirmed)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph confirmed: %v", err)
	}
	if !saved.Saved || len(saved.Blockers) != 0 || saved.Version != record.Version+1 {
		t.Fatalf("confirmed graph save = %+v, want single revision bump", saved)
	}
	updatedDef, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after confirmed save: %v", err)
	}
	if updatedRecord.Version != record.Version+1 || len(updatedDef.Edges) != len(def.Edges)-1 {
		t.Fatalf("updated graph = revision %d edges %d, want revision %d edges %d", updatedRecord.Version, len(updatedDef.Edges), record.Version+1, len(def.Edges)-1)
	}

	stale := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, updatedDef)
	staleResult, err := store.SaveWorkflowGraph(ctx, stale)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale no-op: %v", err)
	}
	if !staleResult.Saved || len(staleResult.Blockers) != 0 || staleResult.Version != updatedRecord.Version {
		t.Fatalf("stale no-op save = %+v, want successful no-op without workflow version check", staleResult)
	}
}

func TestWorkflowGraphSaveSupportsMetadataAndNoopRevisions(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	metadataOnly := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	metadataOnly.Metadata = &WorkflowGraphSaveMetadata{Name: "Renamed workflow", Description: "Updated description"}
	metadataSaved, err := store.SaveWorkflowGraph(ctx, metadataOnly)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph metadata-only: %v", err)
	}
	if !metadataSaved.Saved || metadataSaved.Version != record.Version+1 {
		t.Fatalf("metadata-only save = %+v, want version bump", metadataSaved)
	}
	_, afterMetadata, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after metadata-only: %v", err)
	}
	if afterMetadata.Name != "Renamed workflow" || afterMetadata.Description != "Updated description" || afterMetadata.Version != record.Version+1 {
		t.Fatalf("record after metadata-only = %+v, want metadata persisted with version bump", afterMetadata)
	}

	noop := workflowGraphSaveRequestFromDefinition(workflowID, afterMetadata.Version, false, def)
	noop.Metadata = &WorkflowGraphSaveMetadata{Name: afterMetadata.Name, Description: afterMetadata.Description}
	noopSaved, err := store.SaveWorkflowGraph(ctx, noop)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph noop: %v", err)
	}
	if !noopSaved.Saved || noopSaved.Changed || noopSaved.Version != afterMetadata.Version {
		t.Fatalf("noop save = %+v, want no revision bump", noopSaved)
	}
}

func TestWorkflowGraphSaveRoundTripsTransitionInvocationContract(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	for i := range req.Edges {
		switch req.Edges[i].Key {
		case "start":
			req.Edges[i].PromptTemplate = "Start from {{.TaskTitle}}."
		case "done":
			req.Edges[i].Parameters = []workflow.Parameter{{Key: "summary", Description: "Summary for terminal history."}}
		}
	}
	saved, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || !saved.Changed || saved.Version != record.Version+1 {
		t.Fatalf("save result = %+v, want graph change with version bump", saved)
	}

	updated, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	startEdge := edgeByKey(t, updated, "start")
	if startEdge.PromptTemplate != "Start from {{.TaskTitle}}." {
		t.Fatalf("start edge prompt = %q, want transition prompt round-tripped", startEdge.PromptTemplate)
	}
	doneEdge := edgeByKey(t, updated, "done")
	if len(doneEdge.Parameters) != 1 || doneEdge.Parameters[0].Key != "summary" || doneEdge.Parameters[0].Description != "Summary for terminal history." {
		t.Fatalf("done edge parameters = %+v, want transition parameters round-tripped", doneEdge.Parameters)
	}

	noop := workflowGraphSaveRequestFromDefinition(workflowID, updatedRecord.Version, false, updated)
	noopSaved, err := store.SaveWorkflowGraph(ctx, noop)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph noop: %v", err)
	}
	if !noopSaved.Saved || noopSaved.Changed || noopSaved.Version != updatedRecord.Version {
		t.Fatalf("noop save = %+v, want prompt/parameters preserved as unchanged graph", noopSaved)
	}
}

func TestWorkflowGraphSaveAcceptsClientGeneratedTopologyIDsAndRejectsCollisions(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Nodes = append(req.Nodes, NodeRecord{
		ID:           "workflow-node-00000000-0000-4000-8000-000000000001",
		WorkflowID:   workflowID,
		Key:          "client_generated",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Client Generated",
		SubagentRole: workflow.DefaultAgentRole,
	})
	req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
		ID:           "workflow-transition-group-00000000-0000-4000-8000-000000000001",
		WorkflowID:   workflowID,
		SourceNodeID: workflow.NodeID("node-agent-" + string(workflowID)),
		TransitionID: "client_generated",
		DisplayName:  "Client Generated",
	})
	req.Edges = append(req.Edges, EdgeRecord{
		ID:                "workflow-edge-00000000-0000-4000-8000-000000000001",
		WorkflowID:        workflowID,
		TransitionGroupID: "workflow-transition-group-00000000-0000-4000-8000-000000000001",
		Key:               "client_generated",
		TargetNodeID:      "workflow-node-00000000-0000-4000-8000-000000000001",
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Client generated.",
	})

	saved, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph client ids: %v", err)
	}
	if !saved.Saved || len(saved.Blockers) != 0 {
		t.Fatalf("client id graph save = %+v, want saved without blockers", saved)
	}
	updated, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after client ids: %v", err)
	}
	if nodeByID(t, updated, "workflow-node-00000000-0000-4000-8000-000000000001").Key != "client_generated" {
		t.Fatalf("client-generated node id was not persisted")
	}

	colliding := workflowGraphSaveRequestFromDefinition(workflowID, saved.Version, false, updated)
	colliding.Nodes = append(colliding.Nodes, colliding.Nodes[len(colliding.Nodes)-1])
	collidingResult, err := store.SaveWorkflowGraph(ctx, colliding)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph duplicate client id: %v", err)
	}
	if collidingResult.Saved || workflowGraphSaveBlockerCount(collidingResult.Blockers, "validation_failed") == 0 {
		t.Fatalf("duplicate client id graph save = %+v, want validation blocker", collidingResult)
	}
}

func TestWorkflowGraphSaveMetadataAndGraphAreAtomic(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(workflowID))

	combined := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	combined.Metadata = &WorkflowGraphSaveMetadata{Name: "Combined save", Description: "Graph and metadata"}
	combined.Nodes = renameWorkflowGraphSaveNode(combined.Nodes, agentID, "Edited Agent")
	saved, err := store.SaveWorkflowGraph(ctx, combined)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph combined: %v", err)
	}
	if !saved.Saved || saved.Version != record.Version+1 {
		t.Fatalf("combined save = %+v, want one version bump", saved)
	}
	updatedDef, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after combined: %v", err)
	}
	if updatedRecord.Name != "Combined save" || nodeByID(t, updatedDef, agentID).DisplayName != "Edited Agent" {
		t.Fatalf("combined save persisted record=%+v node=%+v", updatedRecord, nodeByID(t, updatedDef, agentID))
	}

	blocked := workflowGraphSaveRequestFromDefinition(workflowID, updatedRecord.Version, false, updatedDef)
	blocked.Metadata = &WorkflowGraphSaveMetadata{Name: "Must not persist", Description: "Blocked"}
	blocked.Edges = removeWorkflowGraphSaveEdge(blocked.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	blockedResult, err := store.SaveWorkflowGraph(ctx, blocked)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph blocked combined: %v", err)
	}
	if blockedResult.Saved || workflowGraphSaveBlockerCount(blockedResult.Blockers, "confirmation_required") == 0 {
		t.Fatalf("blocked combined save = %+v, want confirmation blocker", blockedResult)
	}
	_, unchangedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after blocked combined: %v", err)
	}
	if unchangedRecord.Name != "Combined save" || unchangedRecord.Description != "Graph and metadata" || unchangedRecord.Version != updatedRecord.Version {
		t.Fatalf("record after blocked combined = %+v, want unchanged", unchangedRecord)
	}
}

func TestWorkflowGraphSaveValidatesAndPersistsV1NodeGroups(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	groupID := "group-parallel-" + string(workflowID)
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: groupID, WorkflowID: workflowID, Key: "parallel", DisplayName: "Parallel"})
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-a-"+string(workflowID)), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-b-"+string(workflowID)), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-join-"+string(workflowID)), groupID)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph valid group: %v", err)
	}
	if !result.Saved || len(result.Blockers) != 0 {
		t.Fatalf("valid node group graph save = %+v, want saved", result)
	}
	savedDef, savedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after valid group: %v", err)
	}
	if savedRecord.Version != record.Version+1 {
		t.Fatalf("valid node group version = %d, want %d", savedRecord.Version, record.Version+1)
	}
	if len(savedDef.NodeGroups) != 1 || len(savedDef.NodeGroups[0].MemberNodeIDs) != 3 {
		t.Fatalf("saved node groups = %+v, want one group with three members", savedDef.NodeGroups)
	}
	if nodeByID(t, savedDef, workflow.NodeID("node-join-"+string(workflowID))).GroupID != groupID {
		t.Fatalf("saved join group id not persisted: %+v", nodeByID(t, savedDef, workflow.NodeID("node-join-"+string(workflowID))))
	}

	invalid := workflowGraphSaveRequestFromDefinition(workflowID, savedRecord.Version, false, savedDef)
	invalid.Nodes = setWorkflowGraphSaveNodeGroup(invalid.Nodes, workflow.NodeID("node-impl-b-"+string(workflowID)), "")
	invalidResult, err := store.SaveWorkflowGraph(ctx, invalid)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph invalid group: %v", err)
	}
	if invalidResult.Saved || workflowGraphSaveBlockerCount(invalidResult.Blockers, "validation_failed") == 0 {
		t.Fatalf("invalid node group graph save = %+v, want validation blocker", invalidResult)
	}
}

func TestWorkflowGraphSaveRejectsStaleVersion(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if err := store.UpdateWorkflowInfo(ctx, workflowID, "Remote rename", "Remote description"); err != nil {
		t.Fatalf("UpdateWorkflowInfo: %v", err)
	}
	_, remote, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition remote: %v", err)
	}

	stale := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	stale.Metadata = &WorkflowGraphSaveMetadata{Name: "Local rename", Description: "Local description"}
	result, err := store.SaveWorkflowGraph(ctx, stale)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale definition: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "version_changed") != remote.Version {
		t.Fatalf("stale metadata save = %+v, want current version blocker", result)
	}
	_, unchanged, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after stale definition: %v", err)
	}
	if unchanged.Name != "Remote rename" || unchanged.Version != remote.Version {
		t.Fatalf("record after stale metadata save = %+v, want remote metadata preserved", unchanged)
	}
}

func TestPreviewWorkflowGraphSaveDoesNotMutateWithoutBlockers(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, agentID, "Preview Agent")

	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if preview.Saved || len(preview.Blockers) != 0 || !preview.CanSave {
		t.Fatalf("preview graph save = %+v, want non-mutating savable preview without blockers", preview)
	}
	unchangedDef, unchangedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after preview: %v", err)
	}
	if unchangedRecord.Version != record.Version {
		t.Fatalf("workflow version after preview = %d, want %d", unchangedRecord.Version, record.Version)
	}
	if nodeByID(t, unchangedDef, agentID).DisplayName == "Preview Agent" {
		t.Fatalf("preview mutated node display name")
	}
}
