package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
)

func TestCompleteCurrentNodeRecordsPartialJoinArrival(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)

	arrived, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation a"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode join arrival: %v", err)
	}
	if len(arrived.Mutation.Removed) != 1 || !arrived.Mutation.Removed[0].Equal(f.branchA.Reference) {
		t.Fatalf("join arrival removed = %+v, want impl_a branch", arrived.Mutation.Removed)
	}
	if len(arrived.Mutation.Created) != 0 || len(arrived.AutomaticIntents) != 0 {
		t.Fatalf("partial join arrival = %+v, want no successor current node or intent", arrived)
	}

	currentNodes, err := f.store.ListCurrentNodes(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(f.branchB.Reference) {
		t.Fatalf("current nodes after partial arrival = %+v, want only sibling branch", currentNodes)
	}
	for _, currentNode := range currentNodes {
		if currentNode.Reference.NodeID == workflow.NodeIDOf(f.join) {
			t.Fatalf("current nodes after partial arrival = %+v, must not contain Join", currentNodes)
		}
	}

	fanoutBranches, err := f.store.queries.ListTaskActiveFanoutBranches(f.ctx, string(f.task.ID))
	if err != nil {
		t.Fatalf("ListTaskActiveFanoutBranches: %v", err)
	}
	if len(fanoutBranches) != 2 {
		t.Fatalf("fanout branches after partial arrival = %+v, want two", fanoutBranches)
	}
	for _, branch := range fanoutBranches {
		switch branch.TransitionBranchKey {
		case "split_a":
			if branch.ArrivalState != "arrived" || !branch.ArrivalValuesJson.Valid {
				t.Fatalf("split_a arrival = %+v, want arrived values", branch)
			}
			var values map[string]string
			if err := workflow.UnmarshalString(branch.ArrivalValuesJson.String, &values); err != nil {
				t.Fatalf("decode split_a arrival values: %v", err)
			}
			if len(values) != 1 || values["joined"] != "implementation a" {
				t.Fatalf("split_a arrival values = %+v, want only joined output", values)
			}
		case "split_b":
			if branch.ArrivalState != "pending" || branch.ArrivalValuesJson.Valid {
				t.Fatalf("split_b arrival = %+v, want pending without values", branch)
			}
		default:
			t.Fatalf("unexpected fanout branch = %+v", branch)
		}
	}
}

func TestCompleteCurrentNodeJoinArrivalPreservesInterruptedSibling(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)
	interrupted := interruptCurrentNodeForJoinTest(t, f, f.branchB)

	if _, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation a"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode join arrival: %v", err)
	}

	currentNodes, err := f.store.ListCurrentNodes(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after partial Join: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(interrupted.Reference) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted ||
		currentNodes[0].Scheduling.Interruption == nil ||
		currentNodes[0].Scheduling.Interruption.Reason != interrupted.Scheduling.Interruption.Reason {
		t.Fatalf("current nodes after partial Join = %+v, want preserved interrupted sibling", currentNodes)
	}
}

