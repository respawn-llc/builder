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
func (d *workspaceChatDraftGatewayDeps) SessionLaunchClientForProjectWorkspace(_ context.Context, _ string, root string) (apicontract.SessionLaunchService, error) {
	d.defaultRoot = root; return d.launch, nil
}
func (d *workspaceChatDraftGatewayDeps) SessionLaunchClientForProjectWorkspaceID(_ context.Context, _ string, workspaceID string) (apicontract.SessionLaunchService, error) {
	d.selectedID = workspaceID; return d.launch, nil
}
func TestGatewayWorkspaceChatDraftDispatchUsesAttachedWorkspace(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	stub := &workspaceChatDraftGatewayLaunch{}
	deps := &workspaceChatDraftGatewayDeps{Core: appCore, launch: stub}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version})
	if err != nil { t.Fatal(err) }
	handler := gatewayUnaryHandlerEntries[protocol.MethodSessionWorkspaceChatDraft]
	req := protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: "draft", Method: protocol.MethodSessionWorkspaceChatDraft, Params: mustJSON(t, serverapi.WorkspaceChatDraftRequest{Operation: serverapi.WorkspaceChatDraftOperation{Kind: serverapi.WorkspaceChatDraftReadMessage}})}
	response := handler(gateway, context.Background(), &connectionState{attachedProject: appCore.ProjectID(), attachedWorkspaceRoot: "root"}, req)
	if response.Error != nil || stub.calls != 1 || deps.defaultRoot != "root" { t.Fatalf("default dispatch response=%+v calls=%d root=%q", response.Error, stub.calls, deps.defaultRoot) }
	response = handler(gateway, context.Background(), &connectionState{attachedProject: appCore.ProjectID(), attachedWorkspaceID: "workspace"}, req)
	if response.Error != nil || stub.calls != 2 || deps.selectedID != "workspace" { t.Fatalf("selected dispatch response=%+v calls=%d workspace=%q", response.Error, stub.calls, deps.selectedID) }
	unbound, _ := newGatewayTestCore(t, false, true)
	t.Cleanup(func() { _ = unbound.Close() })
	gateway, err = NewGateway(&workspaceChatDraftGatewayDeps{Core: unbound, launch: stub}, protocol.ServerIdentity{ProtocolVersion: protocol.Version})
	if err != nil { t.Fatal(err) }
	response = handler(gateway, context.Background(), &connectionState{}, req)
	if response.Error == nil { t.Fatal("unattached dispatch unexpectedly succeeded") }
}
