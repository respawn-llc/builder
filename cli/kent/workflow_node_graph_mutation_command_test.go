package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowNodeGraphMutationRemote struct {
	workflowSelectorInventoryRemote
	definitionValue serverapi.WorkflowDefinition
	previewResponse serverapi.WorkflowGraphSavePreviewResponse
	saveResponse    serverapi.WorkflowGraphSaveResponse
	calls           []string
	previewRequest  *serverapi.WorkflowGraphSavePreviewRequest
	saveRequest     *serverapi.WorkflowGraphSaveRequest
	rowMutationCall string
}

func (r *workflowNodeGraphMutationRemote) GetWorkflow(_ context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	r.calls = append(r.calls, "get")
	r.record(req.WorkflowID)
	return serverapi.WorkflowGetResponse{Definition: r.definitionValue}, nil
}

func (r *workflowNodeGraphMutationRemote) PreviewWorkflowGraphSave(_ context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.calls = append(r.calls, "preview")
	r.record(req.WorkflowID)
	r.previewRequest = &req
	return r.previewResponse, nil
}

func (r *workflowNodeGraphMutationRemote) SaveWorkflowGraph(_ context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.calls = append(r.calls, "save")
	r.record(req.WorkflowID)
	r.saveRequest = &req
	return r.saveResponse, nil
}

func (r *workflowNodeGraphMutationRemote) AddWorkflowNode(_ context.Context, req serverapi.WorkflowNodeAddRequest) (serverapi.WorkflowNodeAddResponse, error) {
	r.rowMutationCall = "AddWorkflowNode"
	r.record(req.WorkflowID)
	return serverapi.WorkflowNodeAddResponse{}, nil
}

func (r *workflowNodeGraphMutationRemote) UpdateWorkflowNode(_ context.Context, req serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	r.rowMutationCall = "UpdateWorkflowNode"
	r.record(req.WorkflowID)
	return serverapi.WorkflowNodeUpdateResponse{}, nil
}

func newWorkflowNodeGraphMutationRemote(t *testing.T) *workflowNodeGraphMutationRemote {
	t.Helper()
	workflowID := testWorkflowID(t, "node-graph-mutation")
	definition := serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{
			ID:      workflowID,
			Name:    "Workflow",
			Version: 7,
		},
		NodeGroups: []serverapi.WorkflowNodeGroup{{
			WorkflowID:  workflowID,
			GroupID:     "legacy-group",
			GroupKey:    "delivery",
			DisplayName: "Delivery",
		}},
		Nodes: []serverapi.WorkflowNode{
			{
				ID:           "legacy-source",
				WorkflowID:   workflowID,
				Key:          "source",
				Kind:         "agent",
				DisplayName:  "Source",
				GroupID:      "legacy-group",
				GroupKey:     "delivery",
				SubagentRole: "default",
			},
			{
				ID:          "legacy-target",
				WorkflowID:  workflowID,
				Key:         "target",
				Kind:        "terminal",
				DisplayName: "Target",
			},
		},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{{
			ID:           "legacy-transition",
			WorkflowID:   workflowID,
			SourceNodeID: "legacy-source",
			TransitionID: "done",
			DisplayName:  "Done",
		}},
		Edges: []serverapi.WorkflowEdge{{
			ID:                "legacy-edge",
			WorkflowID:        workflowID,
			TransitionGroupID: "legacy-transition",
			Key:               "done",
			TargetNodeID:      "legacy-target",
			AssigneeSelection: "configured",
			ThinkingSelection: "configured",
			ContextMode:       "new_session",
			ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
		}},
	}
	return &workflowNodeGraphMutationRemote{
		workflowSelectorInventoryRemote: workflowSelectorInventoryRemote{expected: workflowID},
		definitionValue:                 definition,
		previewResponse:                 workflowGraphSavePreviewForCommandTest(7, true),
		saveResponse:                    workflowGraphSaveResponseForCommandTest(8, true),
	}
}

