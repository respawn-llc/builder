package workflowstore

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type graphSaveWorkflowFactory func(*testing.T, context.Context, *Store) runtimeids.WorkflowID

type graphSaveFixture struct {
	ctx        context.Context
	store      *Store
	workflowID runtimeids.WorkflowID
	def        workflow.Definition
	record     WorkflowRecord
}

func newGraphSaveFixture(t *testing.T, create graphSaveWorkflowFactory) graphSaveFixture {
	t.Helper()
	ctx, store, _ := newTestStoreContext(t)
	workflowID := create(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	return graphSaveFixture{ctx: ctx, store: store, workflowID: workflowID, def: def, record: record}
}

func (f graphSaveFixture) request(version int64, confirmed bool, def workflow.Definition) WorkflowGraphSaveRequest {
	return workflowGraphSaveRequestFromDefinition(f.workflowID, version, confirmed, def)
}

func (f graphSaveFixture) save(t *testing.T, req WorkflowGraphSaveRequest) WorkflowGraphSaveResult {
	t.Helper()
	result, err := f.store.SaveWorkflowGraph(f.ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	return result
}

func (f graphSaveFixture) preview(t *testing.T, req WorkflowGraphSaveRequest) WorkflowGraphSaveResult {
	t.Helper()
	result, err := f.store.PreviewWorkflowGraphSave(f.ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	return result
}

func (f graphSaveFixture) current(t *testing.T) (workflow.Definition, WorkflowRecord) {
	t.Helper()
	def, record, err := f.store.GetDefinition(f.ctx, f.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	return def, record
}

func TestWorkflowGraphSaveRejectsMissingNestedWorkflowIDs(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	tests := []struct {
		name   string
		mutate func(*WorkflowGraphSaveRequest)
	}{
		{
			name: "node group",
			mutate: func(req *WorkflowGraphSaveRequest) {
				req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: "missing-workflow-id-group", WorkflowID: runtimeids.WorkflowID{}, Key: "missing_workflow_id", DisplayName: "Missing workflow ID"})
			},
		},
		{
			name: "node",
			mutate: func(req *WorkflowGraphSaveRequest) {
				req.Nodes[0].WorkflowID = runtimeids.WorkflowID{}
			},
		},
		{
			name: "transition group",
			mutate: func(req *WorkflowGraphSaveRequest) {
				req.TransitionGroups[0].WorkflowID = runtimeids.WorkflowID{}
			},
		},
		{
			name: "edge",
			mutate: func(req *WorkflowGraphSaveRequest) {
				req.Edges[0].WorkflowID = runtimeids.WorkflowID{}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := f.request(f.record.Version, false, f.def)
			test.mutate(&req)
			_, err := f.store.PreviewWorkflowGraphSave(f.ctx, req)
			if !errors.Is(err, ErrWorkflowIDRequired) {
				t.Fatalf("PreviewWorkflowGraphSave error = %v, want missing nested workflow identity rejection", err)
			}
		})
	}
}

func TestWorkflowGraphElementMutationsRejectMissingWorkflowIDs(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "missing-workflow-id-group"}); !errors.Is(err, ErrWorkflowIDRequired) {
		t.Fatalf("AddTransitionGroup error = %v, want missing workflow identity rejection", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "missing-workflow-id-edge"}); !errors.Is(err, ErrWorkflowIDRequired) {
		t.Fatalf("AddEdge error = %v, want missing workflow identity rejection", err)
	}
}

func TestWorkflowGraphSaveReportsRemovedTransitionBranchImpact(t *testing.T) {
	f := newGraphSaveFixture(t, createFanoutJoinWorkflow)
	removedEdgeID := workflow.EdgeID("edge-split-b-" + f.workflowID.String())
	req := f.request(f.record.Version, false, f.def)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, removedEdgeID)

	preview := f.preview(t, req)

	if !preview.Changed {
		t.Fatalf("preview Changed = false, want true")
	}
	wantRemoved := []WorkflowGraphEntityReference{{
		EntityType: WorkflowGraphEntityTypeEdge,
		EntityID:   string(removedEdgeID),
	}}
	if !slices.Equal(preview.Impact.RemovedEntities, wantRemoved) {
		t.Fatalf("removed entities = %+v, want %+v", preview.Impact.RemovedEntities, wantRemoved)
	}
	if preview.Impact.RemovedEdgeCount != 1 ||
		preview.Impact.NodeTaskReferenceCount != 0 ||
		preview.Impact.EdgeTaskReferenceCount != 0 {
		t.Fatalf("preview impact = %+v, want one removed Transition Branch and unchanged aggregate Task-reference counts", preview.Impact)
	}
}

func TestWorkflowGraphSaveAllowsRemovingNodeGroupWithoutConfirmation(t *testing.T) {
	f := newGraphSaveFixture(t, createFanoutJoinWorkflow)
	removedGroupID := "group-parallel-" + f.workflowID.String()
	added := f.request(f.record.Version, false, f.def)
	added.NodeGroups = append(added.NodeGroups, NodeGroupRecord{
		ID:          removedGroupID,
		WorkflowID:  f.workflowID,
		Key:         "parallel",
		DisplayName: "Parallel",
	})
	for _, nodeID := range []workflow.NodeID{
		workflow.NodeID("node-impl-a-" + f.workflowID.String()),
		workflow.NodeID("node-impl-b-" + f.workflowID.String()),
		workflow.NodeID("node-join-" + f.workflowID.String()),
	} {
		added.Nodes = setWorkflowGraphSaveNodeGroup(added.Nodes, nodeID, removedGroupID)
	}
	addResult := f.save(t, added)
	if !addResult.Saved || !addResult.Changed {
		t.Fatalf("add Node Group = %+v, want changed save", addResult)
	}

	current, record := f.current(t)
	remove := f.request(record.Version, false, current)
	remove.NodeGroups = nil
	for _, nodeID := range []workflow.NodeID{
		workflow.NodeID("node-impl-a-" + f.workflowID.String()),
		workflow.NodeID("node-impl-b-" + f.workflowID.String()),
		workflow.NodeID("node-join-" + f.workflowID.String()),
	} {
		remove.Nodes = setWorkflowGraphSaveNodeGroup(remove.Nodes, nodeID, "")
	}
	preview := f.preview(t, remove)

	wantRemoved := []WorkflowGraphEntityReference{{
		EntityType: WorkflowGraphEntityTypeNodeGroup,
		EntityID:   removedGroupID,
	}}
	if !preview.Changed ||
		preview.Impact.RemovedNodeGroupCount != 1 ||
		!slices.Equal(preview.Impact.RemovedEntities, wantRemoved) {
		t.Fatalf("removed Node Group preview = %+v, want changed exact Node Group impact %+v", preview, wantRemoved)
	}
	if !preview.CanSave || preview.ConfirmationRequired || workflowGraphSaveBlockerCount(preview.Blockers, "confirmation_required") != 0 {
		t.Fatalf("removed Node Group preview = %+v, want saveable without confirmation", preview)
	}

	saved := f.save(t, remove)
	if !saved.Saved || !saved.Changed || saved.Version != record.Version+1 {
		t.Fatalf("removed Node Group save = %+v, want changed save at version %d", saved, record.Version+1)
	}
	savedDefinition, _ := f.current(t)
	if len(savedDefinition.NodeGroups) != 0 {
		t.Fatalf("saved Node Groups = %+v, want removed", savedDefinition.NodeGroups)
	}
}

func TestWorkflowGraphSaveBlockersIdentifyRemovedTaskReferencedEntities(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)

	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + workflowID.String())
	startEdgeID := workflow.EdgeID("edge-start-" + workflowID.String())
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Nodes = removeWorkflowGraphSaveNode(req.Nodes, agentID)
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-start-"+workflowID.String()))
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-done-"+workflowID.String()))
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, startEdgeID)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, workflow.EdgeID("edge-done-"+workflowID.String()))

	preview := graphSaveFixture{ctx: ctx, store: store, workflowID: workflowID}.preview(t, req)

	wantNode := []WorkflowGraphEntityReference{{EntityType: WorkflowGraphEntityTypeNode, EntityID: string(agentID)}}
	if got := workflowGraphSaveBlockerEntities(preview.Blockers, "node_task_references"); !slices.Equal(got, wantNode) {
		t.Fatalf("node_task_references affected entities = %+v, want %+v", got, wantNode)
	}
	wantEdge := []WorkflowGraphEntityReference{{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(startEdgeID)}}
	if got := workflowGraphSaveBlockerEntities(preview.Blockers, "edge_task_references"); !slices.Equal(got, wantEdge) {
		t.Fatalf("edge_task_references affected entities = %+v, want %+v", got, wantEdge)
	}
}

