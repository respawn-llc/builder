package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestWorkflowEdgeAddWithNewTransitionGroupPreviewsAndSavesOneCompleteGraph(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{})
	remote.definitionValue.TransitionGroups[0].TransitionID = "other"
	remote.definitionValue.Edges = nil

	exitCode, stdout, stderr := runWorkflowEdgeCommand(
		t,
		&remote,
		"edge", "add", remote.expected.String(),
		"--from", "source",
		"--transition", "next",
		"--transition-description", "Continue delivery",
		"--edge-key", "new",
		"--to", "target",
		"--context", "new_session",
		"--json",
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	if remote.rowMutationCall != "" {
		t.Fatalf("row mutation call = %q, want none", remote.rowMutationCall)
	}
	if !reflect.DeepEqual(remote.calls, []string{"get", "preview", "save"}) {
		t.Fatalf("calls = %v, want get, preview, save", remote.calls)
	}
	if remote.previewRequest == nil || remote.saveRequest == nil || !reflect.DeepEqual(remote.previewRequest.Graph, remote.saveRequest.Graph) {
		t.Fatalf("preview/save requests = %+v/%+v", remote.previewRequest, remote.saveRequest)
	}
	graph := remote.previewRequest.Graph
	if len(graph.Nodes) != 2 || len(graph.TransitionGroups) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("complete graph = %+v", graph)
	}
	group := graph.TransitionGroups[1]
	edge := graph.Edges[0]
	if _, err := runtimeids.ParseCanonicalUUIDv4(group.ID, "transition_group_id"); err != nil {
		t.Fatalf("new Transition Group ID %q: %v", group.ID, err)
	}
	if _, err := runtimeids.ParseCanonicalUUIDv4(edge.ID, "edge_id"); err != nil {
		t.Fatalf("new Edge ID %q: %v", edge.ID, err)
	}
	if group.SourceNodeID != "source" || group.TransitionID != "next" || group.DisplayName != "Next" || group.Description != "Continue delivery" {
		t.Fatalf("new Transition Group = %+v", group)
	}
	if edge.TransitionGroupID != group.ID || edge.Key != "new" || edge.TargetNodeID != "target" {
		t.Fatalf("new Edge = %+v", edge)
	}
	var output workflowEdgeOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if output.WorkflowID != remote.expected || output.EdgeID != edge.ID || output.TransitionGroupID != group.ID || output.Key != "new" || output.TransitionID != "next" || output.Version != 2 {
		t.Fatalf("JSON output = %+v", output)
	}
}

func TestWorkflowEdgeAddToExistingTransitionGroupMutatesDescriptionAndEdgeTogether(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{})
	remote.definitionValue.Edges = nil

	exitCode, _, stderr := runWorkflowEdgeCommand(
		t,
		&remote,
		"edge", "add", remote.expected.String(),
		"--from", "source",
		"--transition", "next",
		"--transition-description", "Updated guidance",
		"--edge-key", "new",
		"--to", "target",
		"--context", "new_session",
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	graph := remote.previewRequest.Graph
	if len(graph.TransitionGroups) != 1 || graph.TransitionGroups[0].ID != "group" || graph.TransitionGroups[0].Description != "Updated guidance" {
		t.Fatalf("Transition Groups = %+v", graph.TransitionGroups)
	}
	if len(graph.Edges) != 1 || graph.Edges[0].TransitionGroupID != "group" {
		t.Fatalf("Edges = %+v", graph.Edges)
	}
	if remote.rowMutationCall != "" || !reflect.DeepEqual(remote.calls, []string{"get", "preview", "save"}) {
		t.Fatalf("row mutation/calls = %q/%v", remote.rowMutationCall, remote.calls)
	}
}

func TestWorkflowEdgeUpdateMutatesTransitionGroupAndEdgeAtomically(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
		ID:                "edge",
		TransitionGroupID: "group",
		Key:               "old",
		TargetNodeID:      "target",
		AssigneeSelection: "configured",
		ThinkingSelection: "configured",
		ContextMode:       "new_session",
		ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
	})

	exitCode, stdout, stderr := runWorkflowEdgeCommand(
		t,
		&remote,
		"edge", "update", remote.expected.String(), "edge",
		"--transition", "deliver",
		"--transition-display-name", "Deliver",
		"--transition-description", "Ship it",
		"--edge-key", "done",
		"--requires-approval",
		"--json",
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	if !reflect.DeepEqual(remote.calls, []string{"get", "preview", "save"}) || remote.rowMutationCall != "" {
		t.Fatalf("calls/row mutation = %v/%q", remote.calls, remote.rowMutationCall)
	}
	group := remote.previewRequest.Graph.TransitionGroups[0]
	edge := workflowEdgeDraftByID(t, remote.previewRequest, "edge")
	if group.TransitionID != "deliver" || group.DisplayName != "Deliver" || group.Description != "Ship it" {
		t.Fatalf("updated Transition Group = %+v", group)
	}
	if edge.Key != "done" || !edge.RequiresApproval {
		t.Fatalf("updated Edge = %+v", edge)
	}
	if !reflect.DeepEqual(remote.previewRequest.Graph, remote.saveRequest.Graph) {
		t.Fatal("save graph differs from preview graph")
	}
	var output workflowEdgeOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if output.EdgeID != "edge" || output.TransitionGroupID != "group" || output.Key != "done" || output.TransitionID != "deliver" || output.Version != 2 {
		t.Fatalf("JSON output = %+v", output)
	}
}

