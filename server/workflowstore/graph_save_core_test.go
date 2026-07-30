package workflowstore

import (
	"context"
	"sync"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/toolspec"
)

type graphSaveWorkflowFactory func(*testing.T, context.Context, *Store) workflow.WorkflowID

type graphSaveFixture struct {
	ctx        context.Context
	store      *Store
	workflowID workflow.WorkflowID
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

type blockingGraphSaveRoleResolver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingGraphSaveRoleResolver) RoleExists(string) bool {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return true
}

func (r *blockingGraphSaveRoleResolver) RoleToolEnabled(string, toolspec.ID) bool {
	return true
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
	request.Nodes = renameWorkflowGraphSaveNode(request.Nodes, workflow.NodeID("node-agent-"+string(f.workflowID)), "Renamed agent")

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

func TestWorkflowGraphSaveCommitRevalidatesDynamicPolicyImpact(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	activeStore, _ := openConcurrentWorkflowStores(t, cfg)
	activeStore.roleResolver = testsetup.QuestionsEnabled("coder", "reviewer")
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	spareDoneID := workflow.NodeID("node-spare-done-" + string(workflowID))
	spareGroupID := workflow.TransitionGroupID("group-spare-done-" + string(workflowID))
	spareEdgeID := workflow.EdgeID("edge-spare-done-" + string(workflowID))
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
	if outcome.result.Saved || workflowGraphSaveBlockerCount(outcome.result.Blockers, "active_transition_contract_changed") == 0 {
		t.Fatalf("save after dynamic policy race = %+v, want active_transition_contract_changed blocker", outcome.result)
	}
	if outcome.result.EditPolicyImpact.ActiveCurrentNodeCount != 1 {
		t.Fatalf("save result policy impact = %+v, want authoritative active Current Node count", outcome.result.EditPolicyImpact)
	}
}

func TestWorkflowGraphSaveAddingTransitionFromActiveSourceIsBlocked(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(workflow.WorkflowID, workflow.NodeID, workflow.NodeID, *WorkflowGraphSaveRequest)
	}{
		{
			name: "transition group",
			mutate: func(workflowID workflow.WorkflowID, agentID workflow.NodeID, spareDoneID workflow.NodeID, req *WorkflowGraphSaveRequest) {
				groupID := workflow.TransitionGroupID("group-added-" + string(workflowID))
				req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
					ID:           groupID,
					WorkflowID:   workflowID,
					SourceNodeID: agentID,
					TransitionID: "added",
					DisplayName:  "Added",
				})
				req.Edges = append(req.Edges, EdgeRecord{
					ID:                workflow.EdgeID("edge-added-" + string(workflowID)),
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
			mutate: func(workflowID workflow.WorkflowID, _ workflow.NodeID, spareDoneID workflow.NodeID, req *WorkflowGraphSaveRequest) {
				req.Edges = append(req.Edges, EdgeRecord{
					ID:                workflow.EdgeID("edge-added-" + string(workflowID)),
					WorkflowID:        workflowID,
					TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(workflowID)),
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
			agentID := workflow.NodeID("node-agent-" + string(workflowID))
			spareDoneID := workflow.NodeID("node-spare-done-" + string(workflowID))
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
			if workflowGraphSaveBlockerCount(preview.Blockers, "active_transition_contract_changed") == 0 {
				t.Fatalf("preview = %+v, want active_transition_contract_changed blocker", preview)
			}
		})
	}
}

func TestWorkflowGraphSaveReassigningExistingEdgeToActiveSourceIsBlocked(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	planID := workflow.NodeID("node-plan-" + string(workflowID))
	joinID := workflow.NodeID("node-join-" + string(workflowID))
	spareSourceID := workflow.NodeID("node-spare-source-" + string(workflowID))
	spareBranchAID := workflow.NodeID("node-spare-a-" + string(workflowID))
	spareBranchBID := workflow.NodeID("node-spare-b-" + string(workflowID))
	spareRouteGroupID := workflow.TransitionGroupID("group-spare-route-" + string(workflowID))
	spareSplitGroupID := workflow.TransitionGroupID("group-spare-split-" + string(workflowID))
	spareBranchAGroupID := workflow.TransitionGroupID("group-spare-a-" + string(workflowID))
	spareBranchBGroupID := workflow.TransitionGroupID("group-spare-b-" + string(workflowID))
	reassignedEdgeID := workflow.EdgeID("edge-spare-split-a-" + string(workflowID))
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: spareSourceID, WorkflowID: workflowID, Key: "spare_source", Kind: workflow.NodeKindAgent, DisplayName: "Spare Source", SubagentRole: "coder", PromptTemplate: "Route spare work."},
			NodeRecord{ID: spareBranchAID, WorkflowID: workflowID, Key: "spare_a", Kind: workflow.NodeKindAgent, DisplayName: "Spare A", SubagentRole: "coder", PromptTemplate: "Do spare A."},
			NodeRecord{ID: spareBranchBID, WorkflowID: workflowID, Key: "spare_b", Kind: workflow.NodeKindAgent, DisplayName: "Spare B", SubagentRole: "coder", PromptTemplate: "Do spare B."},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: spareRouteGroupID, WorkflowID: workflowID, SourceNodeID: planID, TransitionID: "spare", DisplayName: "Spare"},
			TransitionGroupRecord{ID: spareSplitGroupID, WorkflowID: workflowID, SourceNodeID: spareSourceID, TransitionID: "split", DisplayName: "Split"},
			TransitionGroupRecord{ID: spareBranchAGroupID, WorkflowID: workflowID, SourceNodeID: spareBranchAID, TransitionID: "join", DisplayName: "Join"},
			TransitionGroupRecord{ID: spareBranchBGroupID, WorkflowID: workflowID, SourceNodeID: spareBranchBID, TransitionID: "join", DisplayName: "Join"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-spare-route-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: spareRouteGroupID, Key: "spare", TargetNodeID: spareSourceID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Route spare work."},
			EdgeRecord{ID: reassignedEdgeID, WorkflowID: workflowID, TransitionGroupID: spareSplitGroupID, Key: "spare_a", TargetNodeID: spareBranchAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do spare A."},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-split-b-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: spareSplitGroupID, Key: "spare_b", TargetNodeID: spareBranchBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do spare B."},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-a-join-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: spareBranchAGroupID, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: workflow.EdgeID("edge-spare-b-join-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: spareBranchBGroupID, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
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
			req.Edges[index].TransitionGroupID = workflow.TransitionGroupID("group-split-" + string(workflowID))
		}
	}

	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if workflowGraphSaveBlockerCount(preview.Blockers, "active_transition_contract_changed") == 0 {
		t.Fatalf("preview = %+v, want active_transition_contract_changed blocker", preview)
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
	local.Nodes = renameWorkflowGraphSaveNode(local.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Local agent")
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
	remoteRequest.Nodes = renameWorkflowGraphSaveNode(remoteRequest.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Remote agent")
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
	if currentRecord.Version != remoteResult.Version || workflow.NodeDisplayName(nodeByID(t, current, workflow.NodeID("node-agent-"+string(workflowID)))) != "Remote agent" {
		t.Fatalf("definition after version race = record=%+v definition=%+v, want only remote save", currentRecord, current)
	}
}

func TestWorkflowGraphSaveCommitRejectsChangedConfirmationImpactDuringPreparation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	activeStore, _ := openConcurrentWorkflowStores(t, cfg)
	activeStore.roleResolver = testsetup.QuestionsEnabled("coder", "reviewer")
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	spareDoneID := workflow.NodeID("node-spare-done-" + string(workflowID))
	spareGroupID := workflow.TransitionGroupID("group-spare-done-" + string(workflowID))
	spareEdgeID := workflow.EdgeID("edge-spare-done-" + string(workflowID))
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
	req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Renamed agent")
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

func TestWorkflowGraphSaveAppliesExpectedRevisionAndRemovalConfirmation(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	unconfirmed := f.request(f.record.Version, false, f.def)
	unconfirmed.Edges = removeWorkflowGraphSaveEdge(unconfirmed.Edges, workflow.EdgeID("edge-done-"+string(f.workflowID)))
	unconfirmed.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(unconfirmed.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(f.workflowID)))
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
	staleResult := f.save(t, stale)
	if !staleResult.Saved || len(staleResult.Blockers) != 0 || staleResult.Version != updatedRecord.Version {
		t.Fatalf("stale no-op save = %+v, want successful no-op without workflow version check", staleResult)
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
	combined.Nodes = renameWorkflowGraphSaveNode(combined.Nodes, workflow.NodeID("node-agent-"+string(f.workflowID)), "Renamed agent")
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
	f := newGraphSaveFixture(t, func(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
		return createScriptStartWorkflow(t, ctx, store, "scripts/old")
	})
	scriptID := workflow.NodeID("node-script-" + string(f.workflowID))
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
			req.Edges[i].Parameters = []workflow.Parameter{{Key: "summary", Description: "Summary for terminal history."}}
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
		SourceNodeID: workflow.NodeID("node-agent-" + string(f.workflowID)),
		TransitionID: "client_generated",
		DisplayName:  "Client Generated",
	})
	req.Edges = append(req.Edges, EdgeRecord{
		ID:                "workflow-edge-00000000-0000-4000-8000-000000000001",
		WorkflowID:        f.workflowID,
		TransitionGroupID: "workflow-transition-group-00000000-0000-4000-8000-000000000001",
		Key:               "client_generated",
		TargetNodeID:      "workflow-node-00000000-0000-4000-8000-000000000001",
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
	agentID := workflow.NodeID("node-agent-" + string(f.workflowID))

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
	blocked.Edges = removeWorkflowGraphSaveEdge(blocked.Edges, workflow.EdgeID("edge-done-"+string(f.workflowID)))
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
	groupID := "group-parallel-" + string(f.workflowID)
	req := f.request(f.record.Version, false, f.def)
	req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: groupID, WorkflowID: f.workflowID, Key: "parallel", DisplayName: "Parallel"})
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-a-"+string(f.workflowID)), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-b-"+string(f.workflowID)), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-join-"+string(f.workflowID)), groupID)

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
	if workflow.NodeGroupID(nodeByID(t, savedDef, workflow.NodeID("node-join-"+string(f.workflowID)))) != groupID {
		t.Fatalf("saved join group id not persisted: %+v", nodeByID(t, savedDef, workflow.NodeID("node-join-"+string(f.workflowID))))
	}

	invalid := f.request(savedRecord.Version, false, savedDef)
	invalid.Nodes = setWorkflowGraphSaveNodeGroup(invalid.Nodes, workflow.NodeID("node-impl-b-"+string(f.workflowID)), "")
	invalidResult := f.save(t, invalid)
	if invalidResult.Saved || workflowGraphSaveBlockerCount(invalidResult.Blockers, "validation_failed") == 0 {
		t.Fatalf("invalid node group graph save = %+v, want validation blocker", invalidResult)
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

func TestPreviewWorkflowGraphSaveDoesNotMutateWithoutBlockers(t *testing.T) {
	f := newGraphSaveFixture(t, createValidWorkflow)
	agentID := workflow.NodeID("node-agent-" + string(f.workflowID))
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
