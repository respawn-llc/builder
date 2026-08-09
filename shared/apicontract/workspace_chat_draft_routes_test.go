package apicontract
import ("reflect"; "testing"; "core/shared/protocol"; "core/shared/serverapi")
func TestWorkspaceChatDraftRoute(t *testing.T) {
	r, ok := RouteByMethod(protocol.MethodSessionWorkspaceChatDraft)
	if !ok || r.Scope != ScopeProjectWorkspace || r.Dependency != DependencySessionLaunch || r.Kind != KindUnary || r.RequestType != reflect.TypeOf(serverapi.WorkspaceChatDraftRequest{}) || r.ResponseType != reflect.TypeOf(serverapi.WorkspaceChatDraftResponse{}) { t.Fatalf("route=%+v", r) }
}
