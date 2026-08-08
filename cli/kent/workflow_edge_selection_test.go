package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowEdgeMutationRemote struct {
	workflowSelectorInventoryRemote
	definitionValue serverapi.WorkflowDefinition
	addRequest      *serverapi.WorkflowEdgeAddRequest
	updateRequest   *serverapi.WorkflowEdgeUpdateRequest
	addError        error
	updateError     error
}

func (r *workflowEdgeMutationRemote) GetWorkflow(context.Context, serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	r.record(r.expected)
	return serverapi.WorkflowGetResponse{Definition: r.definitionValue}, nil
}

func (r *workflowEdgeMutationRemote) AddWorkflowEdge(_ context.Context, req serverapi.WorkflowEdgeAddRequest) (serverapi.WorkflowEdgeAddResponse, error) {
	r.record(req.WorkflowID)
	r.addRequest = &req
	return serverapi.WorkflowEdgeAddResponse{Version: 2}, r.addError
}

func (r *workflowEdgeMutationRemote) UpdateWorkflowEdge(_ context.Context, req serverapi.WorkflowEdgeUpdateRequest) (serverapi.WorkflowEdgeUpdateResponse, error) {
	r.record(req.WorkflowID)
	r.updateRequest = &req
	return serverapi.WorkflowEdgeUpdateResponse{Version: 2}, r.updateError
}

func workflowEdgeMutationDefinition(t *testing.T, edge serverapi.WorkflowEdge) workflowEdgeMutationRemote {
	t.Helper()
	workflowID := testWorkflowID(t, "cli-edge-selection")
	edge.WorkflowID = workflowID
	return workflowEdgeMutationRemote{
		workflowSelectorInventoryRemote: workflowSelectorInventoryRemote{expected: workflowID},
		definitionValue: serverapi.WorkflowDefinition{
			Workflow: workflowRecordForTest(workflowID),
			Nodes: []serverapi.WorkflowNode{
				{ID: "source", WorkflowID: workflowID, Key: "source", Kind: "agent", DisplayName: "Source"},
				{ID: "target", WorkflowID: workflowID, Key: "target", Kind: "agent", DisplayName: "Target", SubagentRole: "default"},
			},
			TransitionGroups: []serverapi.WorkflowTransitionGroup{{
				ID: "group", WorkflowID: workflowID, SourceNodeID: "source", TransitionID: "next", DisplayName: "Next",
			}},
			Edges: []serverapi.WorkflowEdge{edge},
		},
	}
}

func testWorkflowID(t *testing.T, seed string) runtimeids.WorkflowID {
	t.Helper()
	return testsetup.WorkflowID(t, seed)
}

func workflowRecordForTest(id runtimeids.WorkflowID) serverapi.WorkflowRecord {
	return serverapi.WorkflowRecord{ID: id, Name: "Workflow", Version: 1}
}

func runWorkflowEdgeCommand(t *testing.T, remote workflowCommandRemote, args ...string) (int, string, string) {
	t.Helper()
	installWorkflowCommandRemote(t, remote)
	var stdout, stderr bytes.Buffer
	exitCode := workflowSubcommand(args, &stdout, &stderr)
	return exitCode, stdout.String(), stderr.String()
}

func TestWorkflowEdgeAddSelectionDefaultsToConfigured(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{})
	exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
		"edge", "add", remote.expected.String(),
		"--from", "source", "--transition", "next", "--edge-key", "new", "--to", "target", "--context", "new_session",
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	if remote.addRequest == nil {
		t.Fatal("AddWorkflowEdge was not called")
	}
	if remote.addRequest.AssigneeSelection != "configured" || remote.addRequest.ThinkingSelection != "configured" {
		t.Fatalf("selection defaults = %q/%q, want configured/configured", remote.addRequest.AssigneeSelection, remote.addRequest.ThinkingSelection)
	}
}

