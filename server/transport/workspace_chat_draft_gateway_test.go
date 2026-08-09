package transport

import (
	"context"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type workspaceChatDraftGatewayLaunch struct{ calls int }
func (s *workspaceChatDraftGatewayLaunch) PlanSession(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	return serverapi.SessionPlanResponse{}, nil
}
func (s *workspaceChatDraftGatewayLaunch) WorkspaceChatDraft(context.Context, serverapi.WorkspaceChatDraftRequest) (serverapi.WorkspaceChatDraftResponse, error) {
	s.calls++; return serverapi.WorkspaceChatDraftResponse{Message: "handled"}, nil
}
type workspaceChatDraftGatewayDeps struct {
	*core.Core
	launch apicontract.SessionLaunchService
	defaultRoot, selectedID string
}
func (d *workspaceChatDraftGatewayDeps) SessionLaunchClientForProjectWorkspace(context.Context, string, string) (apicontract.SessionLaunchService, error) {
	d.defaultRoot = "default"; return d.launch, nil
}
func (d *workspaceChatDraftGatewayDeps) SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (apicontract.SessionLaunchService, error) {
	d.selectedID = "selected"; return d.launch, nil
}

func TestGatewayWorkspaceChatDraftDispatchUsesAttachedWorkspace(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	stub := &workspaceChatDraftGatewayLaunch{}
	gateway, err := NewGateway(&workspaceChatDraftGatewayDeps{Core: appCore, launch: stub}, protocol.ServerIdentity{ProtocolVersion: protocol.Version})
	if err != nil { t.Fatal(err) }
	handler := gatewayUnaryHandlerEntries[protocol.MethodSessionWorkspaceChatDraft]
	req := protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: "draft", Method: protocol.MethodSessionWorkspaceChatDraft, Params: mustJSON(t, serverapi.WorkspaceChatDraftRequest{Operation: serverapi.WorkspaceChatDraftOperation{Kind: serverapi.WorkspaceChatDraftReadMessage}})}
	response := handler(gateway, context.Background(), &connectionState{attachedProject: appCore.ProjectID(), attachedWorkspaceRoot: "root"}, req)
	if response.Error != nil || stub.calls != 1 { t.Fatalf("default dispatch response=%+v calls=%d", response.Error, stub.calls) }
	response = handler(gateway, context.Background(), &connectionState{attachedProject: appCore.ProjectID(), attachedWorkspaceID: "workspace"}, req)
	if response.Error != nil || stub.calls != 2 { t.Fatalf("selected dispatch response=%+v calls=%d", response.Error, stub.calls) }
	unbound, _ := newGatewayTestCore(t, false, true)
	t.Cleanup(func() { _ = unbound.Close() })
	gateway, err = NewGateway(&workspaceChatDraftGatewayDeps{Core: unbound, launch: stub}, protocol.ServerIdentity{ProtocolVersion: protocol.Version})
	if err != nil { t.Fatal(err) }
	response = handler(gateway, context.Background(), &connectionState{}, req)
	if response.Error == nil { t.Fatal("unattached dispatch unexpectedly succeeded") }
}
