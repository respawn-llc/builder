package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
)

func TestCompleteCurrentNodeFanoutCreatesBranchScopedCurrentNodesWithDuplicateTargets(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createDuplicateTargetFanoutWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationA := nodeByKey(t, definition, "impl_a")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "branch plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fanout: %v", err)
	}
	if len(completed.Mutation.Removed) != 1 || !completed.Mutation.Removed[0].Equal(source.Reference) {
		t.Fatalf("fanout removed = %+v, want source current node", completed.Mutation.Removed)
	}
	if len(completed.Mutation.Created) != 2 || len(completed.AutomaticIntents) != 2 {
		t.Fatalf("fanout result = %+v, want two branch current nodes and intents", completed)
	}
	createdByBranch := map[workflow.TransitionBranchKey]workflow.CurrentNode{}
	for _, currentNode := range completed.Mutation.Created {
		branchKey, branchScoped := currentNode.Reference.TransitionBranchKey()
		if !branchScoped {
			t.Fatalf("fanout current node = %+v, want branch scope", currentNode)
		}
		if currentNode.Reference.NodeID != workflow.NodeIDOf(implementationA) ||
			currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingReady {
			t.Fatalf("fanout current node = %+v, want ready duplicate impl_a target", currentNode)
		}
		createdByBranch[branchKey] = currentNode
	}
	for _, branchKey := range []workflow.TransitionBranchKey{"split_a", "split_b"} {
		if _, found := createdByBranch[branchKey]; !found {
			t.Fatalf("fanout current nodes = %+v, missing branch %q", completed.Mutation.Created, branchKey)
		}
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after fanout: %v", err)
	}
	if len(currentNodes) != 2 {
		t.Fatalf("current nodes after fanout = %+v, want two branch nodes", currentNodes)
	}
	fanoutTaskID, err := store.queries.GetTaskActiveFanout(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTaskActiveFanout: %v", err)
	}
	if fanoutTaskID != string(task.ID) {
		t.Fatalf("active fanout task = %q, want %q", fanoutTaskID, task.ID)
	}
	branches, err := store.queries.ListTaskActiveFanoutBranches(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("ListTaskActiveFanoutBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("active fanout branches = %+v, want exactly two expected branches", branches)
	}
	for index, branch := range branches {
		want := []string{"split_a", "split_b"}[index]
		if branch.TransitionBranchKey != want || branch.ArrivalState != "pending" || branch.ArrivalValuesJson.Valid {
			t.Fatalf("active fanout branch = %+v, want pending %q without arrival values", branch, want)
		}
	}
}

