package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

type workflowGraphInspectRemote struct {
	apicontract.WorkflowService
	definition serverapi.WorkflowDefinition
	closed     int
}

func (r *workflowGraphInspectRemote) GetWorkflow(context.Context, serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	return serverapi.WorkflowGetResponse{Definition: r.definition}, nil
}
func (*workflowGraphInspectRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}
func (r *workflowGraphInspectRemote) Close() error { r.closed++; return nil }

func TestWorkflowGraphInspectWritesCanonicalIdentityBoundDocument(t *testing.T) {
	id := workflowGraphApplyID(t)
	remote := &workflowGraphInspectRemote{definition: serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: id, Version: 7},
		Nodes: []serverapi.WorkflowNode{
			{ID: "node-z", WorkflowID: id, Key: "zeta", Kind: "terminal", DisplayName: "Zeta"},
			{ID: "node-a", WorkflowID: id, Key: "alpha", Kind: "start", DisplayName: "Alpha"},
		},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{},
		Edges:            []serverapi.WorkflowEdge{},
	}}
	installWorkflowCommandRemote(t, remote)
	var stdout, stderr bytes.Buffer
	if exit := workflowSubcommand([]string{"graph", "inspect", id.String()}, &stdout, &stderr); exit != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	var document workflowGraphDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if document.WorkflowID != id || document.ExpectedVersion != 7 || remote.closed != 1 {
		t.Fatalf("document=%+v closed=%d", document, remote.closed)
	}
	if len(document.Graph.Nodes) != 2 || document.Graph.Nodes[0].ID != "node-a" || document.Graph.Nodes[1].ID != "node-z" {
		t.Fatalf("canonical Nodes = %+v", document.Graph.Nodes)
	}
}
