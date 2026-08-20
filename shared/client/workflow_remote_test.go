package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/protocol"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func TestRemoteWorkflowTaskSearchUsesDedicatedConnectionAndClosesIt(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErr := make(chan error, 1)
	dedicatedClosed := make(chan struct{})
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		connectionCount.Add(1)
		acceptRemoteHandshake(t, ws)
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodWorkflowTaskSearch {
			handlerErr <- fmt.Errorf("task search method = %q", req.Method)
			return
		}
		var request serverapi.TaskSearchRequest
		if err := json.Unmarshal(req.Params, &request); err != nil {
			handlerErr <- fmt.Errorf("decode task search request: %w", err)
			return
		}
		if err := request.Validate(); err != nil {
			handlerErr <- fmt.Errorf("task search request validation: %w", err)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.TaskSearchResponse{
			Mode:   request.Mode,
			Groups: []serverapi.TaskSearchGroup{},
		})); err != nil {
			handlerErr <- fmt.Errorf("send task search response: %w", err)
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err == nil {
			handlerErr <- fmt.Errorf("unexpected request after task search: %q", req.Method)
			return
		}
		close(dedicatedClosed)
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	}
	response, err := remote.SearchWorkflowTasks(context.Background(), request)
	if err != nil {
		t.Fatalf("SearchWorkflowTasks: %v", err)
	}
	if err := response.Validate(); err != nil || response.Mode != request.Mode || len(response.Groups) != 0 {
		t.Fatalf("search response = %+v / %v", response, err)
	}
	select {
	case <-dedicatedClosed:
	case err := <-handlerErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for dedicated task-search connection close")
	}
	if got := connectionCount.Load(); got != 2 {
		t.Fatalf("connection count = %d, want control plus dedicated connections", got)
	}
	select {
	case err := <-handlerErr:
		t.Fatal(err)
	default:
	}
}