func TestWorkflowGraphSaveStructuralBlockersIdentifyChangedNodes(t *testing.T) {
	tests := []struct {
		name        string
		nodeKind    workflow.NodeKind
		blockerCode string
	}{
		{name: "Start Node", nodeKind: workflow.NodeKindStart, blockerCode: "start_node_changed"},
		{name: "last Terminal Node", nodeKind: workflow.NodeKindTerminal, blockerCode: "last_terminal_changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newGraphSaveFixture(t, createValidWorkflow)
			node := nodeByKind(t, f.def, test.nodeKind)
			nodeID := workflow.NodeIDOf(node)
			req := f.request(f.record.Version, false, f.def)
			for index := range req.Nodes {
				if req.Nodes[index].ID == nodeID {
					req.Nodes[index].Kind = workflow.NodeKindAgent
					req.Nodes[index].SubagentRole = "coder"
				}
			}

			preview := f.preview(t, req)

			want := []WorkflowGraphEntityReference{{EntityType: WorkflowGraphEntityTypeNode, EntityID: string(nodeID)}}
			if got := workflowGraphSaveBlockerEntities(preview.Blockers, test.blockerCode); !slices.Equal(got, want) {
				t.Fatalf("%s affected entities = %+v, want %+v", test.blockerCode, got, want)
			}
		})
	}
}

func TestWorkflowGraphSaveTaskReferencedEditBlockersIdentifyChangedEntities(t *testing.T) {
	t.Run("Node kind", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		startTask(t, ctx, store, task.ID)
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		agentID := workflow.NodeID("node-agent-" + workflowID.String())
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
		for index := range req.Nodes {
			if req.Nodes[index].ID == agentID {
				req.Nodes[index].Kind = workflow.NodeKindScript
				req.Nodes[index].SubagentRole = ""
				req.Nodes[index].CompletionMode = ""
				req.Nodes[index].ScriptPath = "scripts/agent"
			}
		}
		for index := range req.Edges {
			if req.Edges[index].TargetNodeID == agentID {
				req.Edges[index].PromptTemplate = ""
			}
		}

		preview, err := store.PreviewWorkflowGraphSave(ctx, req)
		if err != nil {
			t.Fatalf("PreviewWorkflowGraphSave: %v", err)
		}
		want := []WorkflowGraphEntityReference{{EntityType: WorkflowGraphEntityTypeNode, EntityID: string(agentID)}}
		if got := workflowGraphSaveBlockerEntities(preview.Blockers, "task_referenced_node_kind_changed"); !slices.Equal(got, want) {
			t.Fatalf("task_referenced_node_kind_changed affected entities = %+v, want %+v", got, want)
		}
	})

	t.Run("history-reinterpreting Transition Branch", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		startTask(t, ctx, store, task.ID)
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		edgeID := workflow.EdgeID("edge-start-" + workflowID.String())
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
		for index := range req.Edges {
			if req.Edges[index].ID == edgeID {
				req.Edges[index].TransitionGroupID = workflow.TransitionGroupID("group-done-" + workflowID.String())
			}
		}

		preview, err := store.PreviewWorkflowGraphSave(ctx, req)
		if err != nil {
			t.Fatalf("PreviewWorkflowGraphSave: %v", err)
		}
		want := []WorkflowGraphEntityReference{{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(edgeID)}}
		if got := workflowGraphSaveBlockerEntities(preview.Blockers, "task_referenced_edge_group_changed"); !slices.Equal(got, want) {
			t.Fatalf("task_referenced_edge_group_changed affected entities = %+v, want %+v", got, want)
		}
	})
}

func TestWorkflowGraphSaveValidationBlockerCanonicalizesAffectedEntities(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	agentID := workflow.NodeID("node-agent-" + f.workflowID.String())
	req := f.request(f.record.Version, false, f.def)
	duplicate := *workflowGraphSaveNodeRecord(t, req.Nodes, agentID)
	duplicate.ID, duplicate.Kind, duplicate.SubagentRole = workflow.NodeID("node-second-start-"+f.workflowID.String()), workflow.NodeKindStart, ""
	req.Nodes = append(req.Nodes, duplicate)

	preview := f.preview(t, req)

	want := canonicalWorkflowGraphEntityReferences([]WorkflowGraphEntityReference{{EntityType: WorkflowGraphEntityTypeNode, EntityID: string(workflow.NodeIDOf(nodeByKind(t, f.def, workflow.NodeKindStart)))}, {EntityType: WorkflowGraphEntityTypeNode, EntityID: string(agentID)}, {EntityType: WorkflowGraphEntityTypeNode, EntityID: string(duplicate.ID)}})
	if got := workflowGraphSaveBlockerEntities(preview.Blockers, "validation_failed"); !slices.Equal(got, want) {
		t.Fatalf("validation_failed affected entities = %+v, want canonical deduplicated %+v", got, want)
	}
}

type blockingGraphSaveRoleResolver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingGraphSaveRoleResolver) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return workflow.TargetAgentRole{Identity: role, QuestionsEnabled: true}, true
}

func (r *blockingGraphSaveRoleResolver) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return nil
}

func TestWorkflowGraphSavePreparationDoesNotHoldWriteTransaction(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	other, err := f.store.CreateWorkflow(f.ctx, CreateWorkflowRequest{Name: "Unrelated workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow unrelated: %v", err)
	}
	resolver := &blockingGraphSaveRoleResolver{started: make(chan struct{}), release: make(chan struct{})}
	f.store.roleResolver = resolver
	request := f.request(f.record.Version, false, f.def)
	request.Nodes = renameWorkflowGraphSaveNode(request.Nodes, workflow.NodeID("node-agent-"+f.workflowID.String()), "Renamed agent")

	saveDone := make(chan error, 1)
	go func() {
		_, err := f.store.SaveWorkflowGraph(f.ctx, request)
		saveDone <- err
	}()
	<-resolver.started

	readDone := make(chan error, 1)
	go func() {
		_, _, err := f.store.GetDefinition(f.ctx, other.ID)
		readDone <- err
	}()
	if err := <-readDone; err != nil {
		t.Fatalf("unrelated workflow read during preparation: %v", err)
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := f.store.CreateWorkflow(f.ctx, CreateWorkflowRequest{Name: "Other unrelated workflow"})
		writeDone <- err
	}()
	if err := <-writeDone; err != nil {
		t.Fatalf("unrelated workflow write during preparation: %v", err)
	}

	select {
	case err := <-saveDone:
		t.Fatalf("save completed while validation was blocked: %v", err)
	default:
	}
	close(resolver.release)
	if err := <-saveDone; err != nil {
		t.Fatalf("SaveWorkflowGraph after validation release: %v", err)
	}
}

func TestWorkflowGraphSaveCommitAllowsActiveWorkStartedDuringPreparation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	activeStore, _ := openConcurrentWorkflowStores(t, cfg)
	activeStore.roleResolver = testsetup.QuestionsEnabled("coder", "reviewer")
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	agentID := workflow.NodeID("node-agent-" + workflowID.String())
	spareDoneID := workflow.NodeID("node-spare-done-" + workflowID.String())
	spareGroupID := workflow.TransitionGroupID("group-spare-done-" + workflowID.String())
	spareEdgeID := workflow.EdgeID("edge-spare-done-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes, NodeRecord{ID: spareDoneID, WorkflowID: workflowID, Key: "spare_done", Kind: workflow.NodeKindTerminal, DisplayName: "Spare Done"})
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{ID: spareGroupID, WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "spare_done", DisplayName: "Spare Done"})
		req.Edges = append(req.Edges, EdgeRecord{ID: spareEdgeID, WorkflowID: workflowID, TransitionGroupID: spareGroupID, Key: "spare_done", TargetNodeID: spareDoneID, ContextMode: workflow.ContextModeNewSession})
	})
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
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if workflowGraphSaveBlockerCount(preview.Blockers, "active_transition_contract_changed") != 0 {
		t.Fatalf("preview before active work = %+v, want no dynamic policy blocker", preview)
	}

	resolver := &blockingGraphSaveRoleResolver{started: make(chan struct{}), release: make(chan struct{})}
	store.roleResolver = resolver
	type saveResult struct {
		result WorkflowGraphSaveResult
		err    error
	}
	saved := make(chan saveResult, 1)
	go func() {
		result, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(req, preview.Impact))
		saved <- saveResult{result: result, err: err}
	}()
	<-resolver.started

	task := createDefaultTask(t, ctx, activeStore, binding.ProjectID)
	startTask(t, ctx, activeStore, task.ID)
	close(resolver.release)

	outcome := <-saved
	if outcome.err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", outcome.err)
	}
	if !outcome.result.Saved || len(outcome.result.Blockers) != 0 {
		t.Fatalf("save after active work starts = %+v, want committed graph", outcome.result)
	}
}