func workflowGraphSavePreviewForCommandTest(version int64, changed bool) serverapi.WorkflowGraphSavePreviewResponse {
	return serverapi.WorkflowGraphSavePreviewResponse{
		CurrentVersion: version,
		Changed:        changed,
		Impact: serverapi.WorkflowGraphSaveImpact{
			RemovedEntities: []serverapi.WorkflowGraphEntityReference{},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{},
		CanSave:  true,
	}
}

func workflowGraphSaveResponseForCommandTest(version int64, changed bool) serverapi.WorkflowGraphSaveResponse {
	return serverapi.WorkflowGraphSaveResponse{
		Saved:          true,
		Changed:        changed,
		CurrentVersion: version,
		Impact: serverapi.WorkflowGraphSaveImpact{
			RemovedEntities: []serverapi.WorkflowGraphEntityReference{},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{},
		CanSave:  true,
	}
}

func runWorkflowNodeCommand(t *testing.T, remote workflowCommandRemote, args ...string) (int, string, string) {
	t.Helper()
	installWorkflowCommandRemote(t, remote)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := workflowSubcommand(args, &stdout, &stderr)
	return exitCode, stdout.String(), stderr.String()
}

func TestWorkflowNodeAddPreviewsAndSavesOneCompleteGraphWithCanonicalID(t *testing.T) {
	remote := newWorkflowNodeGraphMutationRemote(t)

	exitCode, stdout, stderr := runWorkflowNodeCommand(
		t,
		remote,
		"node", "add", remote.expected.String(),
		"--key", "implement",
		"--kind", "agent",
		"--agent", "coder",
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
	if remote.previewRequest == nil || remote.saveRequest == nil {
		t.Fatalf("preview/save requests = %+v/%+v, want both", remote.previewRequest, remote.saveRequest)
	}
	if remote.previewRequest.ExpectedVersion != 7 || remote.saveRequest.ExpectedVersion != 7 {
		t.Fatalf("expected versions = %d/%d, want 7", remote.previewRequest.ExpectedVersion, remote.saveRequest.ExpectedVersion)
	}
	if !reflect.DeepEqual(remote.previewRequest.Graph, remote.saveRequest.Graph) {
		t.Fatalf("save graph differs from preview:\npreview=%+v\nsave=%+v", remote.previewRequest.Graph, remote.saveRequest.Graph)
	}
	graph := remote.previewRequest.Graph
	if len(graph.NodeGroups) != 1 || len(graph.Nodes) != 3 || len(graph.TransitionGroups) != 1 || len(graph.Edges) != 1 {
		t.Fatalf("preview graph is not complete: %+v", graph)
	}
	added := graph.Nodes[2]
	if _, err := runtimeids.ParseCanonicalUUIDv4(added.ID, "node_id"); err != nil {
		t.Fatalf("added Node ID %q is not canonical bare UUID v4: %v", added.ID, err)
	}
	if added.Key != "implement" || added.Kind != "agent" || added.DisplayName != "Implement" || added.SubagentRole != "coder" {
		t.Fatalf("added Node = %+v", added)
	}
	var output workflowNodeOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if output.WorkflowID != remote.expected || output.NodeID != added.ID || output.Key != "implement" || output.Kind != "agent" || output.Version != 8 {
		t.Fatalf("JSON output = %+v", output)
	}
}

func TestWorkflowNodeUpdatePreviewsAndSavesCompleteGraphPreservingSuccessOutput(t *testing.T) {
	remote := newWorkflowNodeGraphMutationRemote(t)

	exitCode, stdout, stderr := runWorkflowNodeCommand(
		t,
		remote,
		"node", "update", remote.expected.String(), "source",
		"--key", "plan",
		"--display-name", "Plan",
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
	if len(remote.previewRequest.Graph.Nodes) != 2 ||
		remote.previewRequest.Graph.Nodes[0].ID != "legacy-source" ||
		remote.previewRequest.Graph.Nodes[0].Key != "plan" ||
		remote.previewRequest.Graph.Nodes[0].DisplayName != "Plan" {
		t.Fatalf("updated complete graph = %+v", remote.previewRequest.Graph)
	}
	var output workflowNodeOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if output.WorkflowID != remote.expected ||
		output.NodeID != "legacy-source" ||
		output.Key != "plan" ||
		output.Kind != "agent" ||
		output.Version != 8 {
		t.Fatalf("JSON output = %+v", output)
	}
}

func TestWorkflowNodeNoopUpdateSucceedsWithoutSaveOrVersionChange(t *testing.T) {
	remote := newWorkflowNodeGraphMutationRemote(t)
	remote.previewResponse = workflowGraphSavePreviewForCommandTest(7, false)

	exitCode, stdout, stderr := runWorkflowNodeCommand(
		t,
		remote,
		"node", "update", remote.expected.String(), "source", "--json",
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
	if !reflect.DeepEqual(remote.calls, []string{"get", "preview"}) {
		t.Fatalf("calls = %v, want get and preview only", remote.calls)
	}
	if remote.saveRequest != nil || remote.rowMutationCall != "" {
		t.Fatalf("save/row mutation = %+v/%q, want none", remote.saveRequest, remote.rowMutationCall)
	}
	var output workflowNodeOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if output.NodeID != "legacy-source" || output.Key != "source" || output.Kind != "agent" || output.Version != 7 {
		t.Fatalf("JSON output = %+v, want unchanged Node at version 7", output)
	}
}

func TestWorkflowNodeMutationBlockersDirectToGraphApplyWithoutSaving(t *testing.T) {
	tests := []struct {
		name                 string
		confirmationRequired bool
		blocker              serverapi.WorkflowGraphSaveBlocker
	}{
		{
			name:                 "confirmation required",
			confirmationRequired: true,
			blocker: serverapi.WorkflowGraphSaveBlocker{
				Code:             "confirmation_required",
				Message:          "Confirm graph removal.",
				Count:            1,
				AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: "legacy-target"}},
			},
		},
		{
			name: "validation blocker",
			blocker: serverapi.WorkflowGraphSaveBlocker{
				Code:             "validation_failed",
				Message:          "Workflow graph has blocking validation errors.",
				Count:            1,
				AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: "legacy-source"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := newWorkflowNodeGraphMutationRemote(t)
			remote.previewResponse = workflowGraphSavePreviewForCommandTest(7, true)
			remote.previewResponse.CanSave = false
			remote.previewResponse.ConfirmationRequired = test.confirmationRequired
			remote.previewResponse.Blockers = []serverapi.WorkflowGraphSaveBlocker{test.blocker}

			exitCode, stdout, stderr := runWorkflowNodeCommand(
				t,
				remote,
				"node", "update", remote.expected.String(), "source",
				"--kind", "terminal",
			)

			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1; stderr = %q", exitCode, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if remote.saveRequest != nil || remote.rowMutationCall != "" {
				t.Fatalf("save/row mutation = %+v/%q, want none", remote.saveRequest, remote.rowMutationCall)
			}
			if !reflect.DeepEqual(remote.calls, []string{"get", "preview"}) {
				t.Fatalf("calls = %v, want get and preview only", remote.calls)
			}
			if stderr == "" {
				t.Fatal("stderr is empty, want surfaced blocker guidance")
			}
		})
	}
}

func TestWorkflowGraphMutationBlockerCarriesTypedGraphApplyResolution(t *testing.T) {
	remote := newWorkflowNodeGraphMutationRemote(t)
	remote.previewResponse = workflowGraphSavePreviewForCommandTest(7, true)
	remote.previewResponse.CanSave = false
	remote.previewResponse.ConfirmationRequired = true
	remote.previewResponse.Blockers = []serverapi.WorkflowGraphSaveBlocker{{
		Code:             "confirmation_required",
		Message:          "Confirm graph removal.",
		Count:            1,
		AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: "legacy-target"}},
	}}
	kind := "terminal"

	_, _, err := runWorkflowGraphMutation(
		context.Background(),
		remote,
		remote.expected,
		updateWorkflowNodeDraftMutation(workflowNodeUpdateDraftMutation{
			NodeKey: "source",
			Kind:    &kind,
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
