package client

import (
	"context"
	"testing"

	servicecontract "core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type fakeSessionViewService struct {
	servicecontract.SessionViewService
	latestAnswerRequest  serverapi.SessionLatestCommittedAssistantFinalAnswerRequest
	answer               *string
	workspaceRootRequest serverapi.SessionExecutionWorkspaceRootRequest
	workspaceRoot        string
}

func (s *fakeSessionViewService) GetLatestCommittedAssistantFinalAnswer(_ context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	s.latestAnswerRequest = req
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{Answer: s.answer}, nil
}

func (s *fakeSessionViewService) GetSessionExecutionWorkspaceRoot(_ context.Context, req serverapi.SessionExecutionWorkspaceRootRequest) (serverapi.SessionExecutionWorkspaceRootResponse, error) {
	s.workspaceRootRequest = req
	return serverapi.SessionExecutionWorkspaceRootResponse{WorkspaceRoot: s.workspaceRoot}, nil
}

func TestLoopbackSessionViewClientGetsLatestCommittedAssistantFinalAnswer(t *testing.T) {
	answer := "durable answer"
	service := &fakeSessionViewService{answer: &answer}
	client := NewLoopbackSessionViewClient(service)

	resp, err := client.GetLatestCommittedAssistantFinalAnswer(context.Background(), serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("GetLatestCommittedAssistantFinalAnswer: %v", err)
	}
	if service.latestAnswerRequest.SessionID != "session-1" || resp.Answer == nil || *resp.Answer != answer {
		t.Fatalf("request=%+v response=%+v", service.latestAnswerRequest, resp)
	}
}

func TestLoopbackSessionViewClientGetsSessionExecutionWorkspaceRoot(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	service := &fakeSessionViewService{workspaceRoot: "/workspace"}
	client := NewLoopbackSessionViewClient(service)

	resp, err := client.GetSessionExecutionWorkspaceRoot(context.Background(), serverapi.SessionExecutionWorkspaceRootRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("GetSessionExecutionWorkspaceRoot: %v", err)
	}
	if service.workspaceRootRequest.SessionID != sessionID || resp.WorkspaceRoot != "/workspace" {
		t.Fatalf("request=%+v response=%+v", service.workspaceRootRequest, resp)
	}
}