func TestWorkflowGraphSaveAllowsAddingTransitionFromActiveSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(runtimeids.WorkflowID, workflow.NodeID, workflow.NodeID, *WorkflowGraphSaveRequest)
	}{
		{
			name: "transition group",
			mutate: func(workflowID runtimeids.WorkflowID, agentID workflow.NodeID, spareDoneID workflow.NodeID, req *WorkflowGraphSaveRequest) {
				groupID := workflow.TransitionGroupID("group-added-" + workflowID.String())
				req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
					ID:           groupID,
					WorkflowID:   workflowID,
					SourceNodeID: agentID,
					TransitionID: "added",
					DisplayName:  "Added",
				})
				req.Edges = append(req.Edges, EdgeRecord{
					ID:                workflow.EdgeID("edge-added-" + workflowID.String()),
					WorkflowID:        workflowID,
					TransitionGroupID: groupID,
					Key:               "added",
					TargetNodeID:      spareDoneID,
					ContextMode:       workflow.ContextModeNewSession,
				})
			},
		},
		{
			name: "edge",
			mutate: func(workflowID runtimeids.WorkflowID, _ workflow.NodeID, spareDoneID workflow.NodeID, req *WorkflowGraphSaveRequest) {
				req.Edges = append(req.Edges, EdgeRecord{
					ID:                workflow.EdgeID("edge-added-" + workflowID.String()),
					WorkflowID:        workflowID,
					TransitionGroupID: workflow.TransitionGroupID("group-done-" + workflowID.String()),
					Key:               "added",
					TargetNodeID:      spareDoneID,
					ContextMode:       workflow.ContextModeNewSession,
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, store, binding, _ := newTestStoreWithConfigContext(t)
			workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
			task := createDefaultTask(t, ctx, store, binding.ProjectID)
			startTask(t, ctx, store, task.ID)

			def, record, err := store.GetDefinition(ctx, workflowID)
			if err != nil {
				t.Fatalf("GetDefinition: %v", err)
			}
			agentID := workflow.NodeID("node-agent-" + workflowID.String())
			spareDoneID := workflow.NodeID("node-spare-done-" + workflowID.String())
			req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
			req.Nodes = append(req.Nodes, NodeRecord{
				ID:          spareDoneID,
				WorkflowID:  workflowID,
				Key:         "spare_done",
				Kind:        workflow.NodeKindTerminal,
				DisplayName: "Spare Done",
			})
			test.mutate(workflowID, agentID, spareDoneID, &req)

			preview, err := store.PreviewWorkflowGraphSave(ctx, req)
			if err != nil {
				t.Fatalf("PreviewWorkflowGraphSave: %v", err)
			}
			if workflowGraphSaveBlockerCount(preview.Blockers, "active_transition_contract_changed") != 0 {
				t.Fatalf("preview = %+v, want no active-source transition blocker", preview)
			}
		})
	}
}

func TestWorkflowGraphSaveIncompatibleActiveTransitionFailsCompletionWithoutTaskMutation(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	source := started.Mutation.Created[0].Reference

	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	for index := range req.TransitionGroups {
		if req.TransitionGroups[index].SourceNodeID == source.NodeID {
			req.TransitionGroups[index].TransitionID = "renamed"
		}
	}
	saved, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || len(saved.Blockers) != 0 {
		t.Fatalf("save = %+v, want committed transition edit", saved)
	}

	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "done",
	}); err == nil {
		t.Fatal("CompleteCurrentNode with obsolete transition succeeded")
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after rejected completion: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].Reference != source {
		t.Fatalf("current nodes after rejected completion = %+v, want unchanged source", currentNodes)
	}

	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "renamed",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode with current transition: %v", err)
	}
}

func TestWorkflowGraphSaveAllowsReassigningExistingEdgeToActiveSource(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	planID := workflow.NodeID("node-plan-" + workflowID.String())
	joinID := workflow.NodeID("node-join-" + workflowID.String())
	spareSourceID := workflow.NodeID("node-spare-source-" + workflowID.String())
	spareBranchAID := workflow.NodeID("node-spare-a-" + workflowID.String())
	spareBranchBID := workflow.NodeID("node-spare-b-" + workflowID.String())
	spareRouteGroupID := workflow.TransitionGroupID("group-spare-route-" + workflowID.String())
	spareSplitGroupID := workflow.TransitionGroupID("group-spare-split-" + workflowID.String())
	spareBranchAGroupID := workflow.TransitionGroupID("group-spare-a-" + workflowID.String())
	spareBranchBGroupID := workflow.TransitionGroupID("group-spare-b-" + workflowID.String())
	reassignedEdgeID := workflow.EdgeID("edge-spare-split-a-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: spareSourceID, WorkflowID: workflowID, Key: "spare_source", Kind: workflow.NodeKindAgent, DisplayName: "Spare Source", SubagentRole: "coder"},
			NodeRecord{ID: spareBranchAID, WorkflowID: workflowID, Key: "spare_a", Kind: workflow.NodeKindAgent, DisplayName: "Spare A", SubagentRole: "coder"},
			NodeRecord{ID: spareBranchBID, WorkflowID: workflowID, Key: "spare_b", Kind: workflow.NodeKindAgent, DisplayName: "Spare B", SubagentRole: "coder"},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: spareRouteGroupID, WorkflowID: workflowID, SourceNodeID: planID, TransitionID: "spare", DisplayName: "Spare"},
			TransitionGroupRecord{ID: spareSplitGroupID, WorkflowID: workflowID, SourceNodeID: spareSourceID, TransitionID: "spare_split", DisplayName: "Split"},
			TransitionGroupRecord{ID: spareBranchAGroupID, WorkflowID: workflowID, SourceNodeID: spareBranchAID, TransitionID: "join_spare_a", DisplayName: "Join"},
			TransitionGroupRecord{ID: spareBranchBGroupID, WorkflowID: workflowID, SourceNodeID: spareBranchBID, TransitionID: "join_spare_b", DisplayName: "Join"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-spare-route-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: spareRouteGroupID, Key: "spare", TargetNodeID: spareSourceID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Route spare work."},
			EdgeRecord{ID: reassignedEdgeID, WorkflowID: workflowID, TransitionGroupID: spareSplitGroupID, Key: "spare_a", TargetNodeID: spareBranchAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do spare A."},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-split-b-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: spareSplitGroupID, Key: "spare_b", TargetNodeID: spareBranchBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do spare B."},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-a-join-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: spareBranchAGroupID, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-b-join-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: spareBranchBGroupID, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		)
	})
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)

	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	for index := range req.Edges {
		if req.Edges[index].ID == reassignedEdgeID {
			req.Edges[index].TransitionGroupID = workflow.TransitionGroupID("group-split-" + workflowID.String())
		}
	}

	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if workflowGraphSaveBlockerCount(preview.Blockers, "active_transition_contract_changed") != 0 {
		t.Fatalf("preview = %+v, want no active-source transition blocker", preview)
	}
}

func TestWorkflowGraphSaveAllowsReassigningExistingTransitionGroupToActiveSource(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	agentID := workflow.NodeID("node-agent-" + workflowID.String())
	spareAgentID := workflow.NodeID("node-spare-agent-" + workflowID.String())
	spareDoneID := workflow.NodeID("node-spare-done-" + workflowID.String())
	spareRouteGroupID := workflow.TransitionGroupID("group-spare-route-" + workflowID.String())
	spareKeepGroupID := workflow.TransitionGroupID("group-spare-keep-" + workflowID.String())
	reassignedGroupID := workflow.TransitionGroupID("group-spare-reassigned-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: spareAgentID, WorkflowID: workflowID, Key: "spare_agent", Kind: workflow.NodeKindAgent, DisplayName: "Spare Agent", SubagentRole: "coder"},
			NodeRecord{ID: spareDoneID, WorkflowID: workflowID, Key: "spare_done", Kind: workflow.NodeKindTerminal, DisplayName: "Spare Done"},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: spareRouteGroupID, WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "spare", DisplayName: "Spare"},
			TransitionGroupRecord{ID: spareKeepGroupID, WorkflowID: workflowID, SourceNodeID: spareAgentID, TransitionID: "spare_done", DisplayName: "Done"},
			TransitionGroupRecord{ID: reassignedGroupID, WorkflowID: workflowID, SourceNodeID: spareAgentID, TransitionID: "spare_reassigned", DisplayName: "Spare Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-spare-route-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: spareRouteGroupID, Key: "spare", TargetNodeID: spareAgentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do spare work."},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-keep-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: spareKeepGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-reassigned-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: reassignedGroupID, Key: "spare_done", TargetNodeID: spareDoneID, ContextMode: workflow.ContextModeNewSession},
		)
	})
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)

	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	for index := range req.TransitionGroups {
		if req.TransitionGroups[index].ID == reassignedGroupID {
			req.TransitionGroups[index].SourceNodeID = agentID
		}
	}

	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if workflowGraphSaveBlockerCount(preview.Blockers, "active_transition_contract_changed") != 0 {
		t.Fatalf("preview = %+v, want no active-source transition blocker", preview)
	}
}

