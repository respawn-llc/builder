package client

import (
	"context"
	"errors"
	"testing"

	servicecontract "core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type fakeCapabilityFactsService struct {
	servicecontract.CapabilityFactsService
	req serverapi.CapabilityFactsRequest
	err error
}

func (s *fakeCapabilityFactsService) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	s.req = req
	if s.err != nil {
		return serverapi.CapabilityFactsResponse{}, s.err
	}
	return serverapi.CapabilityFactsResponse{Defaults: serverapi.CapabilityDefaultFacts{PrimaryModelID: "gpt-5.5"}}, nil
}

func TestLoopbackCapabilityFactsClientCallsServiceAndSurfacesErrors(t *testing.T) {
	service := &fakeCapabilityFactsService{}
	client := NewLoopbackCapabilityFactsClient(service)
	workspace := t.TempDir()

	resp, err := client.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{WorkspaceRoot: &workspace})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}
	if resp.Defaults.PrimaryModelID != "gpt-5.5" || service.req.WorkspaceRoot == nil || *service.req.WorkspaceRoot != workspace {
		t.Fatalf("response=%+v req=%+v", resp, service.req)
	}

	service.err = serverapi.ErrUnsupportedProvider
	_, err = client.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if !errors.Is(err, serverapi.ErrUnsupportedProvider) {
		t.Fatalf("GetCapabilityFacts error = %v, want ErrUnsupportedProvider", err)
	}
}

func TestProtocolErrorReconstructsUnsupportedProvider(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeUnsupportedProvider, Message: "unsupported llm provider: nope"})

	if !errors.Is(err, serverapi.ErrUnsupportedProvider) {
		t.Fatalf("reconstructed error = %v, want ErrUnsupportedProvider", err)
	}
}