func TestWorkflowEdgeNoopUpdateSucceedsWithoutSaveOrVersionChange(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
		ID:                "edge",
		TransitionGroupID: "group",
		Key:               "edge",
		TargetNodeID:      "target",
		AssigneeSelection: "configured",
		ThinkingSelection: "configured",
		ContextMode:       "new_session",
		ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
	})
	remote.previewResponse = workflowGraphSavePreviewForCommandTest(1, false)

	exitCode, stdout, stderr := runWorkflowEdgeCommand(
		t,
		&remote,
		"edge", "update", remote.expected.String(), "edge", "--json",
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	if !reflect.DeepEqual(remote.calls, []string{"get", "preview"}) || remote.saveRequest != nil || remote.rowMutationCall != "" {
		t.Fatalf("calls/save/row mutation = %v/%+v/%q", remote.calls, remote.saveRequest, remote.rowMutationCall)
	}
	var output workflowEdgeOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if output.EdgeID != "edge" || output.TransitionGroupID != "group" || output.Key != "edge" || output.TransitionID != "next" || output.Version != 1 {
		t.Fatalf("JSON output = %+v", output)
	}
}

func TestWorkflowEdgeMutationFailuresDoNotSaveAndGuideConfirmationToGraphApply(t *testing.T) {
	tests := []struct {
		name                 string
		confirmationRequired bool
		blocker              serverapi.WorkflowGraphSaveBlocker
	}{
		{
			name: "validation blocker",
			blocker: serverapi.WorkflowGraphSaveBlocker{
				Code:             "validation_failed",
				Message:          "Workflow graph has blocking validation errors.",
				Count:            1,
				AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
			},
		},
		{
			name:                 "confirmation required",
			confirmationRequired: true,
			blocker: serverapi.WorkflowGraphSaveBlocker{
				Code:             "confirmation_required",
				Message:          "Confirm graph removal.",
				Count:            1,
				AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
				ID:                "edge",
				TransitionGroupID: "group",
				Key:               "edge",
				TargetNodeID:      "target",
				AssigneeSelection: "configured",
				ThinkingSelection: "configured",
				ContextMode:       "new_session",
				ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
			})
			remote.previewResponse = workflowGraphSavePreviewForCommandTest(1, true)
			remote.previewResponse.CanSave = false
			remote.previewResponse.ConfirmationRequired = test.confirmationRequired
			remote.previewResponse.Blockers = []serverapi.WorkflowGraphSaveBlocker{test.blocker}

			exitCode, stdout, stderr := runWorkflowEdgeCommand(
				t,
				&remote,
				"edge", "update", remote.expected.String(), "edge",
				"--edge-key", "updated",
			)

			if exitCode != 1 || stdout != "" {
				t.Fatalf("exit code/stdout = %d/%q, stderr = %q", exitCode, stdout, stderr)
			}
			if remote.saveRequest != nil || remote.rowMutationCall != "" || !reflect.DeepEqual(remote.calls, []string{"get", "preview"}) {
				t.Fatalf("save/row mutation/calls = %+v/%q/%v", remote.saveRequest, remote.rowMutationCall, remote.calls)
			}
			if stderr == "" {
				t.Fatal("stderr is empty, want surfaced graph apply guidance")
			}
		})
	}
}

func TestWorkflowEdgeConfirmationBlockerCarriesTypedGraphApplyResolution(t *testing.T) {
	remote := workflowEdgeMutationDefinition(t, serverapi.WorkflowEdge{
		ID:                "edge",
		TransitionGroupID: "group",
		Key:               "edge",
		TargetNodeID:      "target",
		AssigneeSelection: "configured",
		ThinkingSelection: "configured",
		ContextMode:       "new_session",
		ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
	})
	remote.previewResponse = workflowGraphSavePreviewForCommandTest(1, true)
	remote.previewResponse.CanSave = false
	remote.previewResponse.ConfirmationRequired = true
	remote.previewResponse.Blockers = []serverapi.WorkflowGraphSaveBlocker{{
		Code:             "confirmation_required",
		Message:          "Confirm graph removal.",
		Count:            1,
		AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
	}}
	edgeKey := "updated"

	_, _, err := runWorkflowGraphMutation(
		context.Background(),
		&remote,
		remote.expected,
		updateWorkflowEdgeDraftMutation(workflowEdgeUpdateDraftMutation{
			EdgeID:  "edge",
			EdgeKey: &edgeKey,
		}),
	)

	var blocked workflowGraphMutationBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("error type = %T, want workflowGraphMutationBlockedError", err)
	}
	if blocked.WorkflowID != remote.expected ||
		blocked.Resolution != workflowGraphMutationResolutionGraphApply ||
		!reflect.DeepEqual(blocked.BlockerCodes, []string{"confirmation_required"}) {
		t.Fatalf("typed blocker = %+v", blocked)
	}
	if remote.saveRequest != nil {
		t.Fatalf("save request = %+v, want none", remote.saveRequest)
	}
}
