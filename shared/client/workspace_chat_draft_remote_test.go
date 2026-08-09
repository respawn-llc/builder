package client
import ("context"; "encoding/json"; "testing"; "core/shared/protocol"; "core/shared/serverapi"; "golang.org/x/net/websocket")
func TestRemoteWorkspaceChatDraftRoundTripsAllOperations(t *testing.T) {
	blank := ""; ops := []serverapi.WorkspaceChatDraftOperation{{Kind: serverapi.WorkspaceChatDraftReadMessage}, {Kind: serverapi.WorkspaceChatDraftUpdateMessage, Message: &blank}, {Kind: serverapi.WorkspaceChatDraftClear}, {Kind: serverapi.WorkspaceChatDraftConsume}}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		for _, op := range ops { if err := websocket.JSON.Receive(ws, &req); err != nil { t.Error(err); return }; var got serverapi.WorkspaceChatDraftRequest; if req.Method != protocol.MethodSessionWorkspaceChatDraft || json.Unmarshal(req.Params, &got) != nil || got.Operation.Kind != op.Kind { t.Fatalf("request=%+v", got) }; _ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkspaceChatDraftResponse{Message: "server"})) }
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):]); if err != nil { t.Fatal(err) }; defer remote.Close()
	for _, op := range ops { got, err := remote.WorkspaceChatDraft(context.Background(), serverapi.WorkspaceChatDraftRequest{Operation: op}); if err != nil || got.Message != "server" { t.Fatalf("%s=%+v err=%v", op.Kind, got, err) } }
}
