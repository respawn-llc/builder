package transport
import ("testing"; "core/shared/protocol")
func TestGatewayHasWorkspaceChatDraftHandler(t *testing.T) { if _, ok := gatewayUnaryHandlerEntries[protocol.MethodSessionWorkspaceChatDraft]; !ok { t.Fatal("handler missing") } }
