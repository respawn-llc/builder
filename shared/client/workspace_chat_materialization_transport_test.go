package client

import (
	"context"
	"testing"

	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRemoteMaterializeWorkspaceChatUsesDedicatedEmptyRequest(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		method := sessionLaunchMethod("MaterializeWorkspaceChat")
		correlation := receiveRemoteDescriptorCall(t, ws, method, &emptypb.Empty{})
		success, err := protoapi.WorkspaceChatMaterializationToProto(
			serverapi.WorkspaceChatMaterializeResponse{SessionID: sessionID},
		)
		if err != nil {
			t.Errorf("encode materialization response: %v", err)
			return
		}
		sendRemoteDescriptorResult(t, ws, method, correlation, &sessionlaunchpb.MaterializeWorkspaceChatResult{
			Outcome: &sessionlaunchpb.MaterializeWorkspaceChatResult_Success{Success: success},
		})
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	response, err := remote.MaterializeWorkspaceChat(context.Background(), serverapi.WorkspaceChatMaterializeRequest{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	if response.SessionID != sessionID {
		t.Fatalf("Session identity = %q, want %q", response.SessionID.String(), sessionID.String())
	}
}
