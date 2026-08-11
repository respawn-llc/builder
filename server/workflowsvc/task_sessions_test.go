package workflowsvc

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
)

type workflowTaskSessionReadModelStub struct {
	response serverapi.WorkflowTaskSessionListResponse
	err      error
	requests []serverapi.WorkflowTaskOffsetPageRequest
}

func (s *workflowTaskSessionReadModelStub) List(_ context.Context, request serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error) {
	s.requests = append(s.requests, request)
	return s.response, s.err
}

func TestServiceListWorkflowTaskSessionsReturnsStructuredPage(t *testing.T) {
	stub := &workflowTaskSessionReadModelStub{
		response: serverapi.WorkflowTaskSessionListResponse{
			TaskID: "task-1",
			WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{
				Items: []serverapi.WorkflowTaskSessionItem{{
					SessionID: "session-1",
					AgentRole: "coder",
					Status:    serverapi.WorkflowTaskSessionStatusRunning,
				}},
			},
		},
	}
	service := &Service{readModels: ReadModels{TaskSessions: stub}}

	response, err := service.ListWorkflowTaskSessions(context.Background(), serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: "task-1",
	})
	if err != nil {
		t.Fatalf("ListWorkflowTaskSessions: %v", err)
	}
	if len(stub.requests) != 1 ||
		response.TaskID != "task-1" ||
		len(response.Items) != 1 ||
		response.Items[0].SessionID != "session-1" {
		t.Fatalf("response = %+v, requests = %+v", response, stub.requests)
	}
}

func TestServiceListWorkflowTaskSessionsValidatesRequestAndResponse(t *testing.T) {
	operationalErr := errors.New("storage unavailable")
	for name, test := range map[string]struct {
		stub    *workflowTaskSessionReadModelStub
		request serverapi.WorkflowTaskOffsetPageRequest
	}{
		"blank Task ID": {
			stub:    &workflowTaskSessionReadModelStub{},
			request: serverapi.WorkflowTaskOffsetPageRequest{TaskID: " "},
		},
		"mismatched response Task ID": {
			stub: &workflowTaskSessionReadModelStub{response: serverapi.WorkflowTaskSessionListResponse{
				TaskID:             "task-other",
				WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{Items: []serverapi.WorkflowTaskSessionItem{}},
			}},
			request: serverapi.WorkflowTaskOffsetPageRequest{TaskID: "task-1"},
		},
		"operational error": {
			stub:    &workflowTaskSessionReadModelStub{err: operationalErr},
			request: serverapi.WorkflowTaskOffsetPageRequest{TaskID: "task-1"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := &Service{readModels: ReadModels{TaskSessions: test.stub}}
			_, err := service.ListWorkflowTaskSessions(context.Background(), test.request)
			if err == nil {
				t.Fatal("ListWorkflowTaskSessions succeeded")
			}
			if name == "operational error" && !errors.Is(err, operationalErr) {
				t.Fatalf("error = %v, want operational error", err)
			}
			if name == "blank Task ID" && len(test.stub.requests) != 0 {
				t.Fatalf("invalid request reached read model: %+v", test.stub.requests)
			}
		})
	}
}