func TestWorkflowGraphDraftFromDefinitionPreservesEdgeSelectionAndParameterPurpose(t *testing.T) {
	workflowID := testWorkflowID(t, "cli-graph-draft-selection")
	def := serverapi.WorkflowDefinition{
		Workflow: workflowRecordForTest(workflowID),
		Edges: []serverapi.WorkflowEdge{{
			ID: "edge", TransitionGroupID: "group", Key: "edge", TargetNodeID: "target",
			AssigneeSelection: "previous_node", ThinkingSelection: "configured",
			Parameters: []serverapi.WorkflowParameter{{Key: "role", Purpose: "target_assignee"}},
		}},
	}
	graph := workflowGraphDraftFromDefinition(def)
	if len(graph.Edges) != 1 {
		t.Fatalf("draft edges = %+v", graph.Edges)
	}
	edge := graph.Edges[0]
	if edge.AssigneeSelection != "previous_node" || edge.ThinkingSelection != "configured" {
		t.Fatalf("draft selectors = %q/%q", edge.AssigneeSelection, edge.ThinkingSelection)
	}
	if len(edge.Parameters) != 1 || edge.Parameters[0].Purpose != "target_assignee" {
		t.Fatalf("draft parameters = %+v", edge.Parameters)
	}
}

func TestWorkflowEdgeAddMapsEnabledSelectorsAndBlankProtectedDescriptions(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{})
	exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
		"edge", "add", remote.expected.String(),
		"--from", "source", "--transition", "next", "--edge-key", "new", "--to", "target", "--context", "new_session",
		"--assignee-selection", "previous_node",
		"--thinking-selection", "previous_node",
		"--target-assignee-param", "chosen_role=",
		"--target-thinking-param", "chosen_thinking=pick a level",
		"--param", "ordinary=ordinary value",
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	req := remote.addRequest
	if req == nil {
		t.Fatal("AddWorkflowEdge was not called")
	}
	if req.AssigneeSelection != "previous_node" || req.ThinkingSelection != "previous_node" {
		t.Fatalf("selection = %q/%q", req.AssigneeSelection, req.ThinkingSelection)
	}
	if len(req.Parameters) != 3 {
		t.Fatalf("parameters = %+v, want three rows", req.Parameters)
	}
	if req.Parameters[0] != (serverapi.WorkflowParameter{Key: "ordinary", Description: "ordinary value", Purpose: "ordinary"}) {
		t.Fatalf("ordinary parameter = %+v", req.Parameters[0])
	}
	if req.Parameters[1] != (serverapi.WorkflowParameter{Key: "chosen_role", Purpose: "target_assignee"}) {
		t.Fatalf("assignee parameter = %+v", req.Parameters[1])
	}
	if req.Parameters[2] != (serverapi.WorkflowParameter{Key: "chosen_thinking", Description: "pick a level", Purpose: "target_thinking"}) {
		t.Fatalf("thinking parameter = %+v", req.Parameters[2])
	}
}

func TestWorkflowEdgeUpdateRetainsSelectorsAndProtectedRowsWhenReplacingOrdinaryParameters(t *testing.T) {
	edge := serverapi.WorkflowEdge{
		ID:                "edge",
		TransitionGroupID: "group",
		Key:               "edge",
		TargetNodeID:      "target",
		ContextMode:       "new_session",
		AssigneeSelection: "previous_node",
		ThinkingSelection: "previous_node",
		Parameters: []serverapi.WorkflowParameter{
			{Key: "old", Description: "old", Purpose: "ordinary"},
			{Key: "role", Description: "custom role", Purpose: "target_assignee"},
			{Key: "thinking", Purpose: "target_thinking"},
		},
	}
	remote := workflowEdgeMutationDefinition(t, edge)
	exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
		"edge", "update", remote.expected.String(), "edge",
		"--param", "new=updated",
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	req := remote.updateRequest
	if req == nil {
		t.Fatal("UpdateWorkflowEdge was not called")
	}
	if req.AssigneeSelection != "previous_node" || req.ThinkingSelection != "previous_node" {
		t.Fatalf("selectors = %q/%q", req.AssigneeSelection, req.ThinkingSelection)
	}
	if len(req.Parameters) != 3 ||
		req.Parameters[0].Key != "new" ||
		req.Parameters[1] != edge.Parameters[1] ||
		req.Parameters[2] != edge.Parameters[2] {
		t.Fatalf("parameters = %+v, want ordinary replacement with protected rows retained", req.Parameters)
	}
}