func TestCompleteCurrentNodeCarriesFanoutBranchAcrossPreJoinNode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationB := nodeByKey(t, definition, "impl_b")
	join := nodeByKey(t, definition, "join")
	joinAEdgeID := edgeByKey(t, definition, "join_a").ID
	verifyID := workflow.NodeID("node-verify-" + string(workflowID))
	verifyGroupID := workflow.TransitionGroupID("group-verify-" + string(workflowID))
	verifyJoinEdgeID := workflow.EdgeID("edge-verify-join-" + string(workflowID))
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:             verifyID,
			WorkflowID:     workflowID,
			Key:            "verify",
			Kind:           workflow.NodeKindAgent,
			DisplayName:    "Verify",
			SubagentRole:   "coder",
			PromptTemplate: "Verify {{.Inputs.summary}}.",
			InputFields:    []workflow.InputField{{Name: "summary", Description: "Implementation summary."}},
			OutputFields:   []workflow.OutputField{{Name: "joined", Description: "Join value."}},
		})
		workflowGraphSaveEdgeRecord(t, req.Edges, joinAEdgeID).TargetNodeID = verifyID
		workflowGraphSaveEdgeRecord(t, req.Edges, joinAEdgeID).Parameters = []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}
		workflowGraphSaveEdgeRecord(t, req.Edges, joinAEdgeID).PromptTemplate = "Verify {{.Params.summary}}."
		workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(join)).JoinInputProviders = []workflow.JoinInputProvider{{
			InputName:      "joined",
			ProviderEdgeID: verifyJoinEdgeID,
		}}
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           verifyGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: verifyID,
			TransitionID: "join",
			DisplayName:  "Join",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                verifyJoinEdgeID,
			WorkflowID:        workflowID,
			TransitionGroupID: verifyGroupID,
			Key:               "verify_join",
			TargetNodeID:      workflow.NodeIDOf(join),
			ContextMode:       workflow.ContextModeNewSession,
			Parameters:        []workflow.Parameter{{Key: "joined", Description: "Join value."}},
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	fanout, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fanout: %v", err)
	}
	var branchA workflow.CurrentNode
	for _, currentNode := range fanout.Mutation.Created {
		branchKey, branchScoped := currentNode.Reference.TransitionBranchKey()
		if branchScoped && branchKey == "split_a" {
			branchA = currentNode
			break
		}
	}
	if err := branchA.Reference.Validate(); err != nil {
		t.Fatalf("fanout did not create split_a current node: %v", err)
	}

	progressed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"summary": "implementation A"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode branch progression: %v", err)
	}
	if len(progressed.Mutation.Removed) != 1 ||
		!progressed.Mutation.Removed[0].Equal(branchA.Reference) ||
		len(progressed.Mutation.Created) != 1 {
		t.Fatalf("branch progression = %+v, want atomic branch replacement", progressed)
	}
	verify := progressed.Mutation.Created[0]
	verifyBranchKey, verifyBranchScoped := verify.Reference.TransitionBranchKey()
	if !verifyBranchScoped ||
		verifyBranchKey != "split_a" ||
		verify.Reference.NodeID != verifyID ||
		verify.CurrentInputValues["summary"] != "implementation A" {
		t.Fatalf("progressed branch current node = %+v, want split_a verify with materialized value", verify)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after branch progression: %v", err)
	}
	if len(currentNodes) != 2 {
		t.Fatalf("current nodes after branch progression = %+v, want two active branches", currentNodes)
	}
	refs := map[workflow.NodeID]map[workflow.TransitionBranchKey]bool{}
	for _, currentNode := range currentNodes {
		branchKey, branchScoped := currentNode.Reference.TransitionBranchKey()
		if !branchScoped {
			t.Fatalf("current node after branch progression = %+v, want branch scope", currentNode)
		}
		if refs[currentNode.Reference.NodeID] == nil {
			refs[currentNode.Reference.NodeID] = map[workflow.TransitionBranchKey]bool{}
		}
		refs[currentNode.Reference.NodeID][branchKey] = true
	}
	if !refs[verifyID]["split_a"] || !refs[workflow.NodeIDOf(implementationB)]["split_b"] {
		t.Fatalf("current branch refs = %+v, want verify/split_a and impl_b/split_b", refs)
	}
}