func TestCompleteCurrentNodeAppliesJoinWhenAllBranchesArrive(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)
	if _, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation a"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first join arrival: %v", err)
	}

	joined, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchB.Reference,
		TransitionID: "join",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode final join arrival: %v", err)
	}
	if len(joined.Mutation.Removed) != 1 || !joined.Mutation.Removed[0].Equal(f.branchB.Reference) {
		t.Fatalf("final join arrival removed = %+v, want impl_b branch", joined.Mutation.Removed)
	}
	if len(joined.Mutation.Created) != 1 ||
		joined.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(f.synth) ||
		joined.Mutation.Created[0].CurrentInputValues["joined"] != "implementation a" ||
		len(joined.AutomaticIntents) != 1 ||
		!joined.AutomaticIntents[0].Equal(joined.Mutation.Created[0].Reference) {
		t.Fatalf("join application = %+v, want ready synth from selected provider", joined)
	}

	currentNodes, err := f.store.ListCurrentNodes(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after join: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(joined.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes after join = %+v, want synth", currentNodes)
	}
	if _, err := f.store.queries.GetTaskActiveFanout(f.ctx, string(f.task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTaskActiveFanout after join = %v, want no active fanout", err)
	}
	branches, err := f.store.queries.ListTaskActiveFanoutBranches(f.ctx, string(f.task.ID))
	if err != nil {
		t.Fatalf("ListTaskActiveFanoutBranches after join: %v", err)
	}
	if len(branches) != 0 {
		t.Fatalf("fanout branches after join = %+v, want none", branches)
	}
}

func TestCompleteCurrentNodeUsesLatestJoinProviderAndOutgoingTransition(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)
	definition, record, err := f.store.GetDefinition(f.ctx, f.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition for latest Join edit: %v", err)
	}
	joinAEdgeID := edgeByKey(t, definition, "join_a").ID
	latestSynthID := workflow.NodeID("node-latest-synth-" + string(f.workflowID))
	latestSynthDoneGroupID := workflow.TransitionGroupID("group-latest-synth-done-" + string(f.workflowID))
	latestSynthDoneEdgeID := workflow.EdgeID("edge-latest-synth-done-" + string(f.workflowID))
	req := workflowGraphSaveRequestFromDefinition(f.workflowID, record.Version, false, definition)
	done := nodeByKind(t, definition, workflow.NodeKindTerminal)
	req.Nodes = removeWorkflowGraphSaveNode(req.Nodes, workflow.NodeIDOf(f.synth))
	req.Nodes = append(req.Nodes, NodeRecord{
		ID:             latestSynthID,
		WorkflowID:     f.workflowID,
		Key:            "latest_synth",
		Kind:           workflow.NodeKindAgent,
		DisplayName:    "Latest Synthesize",
		SubagentRole:   "coder",
		PromptTemplate: "Synthesize {{.Inputs.latest_joined}}.",
		InputFields:    []workflow.InputField{{Name: "latest_joined", Description: "Latest joined value."}},
	})
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-synth-done-"+string(f.workflowID)))
	req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
		ID:           latestSynthDoneGroupID,
		WorkflowID:   f.workflowID,
		SourceNodeID: latestSynthID,
		TransitionID: "done",
		DisplayName:  "Done",
	})
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, workflow.EdgeID("edge-synth-done-"+string(f.workflowID)))
	req.Edges = append(req.Edges, EdgeRecord{
		ID:                latestSynthDoneEdgeID,
		WorkflowID:        f.workflowID,
		TransitionGroupID: latestSynthDoneGroupID,
		Key:               "latest_synth_done",
		TargetNodeID:      workflow.NodeIDOf(done),
		ContextMode:       workflow.ContextModeNewSession,
	})
	workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(f.join)).JoinInputProviders = []workflow.JoinInputProvider{{
		InputName:      "latest_joined",
		ProviderEdgeID: joinAEdgeID,
	}}
	workflowGraphSaveEdgeRecord(t, req.Edges, joinAEdgeID).Parameters = []workflow.Parameter{{
		Key:         "latest_joined",
		Description: "Latest joined value.",
	}}
	joinOutgoing := workflowGraphSaveEdgeRecord(t, req.Edges, workflow.EdgeID("edge-join-synth-"+string(f.workflowID)))
	joinOutgoing.TargetNodeID = latestSynthID
	joinOutgoing.PromptTemplate = "Synthesize {{.Params.latest_joined}}."
	preview, err := f.store.PreviewWorkflowGraphSave(f.ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave latest Join edit: %v", err)
	}
	saved, err := f.store.SaveWorkflowGraph(f.ctx, confirmWorkflowGraphSaveRequest(req, preview.Impact))
	if err != nil {
		t.Fatalf("SaveWorkflowGraph latest Join edit: %v", err)
	}
	if !saved.Saved {
		t.Fatalf("SaveWorkflowGraph latest Join edit = %+v, want saved", saved)
	}

	if _, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"latest_joined": "latest implementation"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first Join arrival after edit: %v", err)
	}
	joined, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchB.Reference,
		TransitionID: "join",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode final Join arrival after edit: %v", err)
	}
	if len(joined.Mutation.Created) != 1 ||
		joined.Mutation.Created[0].Reference.NodeID != latestSynthID ||
		joined.Mutation.Created[0].CurrentInputValues["latest_joined"] != "latest implementation" {
		t.Fatalf("latest Join application = %+v, want latest provider and outgoing target", joined)
	}
}

