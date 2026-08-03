package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/protocol"
	"core/shared/runtimeids"
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
		resp := serverapi.WorkflowListResponse{Workflows: []serverapi.WorkflowRecord{{ID: runtimeids.NewWorkflowID(), Name: "Workflow"}}}
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
	if len(resp.Workflows) != 1 || resp.Workflows[0].ID.IsZero() {
		_ = remote.Close()
		t.Fatalf("response = %+v", resp)
	}
	_ = remote.Close()
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowProjectLabelCreateAndListRoutes(t *testing.T) {
	handlerErr := make(chan error, 1)
	labelID := "11111111-1111-4111-8111-111111111111"
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
			handlerErr <- fmt.Errorf("receive project label create: %w", err)
			return
		}
		if req.Method != protocol.MethodWorkflowProjectLabelCreate {
			handlerErr <- fmt.Errorf("project label create method = %q", req.Method)
			return
		}
		var createRequest serverapi.WorkflowProjectLabelCreateRequest
		if err := json.Unmarshal(req.Params, &createRequest); err != nil {
			handlerErr <- fmt.Errorf("decode project label create: %w", err)
			return
		}
		if createRequest.ProjectID != "project-1" || createRequest.Name != "Priority" {
			handlerErr <- fmt.Errorf("project label create request = %+v", createRequest)
			return
		}
		label := serverapi.WorkflowProjectLabel{ID: labelID, Name: "Priority"}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowProjectLabelCreateResponse{Label: label})); err != nil {
			handlerErr <- fmt.Errorf("send project label create response: %w", err)
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive project label list: %w", err)
			return
		}
		if req.Method != protocol.MethodWorkflowProjectLabelList {
			handlerErr <- fmt.Errorf("project label list method = %q", req.Method)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowProjectLabelCatalogResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: "project-1",
				Labels:    []serverapi.WorkflowProjectLabel{label},
			},
		})); err != nil {
			handlerErr <- fmt.Errorf("send project label list response: %w", err)
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
	created, err := remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: "project-1",
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	listed, err := remote.ListWorkflowProjectLabels(context.Background(), serverapi.WorkflowProjectLabelCatalogRequest{ProjectID: "project-1"})
	if err != nil {
		t.Fatalf("ListWorkflowProjectLabels: %v", err)
	}
	if created.Label.ID != labelID || !reflect.DeepEqual(listed.Catalog.Labels, []serverapi.WorkflowProjectLabel{created.Label}) {
		t.Fatalf("created/listed = %+v / %+v", created, listed)
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowProjectLabelAndTaskAssignmentMutationRoutes(t *testing.T) {
	handlerErr := make(chan error, 1)
	labelID := "11111111-1111-4111-8111-111111111111"
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
		if err := expectWorkflowRemoteMethod(ws, &req, protocol.MethodWorkflowProjectLabelRename); err != nil {
			handlerErr <- err
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowProjectLabelRenameResponse{
			Label: serverapi.WorkflowProjectLabel{ID: labelID, Name: "Urgent"},
		})); err != nil {
			handlerErr <- fmt.Errorf("send project label rename response: %w", err)
			return
		}
		if err := expectWorkflowRemoteMethod(ws, &req, protocol.MethodWorkflowTaskLabelsGet); err != nil {
			handlerErr <- err
			return
		}
		assignment := serverapi.WorkflowTaskAssignedLabelIDs{TaskID: "task-1", LabelIDs: []string{labelID}}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowTaskLabelsGetResponse{Assignment: assignment})); err != nil {
			handlerErr <- fmt.Errorf("send task labels get response: %w", err)
			return
		}
		if err := expectWorkflowRemoteMethod(ws, &req, protocol.MethodWorkflowTaskLabelsUpdate); err != nil {
			handlerErr <- err
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowTaskLabelsUpdateResponse{Assignment: assignment})); err != nil {
			handlerErr <- fmt.Errorf("send task labels update response: %w", err)
			return
		}
		if err := expectWorkflowRemoteMethod(ws, &req, protocol.MethodWorkflowProjectLabelDelete); err != nil {
			handlerErr <- err
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowProjectLabelDeleteResponse{LabelID: labelID})); err != nil {
			handlerErr <- fmt.Errorf("send project label delete response: %w", err)
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
	renamed, err := remote.RenameWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelRenameRequest{
		ProjectID: "project-1",
		LabelID:   labelID,
		Name:      "Urgent",
	})
	if err != nil {
		t.Fatalf("RenameWorkflowProjectLabel: %v", err)
	}
	assignment, err := remote.GetWorkflowTaskLabels(context.Background(), serverapi.WorkflowTaskLabelsGetRequest{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("GetWorkflowTaskLabels: %v", err)
	}
	updated, err := remote.UpdateWorkflowTaskLabels(context.Background(), serverapi.WorkflowTaskLabelsUpdateRequest{
		TaskID:      "task-1",
		AddLabelIDs: []string{labelID},
	})
	if err != nil {
		t.Fatalf("UpdateWorkflowTaskLabels: %v", err)
	}
	deleted, err := remote.DeleteWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelDeleteRequest{
		ProjectID: "project-1",
		LabelID:   labelID,
	})
	if err != nil {
		t.Fatalf("DeleteWorkflowProjectLabel: %v", err)
	}
	if renamed.Label.Name != "Urgent" ||
		!reflect.DeepEqual(assignment.Assignment.LabelIDs, []string{labelID}) ||
		!reflect.DeepEqual(updated.Assignment.LabelIDs, []string{labelID}) ||
		deleted.LabelID != labelID {
		t.Fatalf("responses = %+v %+v %+v %+v", renamed, assignment, updated, deleted)
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowProjectLabelRoutePreservesTypedError(t *testing.T) {
	handlerErr := make(chan error, 1)
	projectID := "project-1"
	source := &serverapi.WorkflowLabelError{
		Reason:    serverapi.WorkflowLabelErrorReasonNameConflict,
		ProjectID: &projectID,
	}
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
		if err := expectWorkflowRemoteMethod(ws, &req, protocol.MethodWorkflowProjectLabelCreate); err != nil {
			handlerErr <- err
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(
			req.ID,
			source.RPCErrorCode(),
			source.Error(),
			source.RPCErrorData(),
		)); err != nil {
			handlerErr <- fmt.Errorf("send workflow label error: %w", err)
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
	_, err = remote.CreateWorkflowProjectLabel(context.Background(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: "project-1",
		Name:      "Priority",
	})
	var decoded *serverapi.WorkflowLabelError
	if !errors.As(err, &decoded) || !reflect.DeepEqual(decoded, source) {
		t.Fatalf("error = %T %v, want %+v", err, err, source)
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowTaskCreateCarriesLabelIDs(t *testing.T) {
	handlerErr := make(chan error, 1)
	labelID := "11111111-1111-4111-8111-111111111111"
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
		if err := expectWorkflowRemoteMethod(ws, &req, protocol.MethodWorkflowTaskCreate); err != nil {
			handlerErr <- err
			return
		}
		var createRequest serverapi.WorkflowTaskCreateRequest
		if err := json.Unmarshal(req.Params, &createRequest); err != nil {
			handlerErr <- fmt.Errorf("decode workflow task create: %w", err)
			return
		}
		if !reflect.DeepEqual(createRequest.LabelIDs, []string{labelID}) {
			handlerErr <- fmt.Errorf("workflow task create label IDs = %+v", createRequest.LabelIDs)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowTaskCreateResponse{
			Task: serverapi.WorkflowTaskSummary{ID: "task-1", WorkflowID: runtimeids.NewWorkflowID()},
		})); err != nil {
			handlerErr <- fmt.Errorf("send workflow task create response: %w", err)
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
	created, err := remote.CreateWorkflowTask(context.Background(), serverapi.WorkflowTaskCreateRequest{
		ProjectID:  "project-1",
		WorkflowID: func() *runtimeids.WorkflowID { value := runtimeids.NewWorkflowID(); return &value }(),
		Title:      "Labeled task",
		LabelIDs:   []string{labelID},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	if created.Task.ID != "task-1" {
		t.Fatalf("created task = %+v", created.Task)
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func expectWorkflowRemoteMethod(ws *websocket.Conn, req *protocol.Request, method string) error {
	if err := websocket.JSON.Receive(ws, req); err != nil {
		return fmt.Errorf("receive %s: %w", method, err)
	}
	if req.Method != method {
		return fmt.Errorf("method = %q, want %q", req.Method, method)
	}
	return nil
}

func TestRemoteWorkflowTaskListRoundTripsTypedScope(t *testing.T) {
	handlerErr := make(chan error, 1)
	projectID := "project-1"
	workflowID := runtimeids.NewWorkflowID()
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
			handlerErr <- fmt.Errorf("receive task list: %w", err)
			return
		}
		if req.Method != protocol.MethodWorkflowTaskList {
			handlerErr <- fmt.Errorf("task list method = %q", req.Method)
			return
		}
		response := serverapi.WorkflowTaskListResponse{
			Scope:                       serverapi.WorkflowTaskListScope{ProjectID: projectID, WorkflowID: &workflowID},
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
			Tasks:                       []serverapi.WorkflowTaskListItem{{TaskID: "task-1", WorkflowID: workflowID}},
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response)); err != nil {
			handlerErr <- fmt.Errorf("send task list response: %w", err)
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
	response, err := remote.ListWorkflowTasks(context.Background(), serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
	})
	if err != nil {
		t.Fatalf("ListWorkflowTasks: %v", err)
	}
	if response.Scope.ProjectID != projectID || response.Scope.WorkflowID == nil || *response.Scope.WorkflowID != workflowID || response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne {
		t.Fatalf("response scope = %+v, want typed project/workflow scope", response)
	}
	if len(response.Tasks) != 1 || response.Tasks[0].WorkflowID != workflowID {
		t.Fatalf("response tasks = %+v, want one workflow task", response.Tasks)
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowTaskSearchUsesDedicatedConnectionAndClosesIt(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErr := make(chan error, 1)
	dedicatedClosed := make(chan struct{})
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		connectionIndex := connectionCount.Add(1)
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
		if connectionIndex == 1 {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive task search: %w", err)
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

func TestRemoteWorkflowTaskSearchRejectsInvalidResponse(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		connectionIndex := connectionCount.Add(1)
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive handshake: %w", err)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{
			Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
		})); err != nil {
			handlerErr <- fmt.Errorf("send handshake response: %w", err)
			return
		}
		if connectionIndex == 1 {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			handlerErr <- fmt.Errorf("receive task search: %w", err)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.TaskSearchResponse{
			Mode: serverapi.TaskSearchModeLiteral,
		})); err != nil {
			handlerErr <- fmt.Errorf("send invalid task search response: %w", err)
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_, err = remote.SearchWorkflowTasks(context.Background(), serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err == nil {
		t.Fatal("invalid task search response was accepted")
	}
	select {
	case handlerErr := <-handlerErr:
		t.Fatal(handlerErr)
	default:
	}
}

func TestRemoteWorkflowAttentionListRoundTripsGlobalRequest(t *testing.T) {
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
			handlerErr <- fmt.Errorf("receive workflow attention list: %w", err)
			return
		}
		if req.Method != protocol.MethodWorkflowAttentionList {
			handlerErr <- fmt.Errorf("workflow attention list method = %q", req.Method)
			return
		}
		var params map[string]any
		if err := json.Unmarshal(req.Params, &params); err != nil {
			handlerErr <- fmt.Errorf("decode global attention params: %w", err)
			return
		}
		if _, present := params["project_id"]; present {
			handlerErr <- fmt.Errorf("global attention request includes project_id: %#v", params)
			return
		}
		if params["page_size"] != float64(25) || params["page_token"] != "cursor-1" {
			handlerErr <- fmt.Errorf("global attention params = %#v", params)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorkflowAttentionListResponse{
			Items:             []serverapi.WorkflowAttentionItem{},
			GeneratedAtUnixMs: 1,
		})); err != nil {
			handlerErr <- fmt.Errorf("send workflow attention response: %w", err)
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
	response, err := remote.ListWorkflowAttention(context.Background(), serverapi.WorkflowAttentionListRequest{PageSize: 25, PageToken: "cursor-1"})
	if err != nil {
		t.Fatalf("ListWorkflowAttention: %v", err)
	}
	if response.GeneratedAtUnixMs != 1 || len(response.Items) != 0 {
		t.Fatalf("global attention response = %+v", response)
	}
	if err := <-handlerErr; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWorkflowAttentionAndActivityRejectMalformedResponses(t *testing.T) {
	t.Run("removed global attention kind", func(t *testing.T) {
		remote := newWorkflowResponseRemote(t, protocol.MethodWorkflowAttentionList, serverapi.WorkflowAttentionListResponse{
			Items: []serverapi.WorkflowAttentionItem{{Kind: "validation_blocker"}},
		})
		if _, err := remote.ListWorkflowAttention(context.Background(), serverapi.WorkflowAttentionListRequest{}); err == nil {
			t.Fatal("ListWorkflowAttention accepted a removed attention kind")
		}
	})

	t.Run("task attention task mismatch", func(t *testing.T) {
		remote := newWorkflowResponseRemote(t, protocol.MethodWorkflowTaskAttentionList, serverapi.WorkflowTaskAttentionListResponse{
			Items: []serverapi.WorkflowAttentionItem{*workflowRemoteInterruptedAttention("task-other")},
		})
		if _, err := remote.ListWorkflowTaskAttention(context.Background(), serverapi.WorkflowTaskAttentionListRequest{TaskID: "task-requested"}); err == nil {
			t.Fatal("ListWorkflowTaskAttention accepted an item for another task")
		}
	})

	t.Run("global attention approval session field", func(t *testing.T) {
		sessionID := "session-1"
		item := workflowRemoteApprovalAttention()
		item.SessionID = &sessionID
		remote := newWorkflowResponseRemote(t, protocol.MethodWorkflowAttentionList, serverapi.WorkflowAttentionListResponse{
			Items: []serverapi.WorkflowAttentionItem{item},
		})
		if _, err := remote.ListWorkflowAttention(context.Background(), serverapi.WorkflowAttentionListRequest{}); err == nil {
			t.Fatal("ListWorkflowAttention accepted an approval carrying session_id")
		}
	})

	t.Run("global attention malformed approval snapshot", func(t *testing.T) {
		item := workflowRemoteApprovalAttention()
		item.ApprovalSnapshot = &serverapi.WorkflowAttentionApprovalSnapshot{}
		remote := newWorkflowResponseRemote(t, protocol.MethodWorkflowAttentionList, serverapi.WorkflowAttentionListResponse{
			Items: []serverapi.WorkflowAttentionItem{item},
		})
		if _, err := remote.ListWorkflowAttention(context.Background(), serverapi.WorkflowAttentionListRequest{}); err == nil {
			t.Fatal("ListWorkflowAttention accepted an approval with a malformed snapshot")
		}
	})

	t.Run("activity unsupported type", func(t *testing.T) {
		remote := newWorkflowResponseRemote(t, protocol.MethodWorkflowTaskActivityList, serverapi.WorkflowTaskActivityListResponse{
			Items: []serverapi.WorkflowTaskActivityItem{{
				Type:   "unsupported",
				TaskID: "task-requested",
			}},
		})
		if _, err := remote.ListWorkflowTaskActivity(context.Background(), serverapi.WorkflowTaskActivityListRequest{TaskID: "task-requested"}); err == nil {
			t.Fatal("ListWorkflowTaskActivity accepted an unsupported type")
		}
	})
}

func newWorkflowResponseRemote(t *testing.T, wantMethod string, response any) *Remote {
	t.Helper()
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
			handlerErr <- fmt.Errorf("receive workflow response request: %w", err)
			return
		}
		if req.Method != wantMethod {
			handlerErr <- fmt.Errorf("workflow response request method = %q, want %q", req.Method, wantMethod)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response)); err != nil {
			handlerErr <- fmt.Errorf("send workflow response: %w", err)
			return
		}
		handlerErr <- nil
	}))
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		server.Close()
		t.Fatalf("DialRemoteURL: %v", err)
	}
	t.Cleanup(func() {
		_ = remote.Close()
		server.Close()
		if err := <-handlerErr; err != nil {
			t.Error(err)
		}
	})
	return remote
}

func workflowRemoteInterruptedAttention(taskID string) *serverapi.WorkflowAttentionItem {
	return &serverapi.WorkflowAttentionItem{
		ProjectID:   "project-1",
		Kind:        "interrupted_current_node",
		TaskID:      taskID,
		TaskShortID: "KENT-1",
		TaskTitle:   "Task",
		WorkflowID:  runtimeids.NewWorkflowID(),
		CurrentNode: &serverapi.WorkflowTaskCurrentNode{NodeID: "review"},
	}
}

func workflowRemoteApprovalAttention() serverapi.WorkflowAttentionItem {
	return serverapi.WorkflowAttentionItem{
		ProjectID:   "project-1",
		Kind:        "approval",
		TaskID:      "task-requested",
		TaskShortID: "KENT-1",
		TaskTitle:   "Task",
		WorkflowID:  runtimeids.NewWorkflowID(),
		ApprovalID:  workflowRemoteString("approval-1"),
		ApprovalSnapshot: &serverapi.WorkflowAttentionApprovalSnapshot{
			SourceNodeDisplayName: "Review",
			Targets:               []serverapi.WorkflowAttentionApprovalTarget{{DisplayName: "Done"}},
			OutputValues:          map[string]string{},
			WorkflowRevisionSeen:  1,
		},
	}
}

func workflowRemoteString(value string) *string {
	return &value
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
		response := serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
			ExecutionTarget: &serverapi.WorkflowExecutionTarget{
				Mode:       serverapi.WorkflowExecutionTargetModeNone,
				Provenance: serverapi.WorkflowExecutionTargetProvenanceLegacyObserved,
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
