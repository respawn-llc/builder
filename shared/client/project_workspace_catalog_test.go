package client

import (
	"context"
	"sync/atomic"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func TestRemoteProjectWorkspaceCatalogRejectsInvalidBoundaries(t *testing.T) {
	next := 999
	for name, response := range map[string]serverapi.ProjectWorkspaceListResponse{
		"project mismatch": {ProjectID: "other", Workspaces: []serverapi.ProjectWorkspaceCatalogRow{}},
		"continuation mismatch": {ProjectID: "project-1", Offset: 100, Workspaces: []serverapi.ProjectWorkspaceCatalogRow{
			{WorkspaceID: "workspace-1", DisplayName: "Workspace", RootPath: "/workspace"},
		}, NextOffset: &next},
	} {
		t.Run(name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				request := acceptRemoteHandshake(t, ws)
				if err := websocket.JSON.Receive(ws, &request); err != nil {
					t.Fatal(err)
				}
				if request.Method != protocol.MethodProjectWorkspaceList {
					t.Fatalf("method = %q", request.Method)
				}
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, response))
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = remote.Close() }()
			if _, err := remote.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{ProjectID: "project-1", Offset: 100, Limit: 10}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestRemoteProjectWorkspaceCatalogRejectsInvalidRequestBeforeTransport(t *testing.T) {
	var received atomic.Bool
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		request := acceptRemoteHandshake(t, ws)
		if websocket.JSON.Receive(ws, &request) == nil {
			received.Store(true)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatal(err)
	}
	_, err = remote.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{ProjectID: "project-1"})
	_ = remote.Close()
	if err == nil || received.Load() {
		t.Fatal("invalid request reached transport")
	}
}

func TestRemoteGetsTypedExactProjectWorkspaceResult(t *testing.T) {
	selector, err := serverapi.NewProjectWorkspaceSelectorForID("workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		request := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Fatal(err)
		}
		if request.Method != protocol.MethodProjectWorkspaceGet {
			t.Fatalf("method = %q", request.Method)
		}
		_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, serverapi.ProjectWorkspaceGetResponse{
			ProjectID: "project-1", Result: serverapi.ProjectWorkspaceGetResultNotAttached,
		}))
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remote.Close() }()
	response, err := remote.GetProjectWorkspace(context.Background(), serverapi.ProjectWorkspaceGetRequest{
		ProjectID: "project-1", ProjectWorkspaceSelector: selector,
	})
	if err != nil || response.Result != serverapi.ProjectWorkspaceGetResultNotAttached || response.Workspace != nil {
		t.Fatalf("response = %+v, error = %v", response, err)
	}
}
