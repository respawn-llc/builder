package transport

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type validationRoutingWorktreeService struct {
	apicontract.WorktreeService
	apicontract.WorktreeTrustedService
	rawCalls     int
	trustedCalls int
	request      serverapi.WorktreeCreateRequest
}

func (s *validationRoutingWorktreeService) CreateWorktree(
	context.Context,
	serverapi.WorktreeCreateRequest,
) (serverapi.WorktreeCreateResponse, error) {
	s.rawCalls++
	return serverapi.WorktreeCreateResponse{}, errors.New("raw Worktree create called")
}

func (s *validationRoutingWorktreeService) CreateWorktreeValidated(
	_ context.Context,
	request apicontract.Validated[serverapi.WorktreeCreateRequest],
	_ apicontract.AuthorizedSessionInActiveProject,
) (serverapi.WorktreeCreateResponse, error) {
	s.trustedCalls++
	s.request = request.Value()
	return serverapi.WorktreeCreateResponse{}, errors.New("trusted Worktree create reached")
}

type validationRoutingWorktreeDependencies struct {
	*core.Core
	worktrees apicontract.WorktreeService
}

func (d *validationRoutingWorktreeDependencies) WorktreeClient() apicontract.WorktreeService {
	return d.worktrees
}

func TestGatewayWorktreeCreateValidatesBeforeTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	sessionStore := createGatewayAuthoritativeSession(t, appCore)
	service := &validationRoutingWorktreeService{}
	gateway, err := NewGateway(
		&validationRoutingWorktreeDependencies{Core: appCore, worktrees: service},
		protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true, attachedProject: appCore.ProjectID()}

	malformed := serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		SessionID:        " \t ",
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/malformed",
	}
	response := gateway.dispatch(t.Context(), state, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "malformed-worktree-create",
		Method:  protocol.MethodWorktreeCreate,
		Params:  mustJSON(t, malformed),
	})
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorktreeCreate {
		t.Fatalf("malformed Worktree create response = %+v, want typed create validation error", response.Error)
	}
	if service.rawCalls != 0 || service.trustedCalls != 0 {
		t.Fatalf("malformed owner calls: raw=%d trusted=%d", service.rawCalls, service.trustedCalls)
	}

	valid := malformed
	valid.SessionID = sessionStore.Meta().SessionID
	response = gateway.dispatch(t.Context(), state, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "valid-worktree-create",
		Method:  protocol.MethodWorktreeCreate,
		Params:  mustJSON(t, valid),
	})
	if response.Error == nil {
		t.Fatal("valid Worktree create unexpectedly succeeded")
	}
	if service.rawCalls != 0 || service.trustedCalls != 1 {
		t.Fatalf("valid owner calls: raw=%d trusted=%d", service.rawCalls, service.trustedCalls)
	}
	if service.request != valid {
		t.Fatalf("trusted request = %+v, want %+v", service.request, valid)
	}
}

func TestWorktreeCreatePathHasOneSemanticValidationBoundary(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../worktree/service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse Worktree service: %v", err)
	}
	var rawCreate *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv != nil && function.Name.Name == "CreateWorktree" {
			rawCreate = function
			break
		}
	}
	if rawCreate == nil {
		t.Fatal("raw Worktree CreateWorktree method is missing")
	}
	withValidatedCalls := 0
	ast.Inspect(rawCreate.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "WithValidated" {
			withValidatedCalls++
		}
		return true
	})
	if withValidatedCalls != 1 {
		t.Fatalf("raw Worktree create WithValidated calls = %d, want 1", withValidatedCalls)
	}
}
