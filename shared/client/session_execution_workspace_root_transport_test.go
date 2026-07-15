package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"golang.org/x/net/websocket"
)

func TestRemoteSessionExecutionWorkspaceRootRoundTrip(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("target-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	requests := make(chan serverapi.SessionExecutionWorkspaceRootRequest, 1)
	remote := dialSessionExecutionWorkspaceRootRemote(
		t,
		serverapi.SessionExecutionWorkspaceRootResponse{WorkspaceRoot: "/workspace"},
		requests,
	)

	response, err := remote.GetSessionExecutionWorkspaceRoot(
		context.Background(),
		serverapi.SessionExecutionWorkspaceRootRequest{SessionID: sessionID},
	)
	if err != nil {
		t.Fatalf("GetSessionExecutionWorkspaceRoot: %v", err)
	}
	if response.WorkspaceRoot != "/workspace" {
		t.Fatalf("workspace root = %q, want %q", response.WorkspaceRoot, "/workspace")
	}
	request := <-requests
	if request.SessionID != sessionID {
		t.Fatalf("session ID = %q, want %q", request.SessionID.String(), sessionID.String())
	}
}

func TestRemoteSessionExecutionWorkspaceRootRejectsMissingRoot(t *testing.T) {
	remote := dialSessionExecutionWorkspaceRootRemote(t, struct{}{}, nil)
	sessionID, err := runtimeids.ParseSessionID("target-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := remote.GetSessionExecutionWorkspaceRoot(
		context.Background(),
		serverapi.SessionExecutionWorkspaceRootRequest{SessionID: sessionID},
	); err == nil {
		t.Fatal("GetSessionExecutionWorkspaceRoot error = nil")
	}
}

func dialSessionExecutionWorkspaceRootRemote(
	t *testing.T,
	result any,
	requests chan<- serverapi.SessionExecutionWorkspaceRootRequest,
) *Remote {
	t.Helper()
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		for {
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				t.Fatalf("receive execution workspace root request: %v", err)
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{
					Kind:      "project",
					ProjectID: "project-1",
				})); err != nil {
					t.Fatalf("send attach response: %v", err)
				}
			case protocol.MethodSessionGetExecutionWorkspaceRoot:
				var request serverapi.SessionExecutionWorkspaceRootRequest
				if err := json.Unmarshal(req.Params, &request); err != nil {
					t.Fatalf("decode execution workspace root request: %v", err)
				}
				if requests != nil {
					requests <- request
				}
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, result)); err != nil {
					t.Fatalf("send execution workspace root response: %v", err)
				}
			default:
				t.Fatalf("unexpected method %q", req.Method)
			}
		}
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	return remote
}