func TestWorkflowEdgeUpdateClearParamsRetainsDormantProtectedRows(t *testing.T) {
	edge := serverapi.WorkflowEdge{
		ID:                "edge",
		TransitionGroupID: "group",
		Key:               "edge",
		TargetNodeID:      "target",
		ContextMode:       "new_session",
		AssigneeSelection: "configured",
		ThinkingSelection: "previous_node",
		Parameters: []serverapi.WorkflowParameter{
			{Key: "ordinary", Description: "ordinary", Purpose: "ordinary"},
			{Key: "thinking_level", Description: "authored", Purpose: "target_thinking"},
		},
	}
	remote := workflowEdgeMutationDefinition(t, edge)
	exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
		"edge", "update", remote.expected.String(), "edge",
		"--clear-params",
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	if got := remote.updateRequest.Parameters; len(got) != 1 || got[0] != edge.Parameters[1] {
		t.Fatalf("parameters = %+v, want dormant protected row", got)
	}
}

func TestWorkflowEdgeUpdateAllowsIndependentSelectorChanges(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		want    string
		purpose string
	}{
		{name: "assignee", flag: "assignee-selection", want: "previous_node", purpose: "target_assignee"},
		{name: "thinking", flag: "thinking-selection", want: "previous_node", purpose: "target_thinking"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
				ID:                "edge",
				TransitionGroupID: "group",
				Key:               "edge",
				TargetNodeID:      "target",
				ContextMode:       "new_session",
				AssigneeSelection: "configured",
				ThinkingSelection: "configured",
			})
			exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
				"edge", "update", remote.expected.String(), "edge",
				"--"+test.flag, test.want,
			)
			if exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
			}
			if remote.updateRequest == nil {
				t.Fatal("UpdateWorkflowEdge was not called")
			}
			if test.name == "assignee" && (remote.updateRequest.AssigneeSelection != test.want || remote.updateRequest.ThinkingSelection != "configured") {
				t.Fatalf("selectors = %q/%q", remote.updateRequest.AssigneeSelection, remote.updateRequest.ThinkingSelection)
			}
			if test.name == "thinking" && (remote.updateRequest.AssigneeSelection != "configured" || remote.updateRequest.ThinkingSelection != test.want) {
				t.Fatalf("selectors = %q/%q", remote.updateRequest.AssigneeSelection, remote.updateRequest.ThinkingSelection)
			}
			if len(remote.updateRequest.Parameters) != 1 || remote.updateRequest.Parameters[0].Purpose != test.purpose {
				t.Fatalf("parameters = %+v, want initialized %s row", remote.updateRequest.Parameters, test.purpose)
			}
		})
	}
}

func TestWorkflowEdgeUpdateDisablesSelectorsIndependentlyAndRetainsProtectedRows(t *testing.T) {
	tests := []struct {
		name         string
		flag         string
		wantAssignee string
		wantThinking string
		wantPurpose  string
	}{
		{name: "assignee", flag: "assignee-selection", wantAssignee: "configured", wantThinking: "previous_node", wantPurpose: "target_assignee"},
		{name: "thinking", flag: "thinking-selection", wantAssignee: "previous_node", wantThinking: "configured", wantPurpose: "target_thinking"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
				ID: "edge", TransitionGroupID: "group", Key: "edge", TargetNodeID: "target", ContextMode: "new_session",
				AssigneeSelection: "previous_node", ThinkingSelection: "previous_node",
				Parameters: []serverapi.WorkflowParameter{
					{Key: "role", Purpose: "target_assignee"},
					{Key: "thinking", Purpose: "target_thinking"},
				},
			})
			exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
				"edge", "update", remote.expected.String(), "edge", "--"+test.flag, "configured",
			)
			if exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
			}
			req := remote.updateRequest
			if req == nil || req.AssigneeSelection != test.wantAssignee || req.ThinkingSelection != test.wantThinking {
				t.Fatalf("request = %+v, want independent selector disable", req)
			}
			if len(req.Parameters) != 2 || req.Parameters[0].Purpose != "target_assignee" || req.Parameters[1].Purpose != "target_thinking" {
				t.Fatalf("parameters = %+v, want both protected rows retained; disabled purpose=%s", req.Parameters, test.wantPurpose)
			}
		})
	}
}

func TestWorkflowEdgeUpdateProtectedCustomizationRequiresEnabledSelector(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
		ID: "edge", TransitionGroupID: "group", Key: "edge", TargetNodeID: "target",
		ContextMode: "new_session", AssigneeSelection: "configured", ThinkingSelection: "configured",
	})
	exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
		"edge", "update", remote.expected.String(), "edge",
		"--target-assignee-param", "role=",
	)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "assignee selection") {
		t.Fatalf("stderr = %q, want disabled selector diagnostic", stderr)
	}
	if remote.updateRequest != nil {
		t.Fatal("UpdateWorkflowEdge called for disabled protected customization")
	}
}