func TestWorkflowGraphSaveCommitRejectsVersionChangedDuringPreparation(t *testing.T) {
	ctx, store, _, cfg := newTestStoreWithConfigContext(t)
	remote, _ := openConcurrentWorkflowStores(t, cfg)
	remote.roleResolver = testsetup.QuestionsEnabled("coder", "reviewer")
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	local := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	local.Nodes = renameWorkflowGraphSaveNode(local.Nodes, workflow.NodeID("node-agent-"+workflowID.String()), "Local agent")
	resolver := &blockingGraphSaveRoleResolver{started: make(chan struct{}), release: make(chan struct{})}
	store.roleResolver = resolver
	type saveResult struct {
		result WorkflowGraphSaveResult
		err    error
	}
	localDone := make(chan saveResult, 1)
	go func() {
		result, err := store.SaveWorkflowGraph(ctx, local)
		localDone <- saveResult{result: result, err: err}
	}()
	<-resolver.started

	remoteRequest := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	remoteRequest.Nodes = renameWorkflowGraphSaveNode(remoteRequest.Nodes, workflow.NodeID("node-agent-"+workflowID.String()), "Remote agent")
	remoteResult, err := remote.SaveWorkflowGraph(ctx, remoteRequest)
	if err != nil {
		t.Fatalf("remote SaveWorkflowGraph: %v", err)
	}
	if !remoteResult.Saved || remoteResult.Version != record.Version+1 {
		t.Fatalf("remote save = %+v, want committed next version", remoteResult)
	}

	close(resolver.release)
	outcome := <-localDone
	if outcome.err != nil {
		t.Fatalf("local SaveWorkflowGraph: %v", outcome.err)
	}
	if outcome.result.Saved || workflowGraphSaveBlockerCount(outcome.result.Blockers, "version_changed") != remoteResult.Version {
		t.Fatalf("local save after remote version race = %+v, want version_changed", outcome.result)
	}
	current, currentRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after version race: %v", err)
	}
	if currentRecord.Version != remoteResult.Version || workflow.NodeDisplayName(nodeByID(t, current, workflow.NodeID("node-agent-"+workflowID.String()))) != "Remote agent" {
		t.Fatalf("definition after version race = record=%+v definition=%+v, want only remote save", currentRecord, current)
	}
}

func TestWorkflowGraphSaveCommitRejectsChangedConfirmationImpactDuringPreparation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	activeStore, _ := openConcurrentWorkflowStores(t, cfg)
	activeStore.roleResolver = testsetup.QuestionsEnabled("coder", "reviewer")
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	agentID := workflow.NodeID("node-agent-" + workflowID.String())
	spareDoneID := workflow.NodeID("node-spare-done-" + workflowID.String())
	spareGroupID := workflow.TransitionGroupID("group-spare-done-" + workflowID.String())
	spareEdgeID := workflow.EdgeID("edge-spare-done-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes, NodeRecord{ID: spareDoneID, WorkflowID: workflowID, Key: "spare_done", Kind: workflow.NodeKindTerminal, DisplayName: "Spare Done"})
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{ID: spareGroupID, WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "spare_done", DisplayName: "Spare Done"})
		req.Edges = append(req.Edges, EdgeRecord{ID: spareEdgeID, WorkflowID: workflowID, TransitionGroupID: spareGroupID, Key: "spare_done", TargetNodeID: spareDoneID, ContextMode: workflow.ContextModeNewSession})
	})
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
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if preview.Impact.NodeTaskReferenceCount != 0 || preview.Impact.EdgeTaskReferenceCount != 0 {
		t.Fatalf("preview impact = %+v, want no task references before race", preview.Impact)
	}

	resolver := &blockingGraphSaveRoleResolver{started: make(chan struct{}), release: make(chan struct{})}
	store.roleResolver = resolver
	type saveResult struct {
		result WorkflowGraphSaveResult
		err    error
	}
	saved := make(chan saveResult, 1)
	go func() {
		result, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(req, preview.Impact))
		saved <- saveResult{result: result, err: err}
	}()
	<-resolver.started

	task := createDefaultTask(t, ctx, activeStore, binding.ProjectID)
	started := startTask(t, ctx, activeStore, task.ID)
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want one current node", started.Mutation)
	}
	if _, err := activeStore.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "spare_done",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	close(resolver.release)

	outcome := <-saved
	if outcome.err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", outcome.err)
	}
	if outcome.result.Saved || workflowGraphSaveBlockerCount(outcome.result.Blockers, "impact_changed") != 1 {
		t.Fatalf("save after confirmation-impact race = %+v, want impact_changed", outcome.result)
	}
	wantImpactChangedEntities := []WorkflowGraphEntityReference{
		{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(spareEdgeID)},
		{EntityType: WorkflowGraphEntityTypeNode, EntityID: string(spareDoneID)},
		{EntityType: WorkflowGraphEntityTypeTransitionGroup, EntityID: string(spareGroupID)},
	}
	if got := workflowGraphSaveBlockerEntities(outcome.result.Blockers, "impact_changed"); !slices.Equal(got, wantImpactChangedEntities) {
		t.Fatalf("impact_changed affected entities = %+v, want %+v", got, wantImpactChangedEntities)
	}
	if outcome.result.Impact.NodeTaskReferenceCount == 0 {
		t.Fatalf("save result impact = %+v, want authoritative new task reference", outcome.result.Impact)
	}
}

func TestWorkflowGraphSaveCommitIgnoresUnrelatedTaskMetadataDuringPreparation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	taskStore, _ := openConcurrentWorkflowStores(t, cfg)
	taskStore.roleResolver = testsetup.QuestionsEnabled("coder", "reviewer")
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, taskStore, binding.ProjectID)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+workflowID.String()), "Renamed agent")
	resolver := &blockingGraphSaveRoleResolver{started: make(chan struct{}), release: make(chan struct{})}
	store.roleResolver = resolver
	type saveResult struct {
		result WorkflowGraphSaveResult
		err    error
	}
	saved := make(chan saveResult, 1)
	go func() {
		result, err := store.SaveWorkflowGraph(ctx, req)
		saved <- saveResult{result: result, err: err}
	}()
	<-resolver.started

	title := "Task metadata changed"
	if _, err := taskStore.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &title}); err != nil {
		t.Fatalf("UpdateTask unrelated metadata: %v", err)
	}
	close(resolver.release)

	outcome := <-saved
	if outcome.err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", outcome.err)
	}
	if !outcome.result.Saved || outcome.result.Version != record.Version+1 || len(outcome.result.Blockers) != 0 {
		t.Fatalf("save after unrelated task metadata = %+v, want committed graph", outcome.result)
	}
}

