package main

import (
	"bytes"
	"context"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowNodeGraphMutationRemote struct {
	workflowSelectorInventoryRemote
	definitionValue serverapi.WorkflowDefinition
	previewResponse serverapi.WorkflowGraphSavePreviewResponse
	saveResponse    serverapi.WorkflowGraphSaveResponse
	previewRequest  *serverapi.WorkflowGraphSavePreviewRequest
	saveRequest     *serverapi.WorkflowGraphSaveRequest
	rowMutationCall string
}

func (r *workflowNodeGraphMutationRemote) GetWorkflow(_ context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	r.record(req.WorkflowID)
	return serverapi.WorkflowGetResponse{Definition: r.definitionValue}, nil
}
func (r *workflowNodeGraphMutationRemote) PreviewWorkflowGraphSave(_ context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.previewRequest = &req
	return r.previewResponse, nil
}
func (r *workflowNodeGraphMutationRemote) SaveWorkflowGraph(_ context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.saveRequest = &req
	return r.saveResponse, nil
}
func (r *workflowNodeGraphMutationRemote) AddWorkflowNode(context.Context, serverapi.WorkflowNodeAddRequest) (serverapi.WorkflowNodeAddResponse, error) {
	r.rowMutationCall = "add"
	return serverapi.WorkflowNodeAddResponse{}, nil
}
func (r *workflowNodeGraphMutationRemote) UpdateWorkflowNode(context.Context, serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	r.rowMutationCall = "update"
	return serverapi.WorkflowNodeUpdateResponse{}, nil
}

func workflowGraphSavePreviewForCommandTest(version int64, changed bool) serverapi.WorkflowGraphSavePreviewResponse {
	return serverapi.WorkflowGraphSavePreviewResponse{
		CurrentVersion: version, Changed: changed, CanSave: true,
		Impact:   serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{},
	}
}

func workflowGraphSaveResponseForCommandTest(version int64, changed bool) serverapi.WorkflowGraphSaveResponse {
	return serverapi.WorkflowGraphSaveResponse{
		Saved: true, Changed: changed, CurrentVersion: version, CanSave: true,
		Impact:   serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{},
	}
}

func TestWorkflowNodeAddUsesAtomicGraphSaveWithCanonicalID(t *testing.T) {
	id := testWorkflowID(t, "node-graph-mutation")
	remote := &workflowNodeGraphMutationRemote{
		workflowSelectorInventoryRemote: workflowSelectorInventoryRemote{expected: id},
		definitionValue: serverapi.WorkflowDefinition{
			Workflow:         serverapi.WorkflowRecord{ID: id, Version: 7},
			Nodes:            []serverapi.WorkflowNode{{ID: "legacy", WorkflowID: id, Key: "start", Kind: "start", DisplayName: "Start"}},
			TransitionGroups: []serverapi.WorkflowTransitionGroup{},
			Edges:            []serverapi.WorkflowEdge{},
		},
		previewResponse: workflowGraphSavePreviewForCommandTest(7, true),
		saveResponse:    workflowGraphSaveResponseForCommandTest(8, true),
	}
	installWorkflowCommandRemote(t, remote)
	var stdout, stderr bytes.Buffer
	exit := workflowSubcommand([]string{"node", "add", id.String(), "--key", "agent", "--kind", "agent"}, &stdout, &stderr)
	if exit != 0 || remote.previewRequest == nil || remote.saveRequest == nil || remote.rowMutationCall != "" {
		t.Fatalf("exit=%d preview=%v save=%v row=%q stderr=%q", exit, remote.previewRequest != nil, remote.saveRequest != nil, remote.rowMutationCall, stderr.String())
	}
	added := remote.previewRequest.Graph.Nodes[1]
	if _, err := runtimeids.ParseCanonicalUUIDv4(added.ID, "node_id"); err != nil {
		t.Fatalf("Node ID = %q: %v", added.ID, err)
	}
}