func TestWorkflowEdgeAddForwardsInapplicableSelectionError(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{})
	remote.addError = errors.New("edge selector is inapplicable")
	exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
		"edge", "add", remote.expected.String(),
		"--from", "source", "--transition", "next", "--edge-key", "new", "--to", "target", "--context", "new_session",
		"--assignee-selection", "previous_node",
	)
	if exitCode != 1 || !strings.Contains(stderr, "inapplicable") {
		t.Fatalf("exit code = %d, stderr = %q, want forwarded server error", exitCode, stderr)
	}
}

func TestWorkflowEdgeCommandsRejectInvalidSelectionModes(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{})
		exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
			"edge", "add", remote.expected.String(),
			"--from", "source", "--transition", "next", "--edge-key", "new", "--to", "target", "--context", "new_session",
			"--assignee-selection", "invalid",
		)
		if exitCode != 2 || !strings.Contains(stderr, "must be configured or previous_node") {
			t.Fatalf("exit code = %d, stderr = %q, want invalid add mode diagnostic", exitCode, stderr)
		}
		if remote.addRequest != nil {
			t.Fatal("AddWorkflowEdge called for invalid selection mode")
		}
	})
	t.Run("update", func(t *testing.T) {
		remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
			ID: "edge", TransitionGroupID: "group", Key: "edge", TargetNodeID: "target",
			AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session",
		})
		exitCode, _, stderr := runWorkflowEdgeCommand(t, &remote,
			"edge", "update", remote.expected.String(), "edge", "--thinking-selection", "invalid",
		)
		if exitCode != 2 || !strings.Contains(stderr, "must be configured or previous_node") {
			t.Fatalf("exit code = %d, stderr = %q, want invalid update mode diagnostic", exitCode, stderr)
		}
		if remote.updateRequest != nil {
			t.Fatal("UpdateWorkflowEdge called for invalid selection mode")
		}
	})
}

func TestWorkflowNodeCommandsDoNotAcceptEdgeSelectorFlags(t *testing.T) {
	workflowID := testWorkflowID(t, "cli-node-no-edge-selector")
	remote := &workflowEdgeMutationRemote{
		workflowSelectorInventoryRemote: workflowSelectorInventoryRemote{expected: workflowID},
		definitionValue:                 serverapi.WorkflowDefinition{Workflow: workflowRecordForTest(workflowID)},
	}
	exitCode, _, stderr := runWorkflowEdgeCommand(t, remote,
		"node", "add", workflowID.String(), "--key", "node", "--kind", "agent", "--assignee-selection", "previous_node",
	)
	if exitCode != 2 || !strings.Contains(stderr, "flag provided but not defined") {
		t.Fatalf("exit code = %d, stderr = %q, want Node command to reject Edge selector flags", exitCode, stderr)
	}
}

func TestWorkflowInspectIdentifiesSelectorModesAndProtectedPurposes(t *testing.T) {
	workflowID := testWorkflowID(t, "cli-inspect-selection")
	edge := serverapi.WorkflowEdge{
		ID: "edge", WorkflowID: workflowID, TransitionGroupID: "group", Key: "edge", TargetNodeID: "target",
		AssigneeSelection: "previous_node", ThinkingSelection: "configured", ContextMode: "new_session",
		Parameters: []serverapi.WorkflowParameter{
			{Key: "ordinary", Description: "ordinary", Purpose: "ordinary"},
			{Key: "role", Purpose: "target_assignee"},
		},
	}
	def := serverapi.WorkflowDefinition{
		Workflow:         workflowRecordForTest(workflowID),
		Nodes:            []serverapi.WorkflowNode{{ID: "source", Key: "source", Kind: "agent"}, {ID: "target", Key: "target", Kind: "agent"}},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{{ID: "group", SourceNodeID: "source", TransitionID: "next"}},
		Edges:            []serverapi.WorkflowEdge{edge},
	}
	var output bytes.Buffer
	writeWorkflowDefinitionTransitions(&output, def)
	text := output.String()
	for _, want := range []string{"assignee selection: previous_node", "thinking selection: configured", "ordinary (ordinary)", "role (target_assignee)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspect output = %q, missing %q", text, want)
		}
	}
}
