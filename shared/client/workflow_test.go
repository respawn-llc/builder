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
	start   serverapi.WorkflowTaskStartResponse
	move    serverapi.WorkflowTaskMoveResponse
	approve serverapi.WorkflowTaskApproveResponse
	task    serverapi.WorkflowTaskGetResponse
}

func (s *fakeWorkflowService) CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error) {
	s.created = req
	return serverapi.WorkflowCreateResponse{Workflow: serverapi.WorkflowRecord{ID: "workflow-1", Name: req.Name}}, nil
}

func (s *fakeWorkflowService) ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	s.listReq = req
	return serverapi.WorkflowTaskListResponse{ProjectID: *req.ProjectID, WorkflowID: *req.WorkflowID}, nil
}

func (s *fakeWorkflowService) StartWorkflowTask(context.Context, serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	return s.start, nil
}

func (s *fakeWorkflowService) MoveWorkflowTask(context.Context, serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	return s.move, nil
}

func (s *fakeWorkflowService) ApproveWorkflowTask(context.Context, serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error) {
	return s.approve, nil
}

func (s *fakeWorkflowService) GetWorkflowTask(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return s.task, nil
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

func TestLoopbackWorkflowClientRejectsInvalidInitiatingActionResponses(t *testing.T) {
	service := &fakeWorkflowService{}
	client := NewLoopbackWorkflowClient(service)

	if _, err := client.StartWorkflowTask(context.Background(), serverapi.WorkflowTaskStartRequest{}); err == nil {
		t.Fatal("StartWorkflowTask accepted an invalid response")
	}
	if _, err := client.MoveWorkflowTask(context.Background(), serverapi.WorkflowTaskMoveRequest{}); err == nil {
		t.Fatal("MoveWorkflowTask accepted an invalid response")
	}
	if _, err := client.ApproveWorkflowTask(context.Background(), serverapi.WorkflowTaskApproveRequest{}); err == nil {
		t.Fatal("ApproveWorkflowTask accepted an invalid response")
	}
}

func TestLoopbackWorkflowClientRejectsInvalidTaskDetailResponse(t *testing.T) {
	blankRoot := " "
	service := &fakeWorkflowService{task: serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
		ExecutionTarget: &serverapi.WorkflowExecutionTarget{
			Mode:          serverapi.WorkflowExecutionTargetModeNone,
			EffectiveRoot: &blankRoot,
			Provenance:    serverapi.WorkflowExecutionTargetProvenanceResolved,
		},
	}}}
	client := NewLoopbackWorkflowClient(service)
	if _, err := client.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{TaskID: "task-1"}); err == nil {
		t.Fatal("GetWorkflowTask accepted an invalid response")
	}
}
