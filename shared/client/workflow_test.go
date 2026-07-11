package client

import (
	"context"
	"testing"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type fakeWorkflowService struct {
	servicecontract.WorkflowService
	created serverapi.WorkflowCreateRequest
	listReq serverapi.WorkflowTaskListRequest
}

func (s *fakeWorkflowService) CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error) {
	s.created = req
	return serverapi.WorkflowCreateResponse{Workflow: serverapi.WorkflowRecord{ID: "workflow-1", Name: req.Name}}, nil
}

func (s *fakeWorkflowService) ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	s.listReq = req
	return serverapi.WorkflowTaskListResponse{ProjectID: *req.ProjectID, WorkflowID: *req.WorkflowID}, nil
}

func TestLoopbackWorkflowClientCallsService(t *testing.T) {
	service := &fakeWorkflowService{}
	client := NewLoopbackWorkflowClient(service)
	resp, err := client.CreateWorkflow(context.Background(), serverapi.WorkflowCreateRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if resp.Workflow.ID != "workflow-1" || service.created.Name != "Workflow" {
		t.Fatalf("response=%+v service=%+v", resp, service.created)
	}
	projectID := "project-1"
	workflowID := "workflow-1"
	taskList, err := client.ListWorkflowTasks(context.Background(), serverapi.WorkflowTaskListRequest{ProjectID: &projectID, WorkflowID: &workflowID})
	if err != nil {
		t.Fatalf("ListWorkflowTasks: %v", err)
	}
	if taskList.ProjectID != "project-1" || service.listReq.WorkflowID == nil || *service.listReq.WorkflowID != "workflow-1" {
		t.Fatalf("task list response=%+v service=%+v", taskList, service.listReq)
	}
}
