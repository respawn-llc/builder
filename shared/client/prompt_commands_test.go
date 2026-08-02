package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"

	"golang.org/x/net/websocket"
)

func TestRemotePromptCommandCatalogUsesAttachedWorkspaceAndValidatesResponse(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive attach request: %v", err)
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("first post-handshake method = %q, want attach-project", req.Method)
		}
		var attach protocol.AttachProjectRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach request: %v", err)
		}
		response, err := protocol.ProjectAttachResponseForRequest(attach, "workspace-b", "/workspace-b")
		if err != nil {
			t.Fatalf("attach response: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response)); err != nil {
			t.Fatalf("send attach response: %v", err)
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive catalog request: %v", err)
		}
		if req.Method != protocol.MethodPromptCommandCatalogGet {
			t.Fatalf("catalog method = %q, want %q", req.Method, protocol.MethodPromptCommandCatalogGet)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.PromptCommandCatalogResponse{
			Commands: []serverapi.PromptCommandCatalogEntry{{Name: "prompt:remote_demo", Preview: "remote"}},
		})); err != nil {
			t.Fatalf("send catalog response: %v", err)
		}
	})

	remote, err := DialRemoteURLForProjectWorkspace(context.Background(), "ws"+server.URL[len("http"):], "project-1", "/workspace-b")
	if err != nil {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()
	catalog, err := remote.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("GetPromptCommandCatalog: %v", err)
	}
	if len(catalog.Commands) != 1 || catalog.Commands[0].Name != "prompt:remote_demo" {
		t.Fatalf("catalog = %+v", catalog.Commands)
	}
}

func TestRemotePromptCommandErrorRoundTripsTypedKind(t *testing.T) {
	command := "prompt:stale"
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive catalog request: %v", err)
		}
		if req.Method != protocol.MethodPromptCommandCatalogGet {
			t.Fatalf("catalog method = %q", req.Method)
		}
		data := (&serverapi.PromptCommandError{
			Kind:    serverapi.PromptCommandErrorKindCommandNotFound,
			Command: &command,
		}).RPCErrorData()
		if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(req.ID, protocol.ErrCodePromptCommands, "stale", data)); err != nil {
			t.Fatalf("send typed error: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	var typed *serverapi.PromptCommandError
	_, err = remote.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T %v, want PromptCommandError", err, err)
	}
	if typed.Command == nil || *typed.Command != command {
		t.Fatalf("typed error = %+v", typed)
	}
}
