package transport

import (
	"context"
	"errors"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type authorizedWorkspaceWorktreeService struct {
	apicontract.WorktreeService
	rawCalls     int
	trustedCalls int
	request      serverapi.WorktreeWorkspaceListRequest
	binding      apicontract.AuthorizedProjectWorkspaceBinding
}

func (s *authorizedWorkspaceWorktreeService) ListWorkspaceWorktrees(
	context.Context,
	serverapi.WorktreeWorkspaceListRequest,
) (serverapi.WorktreeWorkspaceListResponse, error) {
	s.rawCalls++
	return serverapi.WorktreeWorkspaceListResponse{}, errors.New("raw Worktree Workspace list called")
}

func (s *authorizedWorkspaceWorktreeService) ListWorkspaceWorktreesValidated(
	_ context.Context,
	request apicontract.Validated[serverapi.WorktreeWorkspaceListRequest],
	binding apicontract.AuthorizedProjectWorkspaceBinding,
) (serverapi.WorktreeWorkspaceListResponse, error) {
	s.trustedCalls++
	s.request = request.Value()
	s.binding = binding
	return serverapi.WorktreeWorkspaceListResponse{WorkspaceID: binding.WorkspaceID}, nil
}

type authorizedWorkspaceWorktreeDependencies struct {
	*core.Core
	worktrees apicontract.WorktreeService
}

func (d *authorizedWorkspaceWorktreeDependencies) WorktreeClient() apicontract.WorktreeService {
	return d.worktrees
}

func TestGatewayCarriesAuthorizedProjectWorkspaceBindingToTrustedWorktreeOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	service := &authorizedWorkspaceWorktreeService{}
	gateway, err := NewGateway(
		&authorizedWorkspaceWorktreeDependencies{Core: appCore, worktrees: service},
		protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	binding, err := appCore.MetadataStore().EnsureWorkspaceBinding(t.Context(), appCore.Config().WorkspaceRoot)
	if err != nil {
		t.Fatalf("LookupWorkspaceBindingByID: %v", err)
	}
	request := serverapi.WorktreeWorkspaceListRequest{
		ProjectID:   binding.ProjectID,
		WorkspaceID: binding.WorkspaceID,
	}

	response := gateway.dispatch(t.Context(), &connectionState{
		handshakeDone:         true,
		attachedProject:       binding.ProjectID,
		attachedWorkspaceID:   binding.WorkspaceID,
		attachedWorkspaceRoot: binding.CanonicalRoot,
	}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "workspace-list",
		Method:  protocol.MethodWorktreeWorkspaceList,
		Params:  mustJSON(t, request),
	})
	if response.Error != nil {
		t.Fatalf("dispatch: %+v", response.Error)
	}
	if service.rawCalls != 0 || service.trustedCalls != 1 {
		t.Fatalf("owner calls: raw=%d trusted=%d", service.rawCalls, service.trustedCalls)
	}
	if service.request != request {
		t.Fatalf("trusted request = %+v, want %+v", service.request, request)
	}
	wantBinding := apicontract.AuthorizedProjectWorkspaceBinding{
		ProjectID:     binding.ProjectID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: binding.CanonicalRoot,
	}
	if service.binding != wantBinding {
		t.Fatalf("trusted binding = %+v, want %+v", service.binding, wantBinding)
	}
}