func TestCompleteCurrentNodeRejectsLatestJoinSameKeyCollisionWithoutStateCorruption(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)
	if _, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation a"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first Join arrival: %v", err)
	}
	definition, _, err := f.store.GetDefinition(f.ctx, f.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before collision: %v", err)
	}
	joinB := edgeByKey(t, definition, "join_b")
	forceWorkflowGraphRowsForSnapshotTest(t, f.ctx, f.store, f.workflowID, nil, nil, []EdgeRecord{{
		ID:                 joinB.ID,
		WorkflowID:         joinB.WorkflowID,
		TransitionGroupID:  joinB.TransitionGroupID,
		Key:                joinB.Key,
		TargetNodeID:       joinB.TargetNodeID,
		ContextMode:        joinB.ContextMode,
		ContextSource:      joinB.ContextSource,
		RequiresApproval:   joinB.RequiresApproval,
		PromptTemplate:     joinB.PromptTemplate,
		Parameters:         []workflow.Parameter{{Key: "joined", Description: "Duplicate joined value."}},
		InputBindings:      joinB.InputBindings,
		OutputRequirements: joinB.OutputRequirements,
	}})

	_, err = f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchB.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation b"},
	})
	var validationErr WorkflowValidationError
	if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeProvisionFieldOverlap) {
		t.Fatalf("final Join arrival error = %T %v, want provision-field collision validation", err, err)
	}

	currentNodes, err := f.store.ListCurrentNodes(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after collision: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(f.branchB.Reference) {
		t.Fatalf("current nodes after collision = %+v, want untouched sibling branch", currentNodes)
	}
	branches, err := f.store.queries.ListTaskActiveFanoutBranches(f.ctx, string(f.task.ID))
	if err != nil {
		t.Fatalf("ListTaskActiveFanoutBranches after collision: %v", err)
	}
	if len(branches) != 2 || branches[0].ArrivalState != "arrived" || branches[1].ArrivalState != "pending" {
		t.Fatalf("fanout branches after collision = %+v, want first arrived and sibling pending", branches)
	}
}

func TestCompleteCurrentNodeRejectsMissingLatestJoinTopologyWithoutStateCorruption(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)
	if _, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation a"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first Join arrival: %v", err)
	}
	definition, _, err := f.store.GetDefinition(f.ctx, f.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before topology edit: %v", err)
	}
	splitA := edgeByKey(t, definition, "split_a")
	forceWorkflowGraphRowsForSnapshotTest(t, f.ctx, f.store, f.workflowID, nil, nil, []EdgeRecord{{
		ID:                 splitA.ID,
		WorkflowID:         splitA.WorkflowID,
		TransitionGroupID:  splitA.TransitionGroupID,
		Key:                "renamed_split_a",
		TargetNodeID:       splitA.TargetNodeID,
		ContextMode:        splitA.ContextMode,
		ContextSource:      splitA.ContextSource,
		RequiresApproval:   splitA.RequiresApproval,
		PromptTemplate:     splitA.PromptTemplate,
		Parameters:         splitA.Parameters,
		InputBindings:      splitA.InputBindings,
		OutputRequirements: splitA.OutputRequirements,
	}})

	_, err = f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchB.Reference,
		TransitionID: "join",
	})
	var validationErr WorkflowValidationError
	if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeInvalidFanoutJoinTopology) {
		t.Fatalf("final Join arrival error = %T %v, want missing topology validation", err, err)
	}
	assertPartialCurrentFanoutJoin(t, f)
}