func TestCompleteCurrentNodeSelectsRetainedSessionWithinFanoutBranch(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createDuplicateTargetFanoutWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	join := nodeByKey(t, definition, "join")
	joinAEdgeID := edgeByKey(t, definition, "join_a").ID
	verifyID := workflow.NodeID("node-context-verify-" + string(workflowID))
	verifyGroupID := workflow.TransitionGroupID("group-context-verify-" + string(workflowID))
	verifyJoinEdgeID := workflow.EdgeID("edge-context-verify-join-" + string(workflowID))
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:             verifyID,
			WorkflowID:     workflowID,
			Key:            "verify",
			Kind:           workflow.NodeKindAgent,
			DisplayName:    "Verify",
			SubagentRole:   "coder",
			PromptTemplate: "Verify {{.Inputs.summary}}.",
			InputFields:    []workflow.InputField{{Name: "summary", Description: "Implementation summary."}},
			OutputFields:   []workflow.OutputField{{Name: "joined", Description: "Join value."}},
		})
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, joinAEdgeID)
		edge.TargetNodeID = verifyID
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "impl_a"}
		edge.Parameters = []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}
		edge.PromptTemplate = "Verify {{.Params.summary}}."
		workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(join)).JoinInputProviders = []workflow.JoinInputProvider{{
			InputName:      "joined",
			ProviderEdgeID: verifyJoinEdgeID,
		}}
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           verifyGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: verifyID,
			TransitionID: "join",
			DisplayName:  "Join",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                verifyJoinEdgeID,
			WorkflowID:        workflowID,
			TransitionGroupID: verifyGroupID,
			Key:               "verify_join",
			TargetNodeID:      workflow.NodeIDOf(join),
			ContextMode:       workflow.ContextModeNewSession,
			Parameters:        []workflow.Parameter{{Key: "joined", Description: "Join value."}},
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	fanout, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fanout: %v", err)
	}
	var branchA, branchB workflow.CurrentNode
	for _, currentNode := range fanout.Mutation.Created {
		branchKey, branchScoped := currentNode.Reference.TransitionBranchKey()
		if !branchScoped {
			t.Fatalf("fanout current node = %+v, want branch scope", currentNode)
		}
		switch branchKey {
		case "split_a":
			branchA = currentNode
		case "split_b":
			branchB = currentNode
		}
	}
	if err := branchA.Reference.Validate(); err != nil {
		t.Fatalf("fanout omitted split_a branch: %v", err)
	}
	if err := branchB.Reference.Validate(); err != nil {
		t.Fatalf("fanout omitted split_b branch: %v", err)
	}
	sessionA := associateTaskSessionForTest(t, ctx, store, binding, cfg, branchA.Reference, time.UnixMilli(1_700_000_000_001).UTC())
	associateTaskSessionForTest(t, ctx, store, binding, cfg, branchB.Reference, time.UnixMilli(1_700_000_000_002).UTC())

	progressed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branchA.Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"summary": "implementation A"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode branch context: %v", err)
	}
	if len(progressed.Mutation.Created) != 1 ||
		progressed.Mutation.Created[0].Reference.NodeID != verifyID ||
		progressed.Mutation.Created[0].SessionID == nil ||
		*progressed.Mutation.Created[0].SessionID != sessionA {
		t.Fatalf("branch context target = %+v, want split_a retained session %q", progressed.Mutation.Created, sessionA)
	}
}

func TestApplyPendingApprovalReplacesOnlyItsFanoutBranch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createDuplicateTargetFanoutWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before branch approval edit: %v", err)
	}
	join := nodeByKey(t, definition, "join")
	joinAEdgeID := edgeByKey(t, definition, "join_a").ID
	verifyID := workflow.NodeID("node-approval-verify-" + string(workflowID))
	verifyGroupID := workflow.TransitionGroupID("group-approval-verify-" + string(workflowID))
	verifyJoinEdgeID := workflow.EdgeID("edge-approval-verify-join-" + string(workflowID))
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:             verifyID,
			WorkflowID:     workflowID,
			Key:            "verify",
			Kind:           workflow.NodeKindAgent,
			DisplayName:    "Verify",
			SubagentRole:   "coder",
			PromptTemplate: "Verify.",
		})
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, joinAEdgeID)
		edge.TargetNodeID = verifyID
		edge.RequiresApproval = true
		edge.PromptTemplate = "Verify."
		workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(join)).JoinInputProviders = nil
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           verifyGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: verifyID,
			TransitionID: "join",
			DisplayName:  "Join",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                verifyJoinEdgeID,
			WorkflowID:        workflowID,
			TransitionGroupID: verifyGroupID,
			Key:               "verify_join",
			TargetNodeID:      workflow.NodeIDOf(join),
			ContextMode:       workflow.ContextModeNewSession,
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	fanout, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fanout: %v", err)
	}
	var branchA, branchB workflow.CurrentNode
	for _, currentNode := range fanout.Mutation.Created {
		branchKey, branchScoped := currentNode.Reference.TransitionBranchKey()
		if !branchScoped {
			t.Fatalf("fanout current node = %+v, want branch scope", currentNode)
		}
		switch branchKey {
		case "split_a":
			branchA = currentNode
		case "split_b":
			branchB = currentNode
		}
	}
	if err := branchA.Reference.Validate(); err != nil {
		t.Fatalf("fanout omitted split_a branch: %v", err)
	}
	if err := branchB.Reference.Validate(); err != nil {
		t.Fatalf("fanout omitted split_b branch: %v", err)
	}

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branchA.Reference,
		TransitionID: "join_a",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode branch approval: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("branch completion did not create a pending approval")
	}
	if !completed.PendingApproval.Source.Equal(branchA.Reference) ||
		len(completed.PendingApproval.Branches) != 1 ||
		!completed.PendingApproval.Branches[0].Target.CurrentNode.Reference.IsBranchScoped() {
		t.Fatalf("branch pending approval = %+v, want frozen branch-scoped target", completed.PendingApproval)
	}

	applied, err := store.ApplyPendingApproval(ctx, completed.PendingApproval.ID)
	if err != nil {
		t.Fatalf("ApplyPendingApproval: %v", err)
	}
	if len(applied.Mutation.Removed) != 1 ||
		!applied.Mutation.Removed[0].Equal(branchA.Reference) ||
		len(applied.Mutation.Created) != 1 {
		t.Fatalf("branch approval application = %+v, want one branch replacement", applied)
	}
	target := applied.Mutation.Created[0]
	targetBranch, branchScoped := target.Reference.TransitionBranchKey()
	if !branchScoped || targetBranch != "split_a" || target.Reference.NodeID != verifyID {
		t.Fatalf("branch approval target = %+v, want verify/split_a", target)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after branch approval: %v", err)
	}
	if len(currentNodes) != 2 {
		t.Fatalf("current nodes after branch approval = %+v, want join/split_a and sibling", currentNodes)
	}
	foundTarget := false
	foundSibling := false
	for _, currentNode := range currentNodes {
		if currentNode.Reference.Equal(target.Reference) {
			foundTarget = true
		}
		if currentNode.Reference.Equal(branchB.Reference) {
			foundSibling = true
		}
	}
	if !foundTarget || !foundSibling {
		t.Fatalf("current nodes after branch approval = %+v, want replacement and sibling", currentNodes)
	}
	branches, err := store.queries.ListTaskActiveFanoutBranches(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("ListTaskActiveFanoutBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("active fanout branches after approval = %+v, want unchanged expected branches", branches)
	}
}

