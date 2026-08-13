package transport

import (
	"context"
	"errors"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var errRawPromptViewServiceCalled = errors.New("raw Prompt view service called")

type promptViewRoutingService struct {
	askRawCalls          int
	askTrustedCalls      int
	answerRawCalls       int
	answerTrustedCalls   int
	approvalRawCalls     int
	approvalTrustedCalls int
}

func (s *promptViewRoutingService) ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	s.askRawCalls++
	return serverapi.AskListPendingBySessionResponse{}, errRawPromptViewServiceCalled
}

func (s *promptViewRoutingService) ListPendingAsksBySessionValidated(context.Context, apicontract.Validated[serverapi.AskListPendingBySessionRequest]) (serverapi.AskListPendingBySessionResponse, error) {
	s.askTrustedCalls++
	return serverapi.AskListPendingBySessionResponse{}, nil
}

func (s *promptViewRoutingService) AnswerPromptBatch(context.Context, serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error) {
	s.answerRawCalls++
	return serverapi.PromptAnswerBatchResponse{}, errRawPromptViewServiceCalled
}

func (s *promptViewRoutingService) AnswerPromptBatchValidated(_ context.Context, request apicontract.Validated[serverapi.PromptAnswerBatchRequest]) (serverapi.PromptAnswerBatchResponse, error) {
	s.answerTrustedCalls++
	results := make([]serverapi.PromptAnswerBatchResult, 0, len(request.Value().Entries))
	for _, entry := range request.Value().Entries {
		results = append(results, serverapi.PromptAnswerBatchResult{
			PromptID: entry.PromptID,
			Outcome:  serverapi.PromptAnswerBatchOutcomeSkipped,
		})
	}
	return serverapi.PromptAnswerBatchResponse{Results: results}, nil
}

func (*promptViewRoutingService) SubscribeFollowUp(context.Context, serverapi.PromptFollowUpWatchRequest) (serverapi.PromptFollowUpSubscription, error) {
	return nil, errors.New("unused")
}

func (*promptViewRoutingService) SubscribeFollowUpValidated(context.Context, apicontract.Validated[serverapi.PromptFollowUpWatchRequest]) (serverapi.PromptFollowUpSubscription, error) {
	return nil, errors.New("unused")
}

func (s *promptViewRoutingService) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	s.approvalRawCalls++
	return serverapi.ApprovalListPendingBySessionResponse{}, errRawPromptViewServiceCalled
}

func (s *promptViewRoutingService) ListPendingApprovalsBySessionValidated(context.Context, apicontract.Validated[serverapi.ApprovalListPendingBySessionRequest]) (serverapi.ApprovalListPendingBySessionResponse, error) {
	s.approvalTrustedCalls++
	return serverapi.ApprovalListPendingBySessionResponse{}, nil
}

type promptViewRoutingDependencies struct {
	*core.Core
	service *promptViewRoutingService
}

func (d *promptViewRoutingDependencies) AskViewClient() apicontract.AskViewService {
	return d.service
}

func (d *promptViewRoutingDependencies) PromptControlClient() apicontract.PromptControlService {
	return d.service
}

func (d *promptViewRoutingDependencies) ApprovalViewClient() apicontract.ApprovalViewService {
	return d.service
}

func TestGatewayPromptViewsInvokeTrustedOwnersWithoutRawReentry(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	store := createGatewayAuthoritativeSession(t, appCore)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	service := &promptViewRoutingService{}
	gateway, err := NewGateway(
		&promptViewRoutingDependencies{Core: appCore, service: service},
		protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true, attachedProject: appCore.ProjectID()}

	requests := []protocol.Request{
		{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "ask",
			Method:  protocol.MethodAskListPending,
			Params:  mustJSON(t, serverapi.AskListPendingBySessionRequest{SessionID: sessionID.String()}),
		},
		{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "answer",
			Method:  protocol.MethodPromptAnswerBatch,
			Params: mustJSON(t, serverapi.PromptAnswerBatchRequest{
				SessionID: sessionID,
				StepID:    stepID,
				Entries: []serverapi.PromptAnswerBatchEntry{{
					PromptID: "33333333-3333-4333-8333-333333333333",
					Declined: &serverapi.PromptDeclined{},
				}},
			}),
		},
		{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "approval",
			Method:  protocol.MethodApprovalListPending,
			Params:  mustJSON(t, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID.String()}),
		},
	}
	for _, request := range requests {
		response := gateway.dispatch(t.Context(), state, request)
		if response.Error != nil {
			t.Fatalf("%s response = %+v error = %+v", request.Method, response, response.Error)
		}
	}

	if service.askRawCalls != 0 || service.answerRawCalls != 0 || service.approvalRawCalls != 0 {
		t.Fatalf("raw calls: ask=%d answer=%d approval=%d", service.askRawCalls, service.answerRawCalls, service.approvalRawCalls)
	}
	if service.askTrustedCalls != 1 || service.answerTrustedCalls != 1 || service.approvalTrustedCalls != 1 {
		t.Fatalf("trusted calls: ask=%d answer=%d approval=%d", service.askTrustedCalls, service.answerTrustedCalls, service.approvalTrustedCalls)
	}
}