func TestCompleteCurrentNodeRejectsAmbiguousLatestJoinTopologyWithoutStateCorruption(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)
	if _, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation a"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first Join arrival: %v", err)
	}
	definition, _, err := f.store.GetDefinition(f.ctx, f.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before topology edit: %v", err)
	}
	implementationA := nodeByKey(t, definition, "impl_a")
	implementationB := nodeByKey(t, definition, "impl_b")
	done := nodeByKind(t, definition, workflow.NodeKindTerminal)
	alternateJoinID := workflow.NodeID("node-alternate-join-" + string(f.workflowID))
	alternateAGroupID := workflow.TransitionGroupID("group-alternate-join-a-" + string(f.workflowID))
	alternateBGroupID := workflow.TransitionGroupID("group-alternate-join-b-" + string(f.workflowID))
	alternateDoneGroupID := workflow.TransitionGroupID("group-alternate-join-done-" + string(f.workflowID))
	forceWorkflowGraphRowsForSnapshotTest(t, f.ctx, f.store, f.workflowID,
		[]NodeRecord{{
			ID:          alternateJoinID,
			WorkflowID:  f.workflowID,
			Key:         "alternate_join",
			Kind:        workflow.NodeKindJoin,
			DisplayName: "Alternate Join",
		}},
		[]TransitionGroupRecord{
			{ID: alternateAGroupID, WorkflowID: f.workflowID, SourceNodeID: workflow.NodeIDOf(implementationA), TransitionID: "alternate_join", DisplayName: "Alternate Join"},
			{ID: alternateBGroupID, WorkflowID: f.workflowID, SourceNodeID: workflow.NodeIDOf(implementationB), TransitionID: "alternate_join", DisplayName: "Alternate Join"},
			{ID: alternateDoneGroupID, WorkflowID: f.workflowID, SourceNodeID: alternateJoinID, TransitionID: "done", DisplayName: "Done"},
		},
		[]EdgeRecord{
			{ID: workflow.EdgeID("edge-alternate-join-a-" + string(f.workflowID)), WorkflowID: f.workflowID, TransitionGroupID: alternateAGroupID, Key: "alternate_join_a", TargetNodeID: alternateJoinID, ContextMode: workflow.ContextModeNewSession},
			{ID: workflow.EdgeID("edge-alternate-join-b-" + string(f.workflowID)), WorkflowID: f.workflowID, TransitionGroupID: alternateBGroupID, Key: "alternate_join_b", TargetNodeID: alternateJoinID, ContextMode: workflow.ContextModeNewSession},
			{ID: workflow.EdgeID("edge-alternate-join-done-" + string(f.workflowID)), WorkflowID: f.workflowID, TransitionGroupID: alternateDoneGroupID, Key: "alternate_join_done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		},
	)

	_, err = f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchB.Reference,
		TransitionID: "join",
	})
	var validationErr WorkflowValidationError
	if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeInvalidFanoutJoinTopology) {
		t.Fatalf("final Join arrival error = %T %v, want ambiguous topology validation", err, err)
	}
	assertPartialCurrentFanoutJoin(t, f)
}

func TestCompleteCurrentNodeRejectsLatestUnavailableJoinInputWithoutStateCorruption(t *testing.T) {
	f := startCurrentFanoutJoinTask(t)
	if _, err := f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchA.Reference,
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "implementation a"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first Join arrival: %v", err)
	}
	definition, _, err := f.store.GetDefinition(f.ctx, f.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before input edit: %v", err)
	}
	synth := nodeByKey(t, definition, "synth")
	forceWorkflowGraphRowsForSnapshotTest(t, f.ctx, f.store, f.workflowID, []NodeRecord{{
		ID:             workflow.NodeIDOf(synth),
		WorkflowID:     f.workflowID,
		Key:            workflow.NodeKey(synth),
		Kind:           synth.Kind(),
		DisplayName:    workflow.NodeDisplayName(synth),
		SubagentRole:   workflow.NodeSubagentRole(synth),
		PromptTemplate: workflow.NodePromptTemplate(synth),
		CompletionMode: workflow.NodeCompletionMode(synth),
		InputFields: append(workflow.NodeInputFields(synth), workflow.InputField{
			Name:        "newly_required",
			Description: "Value introduced after this fan-out began.",
		}),
		OutputFields: workflow.NodeOutputFields(synth),
	}}, nil, nil)

	_, err = f.store.CompleteCurrentNode(f.ctx, CurrentNodeCompletionRequest{
		Source:       f.branchB.Reference,
		TransitionID: "join",
	})
	if !completionHasCode(err, CompletionCodeRequiredOutputMissing) {
		t.Fatalf("final Join arrival error = %T %v, want unavailable current Join input validation", err, err)
	}
	assertPartialCurrentFanoutJoin(t, f)
}

