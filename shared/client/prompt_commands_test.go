package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/onboarding"
	"core/server/promptcommands"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"

	"github.com/google/uuid"
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
	foundRemoteCommand := false
	for _, command := range catalog.Commands {
		if command.Name == "prompt:remote_demo" && command.Preview == "remote" {
			foundRemoteCommand = true
			break
		}
	}
	if !foundRemoteCommand {
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

func TestRemotePromptCommandImportCatalogAndInvocationUseServerRoots(t *testing.T) {
	serverRoot := t.TempDir()
	serverWorkspace := t.TempDir()
	clientRoot := t.TempDir()
	home := t.TempDir()
	sourceRoot := filepath.Join(home, ".claude", "commands")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "remote_demo.md"), []byte("server body $ARGUMENTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	var providerUUID uuid.UUID
	for _, provider := range onboarding.ProductionProviderCatalog() {
		if provider.HomeEntry == ".claude" {
			providerUUID = provider.UUID
			break
		}
	}
	if providerUUID == uuid.Nil {
		t.Fatal("Claude Code provider UUID is missing")
	}
	finalizer, err := onboarding.NewFinalizer(onboarding.Options{
		PersistenceRoot: serverRoot,
		WorkspaceRoot:   serverWorkspace,
		HomeDir:         home,
	})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	if _, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		CommandsImport: &serverapi.OnboardingImportSelection{
			Mode:         serverapi.OnboardingImportModeSymlinkSource,
			ProviderUUID: &providerUUID,
		},
	}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	service := promptcommands.New(serverRoot, serverWorkspace)
	resolvedContent := make(chan string, 1)
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		for {
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				return
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				var attach protocol.AttachProjectRequest
				if err := json.Unmarshal(req.Params, &attach); err != nil {
					t.Errorf("decode attach request: %v", err)
					return
				}
				selector, present := attach.Workspace()
				if !present {
					t.Error("attach request omitted workspace selector")
					return
				}
				attachedRoot, present := selector.WorkspaceRoot()
				if !present || attachedRoot != clientRoot {
					t.Errorf("client workspace root = %q, want %q", attachedRoot, clientRoot)
					return
				}
				attachResponse, err := protocol.ProjectAttachResponseForRequest(attach, "workspace-server", serverWorkspace)
				if err != nil {
					t.Errorf("attach response: %v", err)
					return
				}
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, attachResponse)); err != nil {
					t.Errorf("send attach response: %v", err)
					return
				}
			case protocol.MethodPromptCommandCatalogGet:
				entries, err := service.Catalog()
				if err != nil {
					t.Errorf("Catalog: %v", err)
					return
				}
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.PromptCommandCatalogResponse{Commands: entries})); err != nil {
					t.Errorf("send catalog response: %v", err)
					return
				}
			case protocol.MethodRuntimeSubmitUserTurn:
				var submit serverapi.RuntimeSubmitUserTurnRequest
				if err := json.Unmarshal(req.Params, &submit); err != nil {
					t.Errorf("decode submit request: %v", err)
					return
				}
				if submit.Input.PromptCommand == nil {
					t.Errorf("submit input = %+v, want typed prompt command", submit.Input)
					return
				}
				content, err := service.Resolve(submit.Input.PromptCommand.Name, submit.Input.PromptCommand.Arguments)
				if err != nil {
					t.Errorf("Resolve: %v", err)
					return
				}
				resolvedContent <- content
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{
					Message:    textutil.Value("accepted"),
					ResultKind: clientui.UserTurnResultKindAssistantFinal,
				})); err != nil {
					t.Errorf("send submit response: %v", err)
				}
				return
			}
		}
	})
	remote, err := DialRemoteURLForProjectWorkspace(context.Background(), "ws"+server.URL[len("http"):], "project-1", clientRoot)
	if err != nil {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()
	catalog, err := remote.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("GetPromptCommandCatalog: %v", err)
	}
	foundRemoteCommand := false
	for _, command := range catalog.Commands {
		if command.Name == "prompt:remote_demo" && command.Preview == "server body $ARGUMENTS" {
			foundRemoteCommand = true
			break
		}
	}
	if !foundRemoteCommand {
		t.Fatalf("catalog = %+v", catalog.Commands)
	}
	_, err = remote.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: runtimeids.NewSessionID().String(),
		Input:     runtimeinput.Command("prompt:remote_demo", "hello world"),
	})
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if got := <-resolvedContent; got != "server body hello world" {
		t.Fatalf("resolved content = %q", got)
	}
}