func TestApplyPendingApprovalCreatesFrozenFanoutBranches(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createDuplicateTargetFanoutWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		for _, edgeKey := range []string{"split_a", "split_b"} {
			edge := workflowGraphSaveEdgeRecord(t, req.Edges, edgeByKey(t, def, edgeKey).ID)
			edge.RequiresApproval = true
		}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fanout approval: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("fanout completion did not create a pending approval")
	}
	approval := *completed.PendingApproval
	if len(completed.Mutation.Removed) != 0 || len(completed.Mutation.Created) != 0 || len(completed.AutomaticIntents) != 0 {
		t.Fatalf("fanout approval completion = %+v, want retained source without successors", completed)
	}
	if len(approval.Branches) != 2 {
		t.Fatalf("fanout approval branches = %+v, want two frozen branches", approval.Branches)
	}
	for _, branch := range approval.Branches {
		targetBranchKey, branchScoped := branch.Target.CurrentNode.Reference.TransitionBranchKey()
		if !branchScoped ||
			targetBranchKey != branch.TransitionBranchKey ||
			branch.Target.CurrentNode.Scheduling == nil ||
			branch.Target.CurrentNode.Scheduling.State != workflow.CurrentNodeSchedulingReady {
			t.Fatalf("fanout approval branch = %+v, want ready branch-scoped target", branch)
		}
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes while fanout approval is pending: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) {
		t.Fatalf("current nodes while fanout approval is pending = %+v, want serial source", currentNodes)
	}
	if _, err := store.queries.GetTaskActiveFanout(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTaskActiveFanout while approval pending = %v, want no active fanout", err)
	}

	applied, err := store.ApplyPendingApproval(ctx, approval.ID)
	if err != nil {
		t.Fatalf("ApplyPendingApproval fanout: %v", err)
	}
	if len(applied.Mutation.Removed) != 1 ||
		!applied.Mutation.Removed[0].Equal(source.Reference) ||
		len(applied.Mutation.Created) != 2 ||
		len(applied.AutomaticIntents) != 2 {
		t.Fatalf("fanout approval application = %+v, want source replaced by two branches", applied)
	}
	currentNodes, err = store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after fanout approval: %v", err)
	}
	if len(currentNodes) != 2 {
		t.Fatalf("current nodes after fanout approval = %+v, want two branch targets", currentNodes)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after fanout approval: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending approvals after fanout approval = %+v, want none", approvals)
	}
	branches, err := store.queries.ListTaskActiveFanoutBranches(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("ListTaskActiveFanoutBranches after approval: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("active fanout branches after approval = %+v, want two frozen expected branches", branches)
	}
	for index, branch := range branches {
		want := []string{"split_a", "split_b"}[index]
		if branch.TransitionBranchKey != want || branch.ArrivalState != "pending" || branch.ArrivalValuesJson.Valid {
			t.Fatalf("active fanout branch = %+v, want pending %q without arrival values", branch, want)
		}
	}
}

func createDuplicateTargetFanoutWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Duplicate target fan-out"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.ID
	planID := workflow.NodeID("node-plan-" + string(workflowID))
	implementationID := workflow.NodeID("node-impl-" + string(workflowID))
	joinID := workflow.NodeID("node-join-" + string(workflowID))
	synthID := workflow.NodeID("node-synth-" + string(workflowID))
	startGroupID := workflow.TransitionGroupID("group-start-" + string(workflowID))
	splitGroupID := workflow.TransitionGroupID("group-split-" + string(workflowID))
	joinAGroupID := workflow.TransitionGroupID("group-join-a-" + string(workflowID))
	joinBGroupID := workflow.TransitionGroupID("group-join-b-" + string(workflowID))
	synthGroupID := workflow.TransitionGroupID("group-synth-" + string(workflowID))
	doneGroupID := workflow.TransitionGroupID("group-done-" + string(workflowID))
	joinAEdgeID := workflow.EdgeID("edge-join-a-" + string(workflowID))
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: planID, WorkflowID: workflowID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
			NodeRecord{ID: implementationID, WorkflowID: workflowID, Key: "impl_a", Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: "coder", PromptTemplate: "Implement.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}, {Name: "joined", Description: "Join value."}}},
			NodeRecord{ID: joinID, WorkflowID: workflowID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join", JoinInputProviders: []workflow.JoinInputProvider{{InputName: "joined", ProviderEdgeID: joinAEdgeID}}},
			NodeRecord{ID: synthID, WorkflowID: workflowID, Key: "synth", Kind: workflow.NodeKindAgent, DisplayName: "Synthesize", SubagentRole: "coder", PromptTemplate: "Synthesize.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroupID, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: splitGroupID, WorkflowID: workflowID, SourceNodeID: planID, TransitionID: "split", DisplayName: "Split"},
			TransitionGroupRecord{ID: joinAGroupID, WorkflowID: workflowID, SourceNodeID: implementationID, TransitionID: "join_a", DisplayName: "Join A"},
			TransitionGroupRecord{ID: joinBGroupID, WorkflowID: workflowID, SourceNodeID: implementationID, TransitionID: "join_b", DisplayName: "Join B"},
			TransitionGroupRecord{ID: synthGroupID, WorkflowID: workflowID, SourceNodeID: joinID, TransitionID: "synth", DisplayName: "Synthesize"},
			TransitionGroupRecord{ID: doneGroupID, WorkflowID: workflowID, SourceNodeID: synthID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
			EdgeRecord{ID: workflow.EdgeID("edge-split-a-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: splitGroupID, Key: "split_a", TargetNodeID: implementationID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement A.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-split-b-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: splitGroupID, Key: "split_b", TargetNodeID: implementationID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement B.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
			EdgeRecord{ID: joinAEdgeID, WorkflowID: workflowID, TransitionGroupID: joinAGroupID, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "joined", Description: "Join value."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-join-b-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: joinBGroupID, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: workflow.EdgeID("edge-join-synth-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: synthGroupID, Key: "synth", TargetNodeID: synthID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Synthesize."},
			EdgeRecord{ID: workflow.EdgeID("edge-synth-done-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return workflowID
}