type currentFanoutJoinTask struct {
	ctx        context.Context
	store      *Store
	workflowID workflow.WorkflowID
	task       TaskRecord
	branchA    workflow.CurrentNode
	branchB    workflow.CurrentNode
	join       workflow.Node
	synth      workflow.Node
}

func startCurrentFanoutJoinTask(t *testing.T) currentFanoutJoinTask {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationA := nodeByKey(t, definition, "impl_a")
	implementationB := nodeByKey(t, definition, "impl_b")
	join := nodeByKey(t, definition, "join")
	synth := nodeByKey(t, definition, "synth")
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
	branches := map[workflow.NodeID]workflow.CurrentNode{}
	for _, currentNode := range fanout.Mutation.Created {
		branches[currentNode.Reference.NodeID] = currentNode
	}
	branchA, exists := branches[workflow.NodeIDOf(implementationA)]
	if !exists {
		t.Fatalf("fanout current nodes = %+v, missing impl_a", fanout.Mutation.Created)
	}
	branchB, exists := branches[workflow.NodeIDOf(implementationB)]
	if !exists {
		t.Fatalf("fanout current nodes = %+v, missing impl_b", fanout.Mutation.Created)
	}
	return currentFanoutJoinTask{
		ctx:        ctx,
		store:      store,
		workflowID: workflowID,
		task:       task,
		branchA:    branchA,
		branchB:    branchB,
		join:       join,
		synth:      synth,
	}
}

func assertPartialCurrentFanoutJoin(t *testing.T, f currentFanoutJoinTask) {
	t.Helper()
	currentNodes, err := f.store.ListCurrentNodes(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(f.branchB.Reference) {
		t.Fatalf("current nodes = %+v, want untouched sibling branch", currentNodes)
	}
	branches, err := f.store.queries.ListTaskActiveFanoutBranches(f.ctx, string(f.task.ID))
	if err != nil {
		t.Fatalf("ListTaskActiveFanoutBranches: %v", err)
	}
	if len(branches) != 2 || branches[0].ArrivalState != "arrived" || branches[1].ArrivalState != "pending" {
		t.Fatalf("fanout branches = %+v, want first arrived and sibling pending", branches)
	}
}

func interruptCurrentNodeForJoinTest(t *testing.T, f currentFanoutJoinTask, currentNode workflow.CurrentNode) workflow.CurrentNode {
	t.Helper()
	interrupted, err := workflow.NewCurrentNodeWithMaterializedValues(
		currentNode.Reference,
		currentNode.CurrentInputValues,
		currentNode.PriorNodeValues,
		currentNode.SessionID,
		&workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
			Interruption: &workflow.CurrentNodeInterruption{
				Reason: "manual",
				Detail: workflow.CurrentNodeInterruptionDetail{
					Code:   "manual",
					Fields: map[string]string{"scope": "sibling"},
				},
				OccurredAt: time.UnixMilli(1).UTC(),
			},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeWithMaterializedValues interrupted sibling: %v", err)
	}
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx interrupted sibling: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := f.store.queries.WithTx(tx)
	removed, err := deleteTaskCurrentNode(f.ctx, q, currentNode.Reference)
	if err != nil {
		t.Fatalf("delete current sibling: %v", err)
	}
	if removed != 1 {
		t.Fatalf("delete current sibling rows = %d, want one", removed)
	}
	if err := insertTaskCurrentNode(f.ctx, q, interrupted); err != nil {
		t.Fatalf("insert interrupted sibling: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit interrupted sibling: %v", err)
	}
	return interrupted
}
