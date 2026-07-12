package client

import (
	"context"
	"fmt"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
	"net/http/httptest"
)

func TestRemoteWorkflowListRoute(t *testing.T) {
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive handshake: %w", err)
			return
		}
		if req.Method != protocol.MethodHandshake {
			handlerErr <- fmt.Errorf("handshake method = %q", req.Method)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			handlerErr <- fmt.Errorf("send handshake response: %w", err)
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive workflow list: %w", err)
			return
		}
		if req.Method != protocol.MethodWorkflowList {
			handlerErr <- fmt.Errorf("workflow list method = %q", req.Method)
			return
		}
		resp := serverapi.WorkflowListResponse{Workflows: []serverapi.WorkflowRecord{{ID: "workflow-1", Name: "Workflow"}}}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, resp)); err != nil {
			handlerErr <- fmt.Errorf("send workflow list response: %w", err)
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err == nil {
			handlerErr <- fmt.Errorf("unexpected request after workflow list: method = %q", req.Method)
			return
		}
		handlerErr <- nil
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	resp, err := remote.ListWorkflows(context.Background(), serverapi.WorkflowListRequest{})
	if err != nil {
		_ = remote.Close()
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(resp.Workflows) != 1 || resp.Workflows[0].ID != "workflow-1" {
		_ = remote.Close()
		t.Fatalf("response = %+v", resp)
	}
	_ = remote.Close()
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowStartRejectsInvalidResponse(t *testing.T) {
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive handshake: %w", err)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			handlerErr <- fmt.Errorf("send handshake response: %w", err)
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive workflow start: %w", err)
			return
		}
		if req.Method != protocol.MethodWorkflowTaskStart {
			handlerErr <- fmt.Errorf("workflow start method = %q", req.Method)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowTaskStartResponse{})); err != nil {
			handlerErr <- fmt.Errorf("send workflow start response: %w", err)
			return
		}
		handlerErr <- nil
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if _, err := remote.StartWorkflowTask(context.Background(), serverapi.WorkflowTaskStartRequest{
		TaskID:           "task-1",
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}); err == nil {
		t.Fatal("StartWorkflowTask accepted an invalid response")
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowTaskDetailRejectsInvalidResponse(t *testing.T) {
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive handshake: %w", err)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			handlerErr <- fmt.Errorf("send handshake response: %w", err)
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive workflow task get: %w", err)
			return
		}
		if req.Method != protocol.MethodWorkflowTaskGet {
			handlerErr <- fmt.Errorf("workflow task get method = %q", req.Method)
			return
		}
		blankRoot := " "
		response := serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
			ExecutionTarget: &serverapi.WorkflowExecutionTarget{
				Mode:          serverapi.WorkflowExecutionTargetModeNone,
				EffectiveRoot: &blankRoot,
				Provenance:    serverapi.WorkflowExecutionTargetProvenanceResolved,
			},
		}}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response)); err != nil {
			handlerErr <- fmt.Errorf("send workflow task get response: %w", err)
			return
		}
		handlerErr <- nil
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if _, err := remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{TaskID: "task-1"}); err == nil {
		t.Fatal("GetWorkflowTask accepted an invalid response")
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}
