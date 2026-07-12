package client

import (
	"context"
	"testing"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type fakeSessionViewService struct {
	servicecontract.SessionViewService
	req    serverapi.SessionLatestCommittedAssistantFinalAnswerRequest
	answer *string
}

func (s *fakeSessionViewService) GetLatestCommittedAssistantFinalAnswer(_ context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	s.req = req
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{Answer: s.answer}, nil
}

func TestLoopbackSessionViewClientGetsLatestCommittedAssistantFinalAnswer(t *testing.T) {
	answer := "durable answer"
	service := &fakeSessionViewService{answer: &answer}
	client := NewLoopbackSessionViewClient(service)

	resp, err := client.GetLatestCommittedAssistantFinalAnswer(context.Background(), serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("GetLatestCommittedAssistantFinalAnswer: %v", err)
	}
	if service.req.SessionID != "session-1" || resp.Answer == nil || *resp.Answer != answer {
		t.Fatalf("request=%+v response=%+v", service.req, resp)
	}
}