func TestWorkflowGraphSaveAllowsRemovingCompletedSessionNode(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	agentID := workflow.NodeID("node-agent-" + workflowID.String())
	reviewID := workflow.NodeID("node-review-" + workflowID.String())
	reviewDoneGroupID := workflow.TransitionGroupID("group-review-done-" + workflowID.String())
	reviewDoneEdgeID := workflow.EdgeID("edge-review-done-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes, NodeRecord{
			ID: reviewID, WorkflowID: workflowID, Key: "review", Kind: workflow.NodeKindAgent,
			DisplayName: "Review", SubagentRole: "reviewer",
		})
		for index := range req.Edges {
			if req.Edges[index].ID == workflow.EdgeID("edge-done-"+workflowID.String()) {
				req.Edges[index].TargetNodeID = reviewID
				req.Edges[index].PromptTemplate = "Review the work."
			}
		}
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID: reviewDoneGroupID, WorkflowID: workflowID, SourceNodeID: reviewID,
			TransitionID: "finish", DisplayName: "Done",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID: reviewDoneEdgeID, WorkflowID: workflowID, TransitionGroupID: reviewDoneGroupID,
			Key: "finish", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession,
		})
	})
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	agentReference := started.Mutation.Created[0].Reference
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  agentReference,
		AssociatedAt: associatedAt,
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       agentReference,
		TransitionID: "done",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Created) != 1 {
		t.Fatalf("agent completion mutation = %+v, want one review Current Node", completed.Mutation)
	}
	reviewReference := completed.Mutation.Created[0].Reference
	if reviewReference.NodeID != reviewID {
		t.Fatalf("agent completion target = %v, want review Node %q", reviewReference, reviewID)
	}
	completed, err = store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       reviewReference,
		TransitionID: "finish",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review: %v", err)
	}
	if len(completed.Mutation.Created) != 1 {
		t.Fatalf("review completion mutation = %+v, want one terminal Current Node", completed.Mutation)
	}
	terminalReference := completed.Mutation.Created[0].Reference

	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Nodes = removeWorkflowGraphSaveNode(req.Nodes, agentID)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, workflow.EdgeID("edge-done-"+workflowID.String()))
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-done-"+workflowID.String()))
	for index := range req.Edges {
		if req.Edges[index].ID == workflow.EdgeID("edge-start-"+workflowID.String()) {
			req.Edges[index].TargetNodeID = reviewID
			req.Edges[index].PromptTemplate = "Review the work."
		}
	}

	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	saved, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(req, preview.Impact))
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved {
		t.Fatalf("graph save = %+v, want completed Session node removal to succeed", saved)
	}
	savedDefinition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after graph save: %v", err)
	}
	for _, node := range savedDefinition.Nodes {
		if workflow.NodeIDOf(node) == agentID {
			t.Fatalf("saved graph retained removed Node %q", agentID)
		}
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(terminalReference) {
		t.Fatalf("Current Nodes after graph save = %+v, want terminal %v", currentNodes, terminalReference)
	}
	taskOwner, err := store.TaskIDForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("TaskIDForSession: %v", err)
	}
	if taskOwner == nil || *taskOwner != task.ID {
		t.Fatalf("retained Session owner = %v, want Task %q", taskOwner, task.ID)
	}
	association, err := store.LatestTaskSessionForNode(ctx, agentReference)
	if err != nil {
		t.Fatalf("LatestTaskSessionForNode after node removal: %v", err)
	}
	if association.SessionID != sessionID ||
		!association.CurrentNode.Equal(agentReference) ||
		!association.AssociatedAt.Equal(associatedAt) {
		t.Fatalf("historical association = %+v, want Session %q and removed Node %q", association, sessionID, agentID)
	}
}

func TestWorkflowGraphSaveAppliesExpectedRevisionAndRemovalConfirmation(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	unconfirmed := f.request(f.record.Version, false, f.def)
	unconfirmed.Edges = removeWorkflowGraphSaveEdge(unconfirmed.Edges, workflow.EdgeID("edge-done-"+f.workflowID.String()))
	unconfirmed.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(unconfirmed.TransitionGroups, workflow.TransitionGroupID("group-done-"+f.workflowID.String()))
	blocked := f.save(t, unconfirmed)
	if blocked.Saved || workflowGraphSaveBlockerCount(blocked.Blockers, "confirmation_required") != 2 {
		t.Fatalf("unconfirmed graph save = %+v, want confirmation blocker for one removed edge and transition group", blocked)
	}
	_, unchanged := f.current(t)
	if unchanged.Version != f.record.Version {
		t.Fatalf("workflow version after unconfirmed save = %d, want %d", unchanged.Version, f.record.Version)
	}

	confirmed := unconfirmed
	confirmed = confirmWorkflowGraphSaveRequest(confirmed, blocked.Impact)
	saved := f.save(t, confirmed)
	if !saved.Saved || len(saved.Blockers) != 0 || saved.Version != f.record.Version+1 {
		t.Fatalf("confirmed graph save = %+v, want single revision bump", saved)
	}
	updatedDef, updatedRecord := f.current(t)
	if updatedRecord.Version != f.record.Version+1 || len(updatedDef.Edges) != len(f.def.Edges)-1 {
		t.Fatalf("updated graph = revision %d edges %d, want revision %d edges %d", updatedRecord.Version, len(updatedDef.Edges), f.record.Version+1, len(f.def.Edges)-1)
	}

	stale := f.request(f.record.Version, true, updatedDef)
	stalePreview := f.preview(t, stale)
	if workflowGraphSaveBlockerCount(stalePreview.Blockers, "version_changed") != updatedRecord.Version {
		t.Fatalf("stale no-op preview = %+v, want current version blocker", stalePreview)
	}
	if got := workflowGraphSaveBlockerEntities(stalePreview.Blockers, "version_changed"); got == nil || len(got) != 0 {
		t.Fatalf("stale no-op preview affected entities = %+v, want present empty collection", got)
	}
	staleResult := f.save(t, stale)
	if staleResult.Saved || workflowGraphSaveBlockerCount(staleResult.Blockers, "version_changed") != updatedRecord.Version {
		t.Fatalf("stale no-op save = %+v, want current version blocker", staleResult)
	}
}

func TestWorkflowGraphSaveConfirmationIncludesNodeGroupCountAndImpactChangedEntities(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	nodeGroupID := "group-empty-" + f.workflowID.String()
	if _, _, err := f.store.AddNodeGroup(f.ctx, NodeGroupRecord{
		ID:          nodeGroupID,
		WorkflowID:  f.workflowID,
		Key:         "empty",
		DisplayName: "Empty",
	}); err != nil {
		t.Fatalf("AddNodeGroup: %v", err)
	}
	current, record := f.current(t)
	edgeID := workflow.EdgeID("edge-done-" + f.workflowID.String())
	transitionGroupID := workflow.TransitionGroupID("group-done-" + f.workflowID.String())
	req := f.request(record.Version, false, current)
	req.NodeGroups = nil
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, edgeID)
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, transitionGroupID)

	preview := f.preview(t, req)
	if preview.Impact.RemovedNodeGroupCount != 1 {
		t.Fatalf("preview impact = %+v, want one removed Node Group", preview.Impact)
	}
	wantConfirmationEntities := []WorkflowGraphEntityReference{
		{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(edgeID)},
		{EntityType: WorkflowGraphEntityTypeTransitionGroup, EntityID: string(transitionGroupID)},
	}
	if got := workflowGraphSaveBlockerEntities(preview.Blockers, "confirmation_required"); !slices.Equal(got, wantConfirmationEntities) {
		t.Fatalf("confirmation_required affected entities = %+v, want %+v", got, wantConfirmationEntities)
	}

	wrong := confirmWorkflowGraphSaveRequest(req, preview.Impact)
	wrong.ExpectedRemovedNodeGroupCount--
	blocked := f.save(t, wrong)
	wantImpactChangedEntities := []WorkflowGraphEntityReference{
		{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(edgeID)},
		{EntityType: WorkflowGraphEntityTypeNodeGroup, EntityID: nodeGroupID},
		{EntityType: WorkflowGraphEntityTypeTransitionGroup, EntityID: string(transitionGroupID)},
	}
	if blocked.Saved {
		t.Fatalf("save with stale Node Group count = %+v, want blocked", blocked)
	}
	if got := workflowGraphSaveBlockerEntities(blocked.Blockers, "impact_changed"); !slices.Equal(got, wantImpactChangedEntities) {
		t.Fatalf("impact_changed affected entities = %+v, want %+v", got, wantImpactChangedEntities)
	}

	saved := f.save(t, confirmWorkflowGraphSaveRequest(req, preview.Impact))
	if !saved.Saved || !saved.Changed {
		t.Fatalf("save with current Node Group count = %+v, want changed save", saved)
	}
}

func TestWorkflowGraphSaveSupportsMetadataAndNoopRevisions(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	metadataOnly := f.request(f.record.Version, false, f.def)
	metadataOnly.Metadata = &WorkflowGraphSaveMetadata{Name: "Renamed workflow", Description: "Updated description"}
	metadataSaved := f.save(t, metadataOnly)
	if !metadataSaved.Saved || metadataSaved.Version != f.record.Version+1 {
		t.Fatalf("metadata-only save = %+v, want version bump", metadataSaved)
	}
	_, afterMetadata := f.current(t)
	if afterMetadata.Name != "Renamed workflow" || afterMetadata.Description != "Updated description" || afterMetadata.Version != f.record.Version+1 {
		t.Fatalf("record after metadata-only = %+v, want metadata persisted with version bump", afterMetadata)
	}

	noop := f.request(afterMetadata.Version, false, f.def)
	noop.Metadata = &WorkflowGraphSaveMetadata{Name: afterMetadata.Name, Description: afterMetadata.Description}
	noopSaved := f.save(t, noop)
	if !noopSaved.Saved || noopSaved.Changed || noopSaved.Version != afterMetadata.Version {
		t.Fatalf("noop save = %+v, want no revision bump", noopSaved)
	}
}

