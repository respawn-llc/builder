package workflowstore

import (
	"context"
	"core/internal/testharness/testsetup"
	"reflect"
	"testing"

	"core/server/workflow"
)

func TestWorkflowGraphSaveRequestFromDefinitionPreservesProductGraph(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow-rich")
	groupID := "group-parallel"
	groupIDPointer := &groupID
	agentID := testNodeID("node-agent")
	scriptID := testNodeID("node-script")
	joinID := testNodeID("node-join")
	providerEdgeID := testEdgeID("edge-provider")
	def := workflow.Definition{
		ID: workflowID,
		NodeGroups: []workflow.NodeGroup{
			{
				WorkflowID:  workflowID,
				ID:          groupID,
				Key:         "parallel",
				DisplayName: "Parallel",
				MemberNodeIDs: []workflow.NodeID{
					agentID,
					scriptID,
					joinID,
				},
			},
			{WorkflowID: workflowID, ID: "group-followup", Key: "followup", DisplayName: "Follow-up"},
		},
		Nodes: []workflow.Node{
			workflow.StartNode{NodeIdentity: workflow.NodeIdentity{WorkflowID: workflowID, ID: "node-start", Key: "start", DisplayName: "Start"}},
			workflow.AgentNode{
				NodeIdentity:   workflow.NodeIdentity{WorkflowID: workflowID, ID: agentID, Key: "agent", DisplayName: "Agent", GroupID: groupIDPointer},
				SubagentRole:   "coder",
				CompletionMode: "tool",
			},
			workflow.ScriptNode{
				NodeIdentity: workflow.NodeIdentity{WorkflowID: workflowID, ID: scriptID, Key: "script", DisplayName: "Script", GroupID: groupIDPointer},
				ScriptPath:   workflow.MustPresentScriptPath("scripts/run"),
			},
			workflow.JoinNode{
				NodeIdentity:       workflow.NodeIdentity{WorkflowID: workflowID, ID: joinID, Key: "join", DisplayName: "Join", GroupID: groupIDPointer},
				JoinInputProviders: []workflow.JoinInputProvider{{InputName: "summary", ProviderEdgeID: providerEdgeID}},
			},
			workflow.ScriptNode{NodeIdentity: workflow.NodeIdentity{WorkflowID: workflowID, ID: "node-script-absent", Key: "script_absent", DisplayName: "Script absent"}},
			workflow.TerminalNode{NodeIdentity: workflow.NodeIdentity{WorkflowID: workflowID, ID: "node-done", Key: "done", DisplayName: "Done"}},
		},
		TransitionGroups: []workflow.TransitionGroup{{
			WorkflowID:   workflowID,
			ID:           "transition-review",
			SourceNodeID: agentID,
			TransitionID: "review",
			DisplayName:  "Review",
			Description:  "Select after implementation.",
		}},
		Edges: []workflow.Edge{{
			WorkflowID:        workflowID,
			ID:                providerEdgeID,
			Key:               "review",
			TransitionGroupID: "transition-review",
			TargetNodeID:      scriptID,
			ContextMode:       workflow.ContextModeContinueSession,
			ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "agent"},
			RequiresApproval:  true,
			PromptTemplate:    "Review {{.Params.summary}}.",
			Parameters:        []workflow.Parameter{{Key: "summary", Description: "Summary."}},
			InputBindings:     []workflow.InputBinding{{Name: "summary", Source: workflow.BindingSourceTransitionOutput, Field: "summary"}},
			OutputRequirements: []workflow.OutputRequirement{{
				FieldName: "summary",
			}},
		}},
	}

	req := workflowGraphSaveRequestFromDefinition(workflowID, 7, false, def)
	if req.ExpectedVersion != 7 || req.Confirmed {
		t.Fatalf("request metadata = %+v, want version 7 unconfirmed", req)
	}
	if len(req.NodeGroups) != 2 ||
		req.NodeGroups[0].ID != groupID || req.NodeGroups[0].Key != "parallel" || req.NodeGroups[0].DisplayName != "Parallel" || req.NodeGroups[0].SortOrder != 0 ||
		req.NodeGroups[1].ID != "group-followup" || req.NodeGroups[1].Key != "followup" || req.NodeGroups[1].DisplayName != "Follow-up" || req.NodeGroups[1].SortOrder != 100 {
		t.Fatalf("node groups = %+v, want canonical order with normalized sort values", req.NodeGroups)
	}
	agent := workflowGraphSaveNodeRecord(t, req.Nodes, agentID)
	if agent.GroupID == nil || *agent.GroupID != groupID || agent.GroupKey != "parallel" || agent.SubagentRole != "coder" || agent.CompletionMode != "tool" {
		t.Fatalf("agent record = %+v, want identity and execution fields preserved", agent)
	}
	script := workflowGraphSaveNodeRecord(t, req.Nodes, scriptID)
	if script.ScriptPath != "scripts/run" || script.GroupKey != "parallel" {
		t.Fatalf("script record = %+v, want path and group key preserved", script)
	}
	absentScript := workflowGraphSaveNodeRecord(t, req.Nodes, "node-script-absent")
	if absentScript.ScriptPath != "" {
		t.Fatalf("absent script path = %q, want absent storage value", absentScript.ScriptPath)
	}
	join := workflowGraphSaveNodeRecord(t, req.Nodes, joinID)
	if !reflect.DeepEqual(join.JoinInputProviders, []workflow.JoinInputProvider{{InputName: "summary", ProviderEdgeID: providerEdgeID}}) {
		t.Fatalf("join providers = %+v, want provider preserved", join.JoinInputProviders)
	}
	if len(req.TransitionGroups) != 1 || req.TransitionGroups[0].Description != "Select after implementation." {
		t.Fatalf("transition groups = %+v, want description preserved", req.TransitionGroups)
	}
	if len(req.Edges) != 1 || !reflect.DeepEqual(req.Edges[0], EdgeRecord{
		ID:                 providerEdgeID,
		WorkflowID:         workflowID,
		TransitionGroupID:  "transition-review",
		Key:                "review",
		TargetNodeID:       scriptID,
		AssigneeSelection:  workflow.AssigneeSelectionConfigured,
		ThinkingSelection:  workflow.ThinkingSelectionConfigured,
		RequiresApproval:   true,
		ContextMode:        workflow.ContextModeContinueSession,
		ContextSource:      workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "agent"},
		InputBindings:      []workflow.InputBinding{{Name: "summary", Source: workflow.BindingSourceTransitionOutput, Field: "summary"}},
		PromptTemplate:     "Review {{.Params.summary}}.",
		Parameters:         []workflow.Parameter{{Key: "summary", Description: "Summary.", Purpose: workflow.ParameterPurposeOrdinary}},
		OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}},
	}) {
		t.Fatalf("edges = %+v, want invocation contract preserved", req.Edges)
	}
}

