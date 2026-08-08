package client

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func TestRemoteAnswerPromptBatchValidatesExactResponseIdentitySet(t *testing.T) {
	tests := []struct {
		name     string
		response serverapi.PromptAnswerBatchResponse
		wantErr  bool
	}{
		{
			name: "reordered exact set",
			response: serverapi.PromptAnswerBatchResponse{Results: []serverapi.PromptAnswerBatchResult{
				{PromptID: "approval-1", Outcome: serverapi.PromptAnswerBatchOutcomeSkipped},
				{PromptID: "question-1", Outcome: serverapi.PromptAnswerBatchOutcomeResolved},
			}},
		},
		{
			name: "missing identity",
			response: serverapi.PromptAnswerBatchResponse{Results: []serverapi.PromptAnswerBatchResult{
				{PromptID: "question-1", Outcome: serverapi.PromptAnswerBatchOutcomeResolved},
			}},
			wantErr: true,
		},
		{
			name: "foreign identity",
			response: serverapi.PromptAnswerBatchResponse{Results: []serverapi.PromptAnswerBatchResult{
				{PromptID: "question-1", Outcome: serverapi.PromptAnswerBatchOutcomeResolved},
				{PromptID: "foreign", Outcome: serverapi.PromptAnswerBatchOutcomeSkipped},
			}},
			wantErr: true,
		},
		{
			name: "duplicate identity",
			response: serverapi.PromptAnswerBatchResponse{Results: []serverapi.PromptAnswerBatchResult{
				{PromptID: "question-1", Outcome: serverapi.PromptAnswerBatchOutcomeResolved},
				{PromptID: "question-1", Outcome: serverapi.PromptAnswerBatchOutcomeSkipped},
			}},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				request := acceptRemoteHandshake(t, ws)
				if err := websocket.JSON.Receive(ws, &request); err != nil {
					t.Errorf("receive prompt answer batch: %v", err)
					return
				}
				if request.Method != protocol.MethodPromptAnswerBatch {
					t.Errorf("method = %q, want %q", request.Method, protocol.MethodPromptAnswerBatch)
					return
				}
				var params serverapi.PromptAnswerBatchRequest
				if err := json.Unmarshal(request.Params, &params); err != nil {
					t.Errorf("decode prompt answer batch: %v", err)
					return
				}
				if err := params.Validate(); err != nil {
					t.Errorf("validate prompt answer batch: %v", err)
					return
				}
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, test.response)); err != nil {
					t.Errorf("send prompt answer batch response: %v", err)
				}
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()

			response, err := remote.AnswerPromptBatch(context.Background(), remotePromptAnswerBatchRequest(t))
			if test.wantErr {
				if err == nil {
					t.Fatalf("AnswerPromptBatch response = %+v, want contract error", response)
				}
				return
			}
			if err != nil {
				t.Fatalf("AnswerPromptBatch: %v", err)
			}
			if err := serverapi.ValidatePromptAnswerBatchResponse(remotePromptAnswerBatchRequest(t), response); err != nil {
				t.Fatalf("validated response: %v", err)
			}
		})
	}
}

func TestRemoteAnswerPromptBatchDoesNotReconnectOrReplayAfterConnectionLoss(t *testing.T) {
	var connectionCount atomic.Int32
	var requestCount atomic.Int32
	firstRequestCommitted := make(chan struct{}, 1)
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connectionIndex := connectionCount.Add(1)
		handshaken := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			request := event.Frame.Request()
			if !handshaken {
				if request.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "connection %d first method = %q, want handshake", connectionIndex, request.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(request.ID, protocol.HandshakeResponse{
					Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
				}))); err != nil {
					reportHandlerError(handlerErrs, "connection %d send handshake: %v", connectionIndex, err)
					return
				}
				handshaken = true
				continue
			}
			if request.Method != protocol.MethodPromptAnswerBatch {
				reportHandlerError(handlerErrs, "connection %d method = %q, want prompt answer batch", connectionIndex, request.Method)
				return
			}
			var params serverapi.PromptAnswerBatchRequest
			if err := json.Unmarshal(request.Params, &params); err != nil {
				reportHandlerError(handlerErrs, "connection %d decode prompt answer batch: %v", connectionIndex, err)
				return
			}
			if err := params.Validate(); err != nil {
				reportHandlerError(handlerErrs, "connection %d validate prompt answer batch: %v", connectionIndex, err)
				return
			}
			requestCount.Add(1)
			if connectionIndex == 1 {
				firstRequestCommitted <- struct{}{}
				return
			}
			response := serverapi.PromptAnswerBatchResponse{
				Results: make([]serverapi.PromptAnswerBatchResult, 0, len(params.Entries)),
			}
			for _, entry := range params.Entries {
				response.Results = append(response.Results, serverapi.PromptAnswerBatchResult{
					PromptID: entry.PromptID,
					Outcome:  serverapi.PromptAnswerBatchOutcomeSkipped,
				})
			}
			if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(request.ID, response))); err != nil {
				reportHandlerError(handlerErrs, "connection %d send prompt answer batch response: %v", connectionIndex, err)
			}
			return
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	request := remotePromptAnswerBatchRequest(t)
	firstDone := make(chan error, 1)
	go func() {
		_, callErr := remote.AnswerPromptBatch(context.Background(), request)
		firstDone <- callErr
	}()
	select {
	case <-firstRequestCommitted:
	case err := <-handlerErrs:
		t.Fatal(err)
	}
	if err := <-firstDone; err == nil {
		t.Fatal("connection-lost batch unexpectedly succeeded")
	}
	if got := connectionCount.Load(); got != 1 {
		t.Fatalf("connections after in-flight failure = %d, want 1", got)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("requests after in-flight failure = %d, want 1", got)
	}

	response, err := remote.AnswerPromptBatch(context.Background(), request)
	if err != nil {
		t.Fatalf("explicit batch after connection loss: %v", err)
	}
	if got := connectionCount.Load(); got != 2 {
		t.Fatalf("connections after explicit retry = %d, want 2", got)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("requests after explicit retry = %d, want 2", got)
	}
	for _, result := range response.Results {
		if result.Outcome != serverapi.PromptAnswerBatchOutcomeSkipped {
			t.Fatalf("explicit all-stale response = %+v", response)
		}
	}
	requireNoHandlerError(t, handlerErrs)
}

func remotePromptAnswerBatchRequest(t *testing.T) serverapi.PromptAnswerBatchRequest {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	selected := 1
	return serverapi.PromptAnswerBatchRequest{
		SessionID: sessionID,
		StepID:    stepID,
		Entries: []serverapi.PromptAnswerBatchEntry{
			{
				PromptID:       clientui.PromptID("question-1"),
				QuestionAnswer: &serverapi.PromptQuestionAnswer{SelectedOptionNumber: &selected},
			},
			{
				PromptID: clientui.PromptID("approval-1"),
				Declined: &serverapi.PromptDeclined{},
			},
		},
	}
}