func TestWorkflowGraphSaveRoundTripsExecutionTargetPolicy(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	if f.record.ExecutionTargetPolicy.Mode != workflow.ExecutionTargetModeAskOnFirstExecution || f.def.ExecutionTargetPolicy.Mode != workflow.ExecutionTargetModeAskOnFirstExecution {
		t.Fatalf("created workflow policy = record=%+v definition=%+v, want ask_on_first_execution", f.record.ExecutionTargetPolicy, f.def.ExecutionTargetPolicy)
	}

	customRef := "refs/tags/v1"
	custom := f.request(f.record.Version, false, f.def)
	custom.Metadata = &WorkflowGraphSaveMetadata{
		Name:                  f.record.Name,
		Description:           f.record.Description,
		ExecutionTargetPolicy: &workflow.ExecutionTargetPolicy{Mode: workflow.ExecutionTargetModeCustomRef, CustomRef: &customRef},
	}
	customPreview := f.preview(t, custom)
	if !customPreview.CanSave || customPreview.Version != f.record.Version {
		t.Fatalf("custom policy preview = %+v, want saveable unchanged revision", customPreview)
	}
	customSaved := f.save(t, custom)
	if !customSaved.Saved || !customSaved.Changed || customSaved.Version != f.record.Version+1 {
		t.Fatalf("custom policy save = %+v, want one metadata revision", customSaved)
	}
	updated, updatedRecord := f.current(t)
	if updated.ExecutionTargetPolicy.Mode != workflow.ExecutionTargetModeCustomRef || updated.ExecutionTargetPolicy.CustomRef == nil || *updated.ExecutionTargetPolicy.CustomRef != customRef ||
		updatedRecord.ExecutionTargetPolicy != updated.ExecutionTargetPolicy {
		t.Fatalf("custom policy did not round-trip: definition=%+v record=%+v", updated.ExecutionTargetPolicy, updatedRecord.ExecutionTargetPolicy)
	}

	combined := f.request(updatedRecord.Version, false, updated)
	combined.Nodes = renameWorkflowGraphSaveNode(combined.Nodes, workflow.NodeID("node-agent-"+f.workflowID.String()), "Renamed agent")
	combined.Metadata = &WorkflowGraphSaveMetadata{
		Name:                  updatedRecord.Name,
		Description:           updatedRecord.Description,
		ExecutionTargetPolicy: &workflow.ExecutionTargetPolicy{Mode: workflow.ExecutionTargetModeHead},
	}
	combinedSaved := f.save(t, combined)
	if !combinedSaved.Saved || combinedSaved.Version != updatedRecord.Version+1 {
		t.Fatalf("combined policy/graph save = %+v, want exactly one version increment", combinedSaved)
	}
	afterCombined, afterCombinedRecord := f.current(t)
	if afterCombined.ExecutionTargetPolicy.Mode != workflow.ExecutionTargetModeHead || afterCombined.ExecutionTargetPolicy.CustomRef != nil ||
		afterCombinedRecord.ExecutionTargetPolicy != afterCombined.ExecutionTargetPolicy {
		t.Fatalf("non-custom policy should clear custom ref: definition=%+v record=%+v", afterCombined.ExecutionTargetPolicy, afterCombinedRecord.ExecutionTargetPolicy)
	}

	noop := f.request(afterCombinedRecord.Version, false, afterCombined)
	noop.Metadata = &WorkflowGraphSaveMetadata{
		Name:                  afterCombinedRecord.Name,
		Description:           afterCombinedRecord.Description,
		ExecutionTargetPolicy: &afterCombinedRecord.ExecutionTargetPolicy,
	}
	noopSaved := f.save(t, noop)
	if !noopSaved.Saved || noopSaved.Changed || noopSaved.Version != afterCombinedRecord.Version {
		t.Fatalf("policy noop = %+v, want unchanged workflow", noopSaved)
	}

	incomplete := f.request(afterCombinedRecord.Version, false, afterCombined)
	incomplete.Metadata = &WorkflowGraphSaveMetadata{
		Name:                  afterCombinedRecord.Name,
		Description:           afterCombinedRecord.Description,
		ExecutionTargetPolicy: &workflow.ExecutionTargetPolicy{Mode: workflow.ExecutionTargetModeCustomRef},
	}
	incompletePreview := f.preview(t, incomplete)
	if !incompletePreview.CanSave || !hasWorkflowValidationCode(incompletePreview.ValidationErrors, workflow.CodeExecutionTargetCustomRefRequired) {
		t.Fatalf("incomplete custom ref preview = %+v, want saveable semantic validation", incompletePreview)
	}
	incompleteSaved := f.save(t, incomplete)
	if !incompleteSaved.Saved || incompleteSaved.Version != afterCombinedRecord.Version+1 {
		t.Fatalf("incomplete custom ref save = %+v, want one metadata revision", incompleteSaved)
	}
}

func hasWorkflowValidationCode(errors []workflow.ValidationError, want workflow.ValidationErrorCode) bool {
	for _, err := range errors {
		if err.Code == want {
			return true
		}
	}
	return false
}

func TestWorkflowGraphSavePersistsScriptPathOnlyEdit(t *testing.T) {
	f := newGraphSaveFixture(t, func(t *testing.T, ctx context.Context, store *Store) runtimeids.WorkflowID {
		return createScriptStartWorkflow(t, ctx, store, "scripts/old")
	})
	scriptID := workflow.NodeID("node-script-" + f.workflowID.String())
	req := f.request(f.record.Version, false, f.def)
	for index := range req.Nodes {
		if req.Nodes[index].ID == scriptID {
			req.Nodes[index].ScriptPath = "scripts/new"
		}
	}

	saved := f.save(t, req)
	if !saved.Saved || !saved.Changed || saved.Version != f.record.Version+1 {
		t.Fatalf("save result = %+v, want script-path graph change with version bump", saved)
	}
	updatedDef, updatedRecord := f.current(t)
	if updatedRecord.Version != f.record.Version+1 {
		t.Fatalf("updated version = %d, want %d", updatedRecord.Version, f.record.Version+1)
	}
	path, ok := workflow.NodeScriptPath(nodeByID(t, updatedDef, scriptID)).Value()
	if !ok || path != "scripts/new" {
		t.Fatalf("script path = %q/%t, want scripts/new", path, ok)
	}
}

func TestWorkflowGraphSaveRoundTripsTransitionInvocationContract(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	req := f.request(f.record.Version, false, f.def)
	for i := range req.Edges {
		switch req.Edges[i].Key {
		case "start":
			req.Edges[i].PromptTemplate = "Start from {{.TaskTitle}}."
		case "done":
			req.Edges[i].Parameters = []workflow.Parameter{{Key: "summary", Description: "Summary for terminal history.", Purpose: workflow.ParameterPurposeOrdinary}}
		}
	}
	saved := f.save(t, req)
	if !saved.Saved || !saved.Changed || saved.Version != f.record.Version+1 {
		t.Fatalf("save result = %+v, want graph change with version bump", saved)
	}

	updated, updatedRecord := f.current(t)
	startEdge := edgeByKey(t, updated, "start")
	if startEdge.PromptTemplate != "Start from {{.TaskTitle}}." {
		t.Fatalf("start edge prompt = %q, want transition prompt round-tripped", startEdge.PromptTemplate)
	}
	doneEdge := edgeByKey(t, updated, "done")
	if len(doneEdge.Parameters) != 1 || doneEdge.Parameters[0].Key != "summary" || doneEdge.Parameters[0].Description != "Summary for terminal history." {
		t.Fatalf("done edge parameters = %+v, want transition parameters round-tripped", doneEdge.Parameters)
	}

	noop := f.request(updatedRecord.Version, false, updated)
	noopSaved := f.save(t, noop)
	if !noopSaved.Saved || noopSaved.Changed || noopSaved.Version != updatedRecord.Version {
		t.Fatalf("noop save = %+v, want prompt/parameters preserved as unchanged graph", noopSaved)
	}
}