func TestWorkflowGraphSaveFixtureRequestReportsTypedRejectionDetails(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Rejected Fixture Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, record, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(created.ID, record.Version, false, def)
	req.Nodes = append(req.Nodes, req.Nodes[0])

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph rejected fixture: %v", err)
	}
	if result.Saved ||
		workflowGraphSaveBlockerCount(result.Blockers, "validation_failed") == 0 ||
		!hasWorkflowValidationCode(result.ValidationErrors, workflow.CodeDuplicateNodeID) {
		t.Fatalf("rejected fixture = %+v, want validation blocker and duplicate-node detail", result)
	}
}

func TestWorkflowGraphSaveFixtureUsesOneAtomicSaveAndConverterNoop(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Fixture Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	before := created.Version
	agentID := testNodeID("node-agent")
	startGroup := testTransitionGroupID("group-start")
	doneGroup := testTransitionGroupID("group-done")

	result := saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes, NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder"})
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start", Description: "Begin work."},
			TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: testEdgeID("edge-start"), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."},
			EdgeRecord{ID: testEdgeID("edge-done"), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	if !result.Changed || result.Version != before+1 {
		t.Fatalf("fixture save = %+v, want one graph version increment", result)
	}

	def, record, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after fixture save: %v", err)
	}
	for _, edge := range def.Edges {
		if edge.AssigneeSelection != workflow.AssigneeSelectionConfigured ||
			edge.ThinkingSelection != workflow.ThinkingSelectionConfigured {
			t.Fatalf("reloaded edge selectors = %+v, want configured defaults", edge)
		}
	}
	noop := workflowGraphSaveRequestFromDefinition(created.ID, record.Version, false, def)
	preview, err := store.PreviewWorkflowGraphSave(context.Background(), noop)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave converter noop: %v", err)
	}
	if preview.Changed || !preview.CanSave || len(preview.Blockers) != 0 || len(preview.ValidationErrors) != 0 {
		t.Fatalf("converter preview = %+v, want equivalent no-op graph", preview)
	}
	saved, err := store.SaveWorkflowGraph(ctx, noop)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph converter noop: %v", err)
	}
	if !saved.Saved || saved.Changed || saved.Version != record.Version {
		t.Fatalf("converter no-op save = %+v, want stable version", saved)
	}
}