func TestWorkflowGraphSaveRoundTripsEdgeSelectionModesAndParameterPurposes(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	req := f.request(f.record.Version, false, f.def)
	for index := range req.Edges {
		if req.Edges[index].Key != "done" {
			continue
		}
		req.Edges[index].AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		req.Edges[index].ThinkingSelection = workflow.ThinkingSelectionPreviousNode
		req.Edges[index].Parameters = []workflow.Parameter{
			{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee},
			{Key: "thinking", Purpose: workflow.ParameterPurposeTargetThinking},
			{Key: "summary", Description: "Summary.", Purpose: workflow.ParameterPurposeOrdinary},
		}
	}
	saved := f.save(t, req)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("save result = %+v, want changed graph", saved)
	}
	updated, _ := f.current(t)
	edge := edgeByKey(t, updated, "done")
	if edge.AssigneeSelection != workflow.AssigneeSelectionPreviousNode || edge.ThinkingSelection != workflow.ThinkingSelectionPreviousNode {
		t.Fatalf("edge selections = %q/%q", edge.AssigneeSelection, edge.ThinkingSelection)
	}
	if len(edge.Parameters) != 3 || edge.Parameters[0].Purpose != workflow.ParameterPurposeTargetAssignee || edge.Parameters[1].Purpose != workflow.ParameterPurposeTargetThinking || edge.Parameters[2].Purpose != workflow.ParameterPurposeOrdinary {
		t.Fatalf("edge parameters = %+v", edge.Parameters)
	}
}

func TestWorkflowGraphSavePersistsSemanticallyInapplicableSelectorForDraftFixup(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	req := f.request(f.record.Version, false, f.def)
	for index := range req.Edges {
		if req.Edges[index].Key != "done" {
			continue
		}
		req.Edges[index].AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		req.Edges[index].Parameters = []workflow.Parameter{{
			Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	}
	saved := f.save(t, req)
	if !saved.Saved || len(saved.Blockers) != 0 {
		t.Fatalf("semantically inapplicable selector save = %+v, want saved draft", saved)
	}
	foundDiagnostic := false
	for _, diagnostic := range saved.ValidationErrors {
		if diagnostic.Code == workflow.CodeAssigneeSelectionInapplicable {
			foundDiagnostic = true
			if diagnostic.BlocksContext {
				t.Fatal("draft selector applicability diagnostic blocks save")
			}
		}
	}
	if !foundDiagnostic {
		t.Fatalf("save diagnostics = %+v, want Assignee applicability diagnostic", saved.ValidationErrors)
	}
	updated, _ := f.current(t)
	edge := edgeByKey(t, updated, "done")
	if edge.AssigneeSelection != workflow.AssigneeSelectionPreviousNode ||
		len(edge.Parameters) != 1 ||
		edge.Parameters[0].Purpose != workflow.ParameterPurposeTargetAssignee {
		t.Fatalf("reloaded draft edge = %+v, want selector state preserved", edge)
	}
}

func TestWorkflowGraphSaveAcceptsClientGeneratedTopologyIDsAndRejectsCollisions(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	req := f.request(f.record.Version, false, f.def)
	req.Nodes = append(req.Nodes, NodeRecord{
		ID:           "workflow-node-00000000-0000-4000-8000-000000000001",
		WorkflowID:   f.workflowID,
		Key:          "client_generated",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Client Generated",
		SubagentRole: workflow.DefaultAgentRole,
	})
	req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
		ID:           "workflow-transition-group-00000000-0000-4000-8000-000000000001",
		WorkflowID:   f.workflowID,
		SourceNodeID: workflow.NodeID("node-agent-" + f.workflowID.String()),
		TransitionID: "client_generated",
		DisplayName:  "Client Generated",
	})
	req.Edges = append(req.Edges, EdgeRecord{
		ID:                "workflow-edge-00000000-0000-4000-8000-000000000001",
		WorkflowID:        f.workflowID,
		TransitionGroupID: "workflow-transition-group-00000000-0000-4000-8000-000000000001",
		Key:               "client_generated",
		TargetNodeID:      "workflow-node-00000000-0000-4000-8000-000000000001",
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Client generated.",
	})

	saved := f.save(t, req)
	if !saved.Saved || len(saved.Blockers) != 0 {
		t.Fatalf("client id graph save = %+v, want saved without blockers", saved)
	}
	updated, _ := f.current(t)
	if workflow.NodeKey(nodeByID(t, updated, "workflow-node-00000000-0000-4000-8000-000000000001")) != "client_generated" {
		t.Fatalf("client-generated node id was not persisted")
	}

	colliding := f.request(saved.Version, false, updated)
	colliding.Nodes = append(colliding.Nodes, colliding.Nodes[len(colliding.Nodes)-1])
	collidingResult := f.save(t, colliding)
	if collidingResult.Saved || workflowGraphSaveBlockerCount(collidingResult.Blockers, "validation_failed") == 0 {
		t.Fatalf("duplicate client id graph save = %+v, want validation blocker", collidingResult)
	}
}

func TestWorkflowGraphSaveMetadataAndGraphAreAtomic(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	agentID := workflow.NodeID("node-agent-" + f.workflowID.String())

	combined := f.request(f.record.Version, false, f.def)
	combined.Metadata = &WorkflowGraphSaveMetadata{Name: "Combined save", Description: "Graph and metadata"}
	combined.Nodes = renameWorkflowGraphSaveNode(combined.Nodes, agentID, "Edited Agent")
	saved := f.save(t, combined)
	if !saved.Saved || saved.Version != f.record.Version+1 {
		t.Fatalf("combined save = %+v, want one version bump", saved)
	}
	updatedDef, updatedRecord := f.current(t)
	if updatedRecord.Name != "Combined save" || workflow.NodeDisplayName(nodeByID(t, updatedDef, agentID)) != "Edited Agent" {
		t.Fatalf("combined save persisted record=%+v node=%+v", updatedRecord, nodeByID(t, updatedDef, agentID))
	}

	blocked := f.request(updatedRecord.Version, false, updatedDef)
	blocked.Metadata = &WorkflowGraphSaveMetadata{Name: "Must not persist", Description: "Blocked"}
	blocked.Edges = removeWorkflowGraphSaveEdge(blocked.Edges, workflow.EdgeID("edge-done-"+f.workflowID.String()))
	blockedResult := f.save(t, blocked)
	if blockedResult.Saved || workflowGraphSaveBlockerCount(blockedResult.Blockers, "confirmation_required") == 0 {
		t.Fatalf("blocked combined save = %+v, want confirmation blocker", blockedResult)
	}
	_, unchangedRecord := f.current(t)
	if unchangedRecord.Name != "Combined save" || unchangedRecord.Description != "Graph and metadata" || unchangedRecord.Version != updatedRecord.Version {
		t.Fatalf("record after blocked combined = %+v, want unchanged", unchangedRecord)
	}
}

func TestWorkflowGraphSaveValidatesAndPersistsV1NodeGroups(t *testing.T) {
	f := newGraphSaveFixture(t, createFanoutJoinWorkflow)
	groupID := "group-parallel-" + f.workflowID.String()
	req := f.request(f.record.Version, false, f.def)
	req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: groupID, WorkflowID: f.workflowID, Key: "parallel", DisplayName: "Parallel"})
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-a-"+f.workflowID.String()), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-b-"+f.workflowID.String()), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-join-"+f.workflowID.String()), groupID)

	result := f.save(t, req)
	if !result.Saved || len(result.Blockers) != 0 {
		t.Fatalf("valid node group graph save = %+v, want saved", result)
	}
	savedDef, savedRecord := f.current(t)
	if savedRecord.Version != f.record.Version+1 {
		t.Fatalf("valid node group version = %d, want %d", savedRecord.Version, f.record.Version+1)
	}
	if len(savedDef.NodeGroups) != 1 || len(savedDef.NodeGroups[0].MemberNodeIDs) != 3 {
		t.Fatalf("saved node groups = %+v, want one group with three members", savedDef.NodeGroups)
	}
	if workflow.NodeGroupID(nodeByID(t, savedDef, workflow.NodeID("node-join-"+f.workflowID.String()))) != groupID {
		t.Fatalf("saved join group id not persisted: %+v", nodeByID(t, savedDef, workflow.NodeID("node-join-"+f.workflowID.String())))
	}

	invalid := f.request(savedRecord.Version, false, savedDef)
	invalid.Nodes = setWorkflowGraphSaveNodeGroup(invalid.Nodes, workflow.NodeID("node-impl-b-"+f.workflowID.String()), "")
	invalidResult := f.save(t, invalid)
	if invalidResult.Saved || workflowGraphSaveBlockerCount(invalidResult.Blockers, "validation_failed") == 0 {
		t.Fatalf("invalid node group graph save = %+v, want validation blocker", invalidResult)
	}
}

func TestWorkflowGraphSaveKeepsAuthoredCollectionOrderSignificant(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	for _, group := range []NodeGroupRecord{
		{ID: "group-first-" + f.workflowID.String(), WorkflowID: f.workflowID, Key: "first", DisplayName: "First", SortOrder: 50},
		{ID: "group-second-" + f.workflowID.String(), WorkflowID: f.workflowID, Key: "second", DisplayName: "Second", SortOrder: 150},
	} {
		if _, _, err := f.store.AddNodeGroup(f.ctx, group); err != nil {
			t.Fatalf("AddNodeGroup %q: %v", group.ID, err)
		}
	}
	current, record := f.current(t)

	tests := []struct {
		name    string
		reorder func(*WorkflowGraphSaveRequest)
	}{
		{
			name: "Node Groups",
			reorder: func(req *WorkflowGraphSaveRequest) {
				req.NodeGroups[0], req.NodeGroups[1] = req.NodeGroups[1], req.NodeGroups[0]
			},
		},
		{
			name: "Nodes",
			reorder: func(req *WorkflowGraphSaveRequest) {
				req.Nodes[0], req.Nodes[1] = req.Nodes[1], req.Nodes[0]
			},
		},
		{
			name: "Transition Groups",
			reorder: func(req *WorkflowGraphSaveRequest) {
				req.TransitionGroups[0], req.TransitionGroups[1] = req.TransitionGroups[1], req.TransitionGroups[0]
			},
		},
		{
			name: "Transition Branches",
			reorder: func(req *WorkflowGraphSaveRequest) {
				req.Edges[0], req.Edges[1] = req.Edges[1], req.Edges[0]
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := f.request(record.Version, false, current)
			test.reorder(&req)

			preview := f.preview(t, req)

			if !preview.Changed {
				t.Fatalf("preview Changed = false, want reordered %s to remain a graph change", test.name)
			}
		})
	}
}

func TestWorkflowGraphSavePreservesNodeGroupOrderAndAllUniquenessKeySwaps(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	for _, group := range []NodeGroupRecord{
		{ID: "group-first-" + f.workflowID.String(), WorkflowID: f.workflowID, Key: "first", DisplayName: "First", SortOrder: 50},
		{ID: "group-second-" + f.workflowID.String(), WorkflowID: f.workflowID, Key: "second", DisplayName: "Second", SortOrder: 150},
	} {
		if _, _, err := f.store.AddNodeGroup(f.ctx, group); err != nil {
			t.Fatalf("AddNodeGroup %q: %v", group.ID, err)
		}
	}
	current, record := f.current(t)
	req := f.request(record.Version, false, current)
	req.NodeGroups[0], req.NodeGroups[1] = req.NodeGroups[1], req.NodeGroups[0]

	plan, err := f.store.prepareWorkflowGraphSave(f.ctx, req)
	if err != nil {
		t.Fatalf("prepareWorkflowGraphSave: %v", err)
	}
	if !plan.GraphChanged {
		t.Fatal("reordered Node Groups reported an unchanged graph")
	}
	if got := []int64{plan.Prepared.nodeGroups[0].SortOrder, plan.Prepared.nodeGroups[1].SortOrder}; !slices.Equal(got, []int64{0, 100}) {
		t.Fatalf("prepared Node Group sort order = %v, want authored collection order", got)
	}
	currentGraph := plan.current
	currentGraph.transitionGroups[1].SourceNodeID = currentGraph.transitionGroups[0].SourceNodeID
	if err := upsertWorkflowTransitionGroup(f.ctx, f.store.queries, currentGraph.transitionGroups[1], currentGraph.transitionGroups[1].SortOrder, "seed shared transition source"); err != nil {
		t.Fatal(err)
	}
	currentGraph.edges[1].TransitionGroupID = currentGraph.edges[0].TransitionGroupID
	if err := upsertWorkflowEdge(f.ctx, f.store.queries, currentGraph.edges[1], currentGraph.edges[1].SortOrder, "seed shared edge group"); err != nil {
		t.Fatal(err)
	}
	prepared := plan.Prepared
	prepared.nodeGroups[0].Key, prepared.nodeGroups[1].Key = prepared.nodeGroups[1].Key, prepared.nodeGroups[0].Key
	prepared.nodes[0].Key, prepared.nodes[1].Key = prepared.nodes[1].Key, prepared.nodes[0].Key
	prepared.transitionGroups[1].SourceNodeID = currentGraph.transitionGroups[1].SourceNodeID
	prepared.transitionGroups[0].TransitionID, prepared.transitionGroups[1].TransitionID = prepared.transitionGroups[1].TransitionID, prepared.transitionGroups[0].TransitionID
	prepared.edges[1].TransitionGroupID = currentGraph.edges[1].TransitionGroupID
	prepared.edges[0].Key, prepared.edges[1].Key = prepared.edges[1].Key, prepared.edges[0].Key
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := applyWorkflowGraphSave(f.ctx, f.store.queries.WithTx(tx), f.workflowID, currentGraph, prepared, removedWorkflowGraphRows{}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("applyWorkflowGraphSave: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	reloaded, err := currentWorkflowGraphSavePrepared(f.ctx, f.store.queries, f.workflowID)
	if err != nil {
		t.Fatalf("reload graph: %v", err)
	}
	if reloaded.nodeGroups[0].Key != prepared.nodeGroups[0].Key || reloaded.nodeGroups[1].Key != prepared.nodeGroups[1].Key || reloaded.nodes[0].Key != prepared.nodes[0].Key || reloaded.nodes[1].Key != prepared.nodes[1].Key || reloaded.transitionGroups[0].TransitionID != prepared.transitionGroups[0].TransitionID || reloaded.transitionGroups[1].TransitionID != prepared.transitionGroups[1].TransitionID || reloaded.edges[0].Key != prepared.edges[0].Key || reloaded.edges[1].Key != prepared.edges[1].Key {
		t.Fatal("reloaded graph uniqueness keys do not match the swapped graph")
	}
}

func TestWorkflowGraphSaveRejectsStaleVersion(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	if err := f.store.UpdateWorkflowInfo(f.ctx, f.workflowID, "Remote rename", "Remote description"); err != nil {
		t.Fatalf("UpdateWorkflowInfo: %v", err)
	}
	_, remote := f.current(t)

	stale := f.request(f.record.Version, false, f.def)
	stale.Metadata = &WorkflowGraphSaveMetadata{Name: "Local rename", Description: "Local description"}
	result := f.save(t, stale)
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "version_changed") != remote.Version {
		t.Fatalf("stale metadata save = %+v, want current version blocker", result)
	}
	_, unchanged := f.current(t)
	if unchanged.Name != "Remote rename" || unchanged.Version != remote.Version {
		t.Fatalf("record after stale metadata save = %+v, want remote metadata preserved", unchanged)
	}
}

func TestWorkflowGraphSaveChecksVersionBeforePreparingDraftRecords(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	if err := f.store.UpdateWorkflowInfo(f.ctx, f.workflowID, "Remote rename", "Remote description"); err != nil {
		t.Fatalf("UpdateWorkflowInfo: %v", err)
	}
	_, current := f.current(t)

	stale := f.request(f.record.Version, false, f.def)
	stale.Nodes[0].WorkflowID = runtimeids.WorkflowID{}
	preview := f.preview(t, stale)
	if workflowGraphSaveBlockerCount(preview.Blockers, "version_changed") != current.Version {
		t.Fatalf("stale preview = %+v, want current-version blocker", preview)
	}
	saved := f.save(t, stale)
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "version_changed") != current.Version {
		t.Fatalf("stale save = %+v, want current-version blocker", saved)
	}

	currentVersion := stale
	currentVersion.ExpectedVersion = current.Version
	if _, err := f.store.PreviewWorkflowGraphSave(f.ctx, currentVersion); !errors.Is(err, ErrWorkflowIDRequired) {
		t.Fatalf("current-version preview error = %v, want invalid nested Workflow identity", err)
	}
}

func TestPreviewWorkflowGraphSaveDoesNotMutateWithoutBlockers(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	agentID := workflow.NodeID("node-agent-" + f.workflowID.String())
	req := f.request(f.record.Version, false, f.def)
	req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, agentID, "Preview Agent")

	preview := f.preview(t, req)
	if preview.Saved || len(preview.Blockers) != 0 || !preview.CanSave {
		t.Fatalf("preview graph save = %+v, want non-mutating savable preview without blockers", preview)
	}
	unchangedDef, unchangedRecord := f.current(t)
	if unchangedRecord.Version != f.record.Version {
		t.Fatalf("workflow version after preview = %d, want %d", unchangedRecord.Version, f.record.Version)
	}
	if workflow.NodeDisplayName(nodeByID(t, unchangedDef, agentID)) == "Preview Agent" {
		t.Fatalf("preview mutated node display name")
	}
}
