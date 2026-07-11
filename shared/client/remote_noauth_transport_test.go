package client

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

func TestRemoteNoAuthAcknowledgementPropagatesToFreshConnectionStrategies(t *testing.T) {
	var connectionCount atomic.Int32
	var freshAckCount atomic.Int32
	handlerErrs := make(chan error, 16)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connIndex := connectionCount.Add(1)
		handshaken := false
		attached := false
		sawAck := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			if !handshaken {
				if req.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "conn %d first method = %q, want handshake", connIndex, req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				handshaken = true
				continue
			}
			switch req.Method {
			case protocol.MethodAuthAcknowledgeNoAuth:
				sawAck = true
				if connIndex > 1 {
					freshAckCount.Add(1)
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.AuthAcknowledgeNoAuthResponse{NoAuthSelected: true}))); err != nil {
					reportHandlerError(handlerErrs, "send no-auth ack response: %w", err)
				}
			case protocol.MethodAttachProject:
				if connIndex > 1 && !sawAck {
					reportHandlerError(handlerErrs, "conn %d attached project before no-auth acknowledgement", connIndex)
					return
				}
				attached = true
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1", WorkspaceRoot: "/tmp/workspace-a"}))); err != nil {
					reportHandlerError(handlerErrs, "send attach response: %w", err)
					return
				}
			case protocol.MethodAttachSession:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "session", SessionID: "session-1"}))); err != nil {
					reportHandlerError(handlerErrs, "send attach-session response: %w", err)
					return
				}
			case protocol.MethodRuntimeSubmitUserTurn:
				if !attached {
					reportHandlerError(handlerErrs, "submit before project attach")
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{Message: "ok"}))); err != nil {
					reportHandlerError(handlerErrs, "send submit response: %w", err)
				}
				return
			case protocol.MethodSessionSubscribeActivity:
				if !attached {
					reportHandlerError(handlerErrs, "subscribe before project attach")
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{Stream: protocol.MethodSessionActivityEvent}))); err != nil {
					reportHandlerError(handlerErrs, "send subscribe response: %w", err)
				}
				return
			case protocol.MethodRunPrompt:
				if !attached {
					reportHandlerError(handlerErrs, "run.prompt before project attach")
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RunPromptResponse{}))); err != nil {
					reportHandlerError(handlerErrs, "send run.prompt response: %w", err)
				}
				return
			default:
				reportHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if err := remote.EnableNoAuthBootstrapAcknowledgement(context.Background()); err != nil {
		t.Fatalf("EnableNoAuthBootstrapAcknowledgement: %v", err)
	}
	if _, err := remote.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{ClientRequestID: "submit-1", SessionID: "session-1", Text: "hi"}); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	sub, err := remote.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	_ = sub.Close()
	if _, err := remote.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "run-1", Prompt: "hi"}, nil); err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if got := freshAckCount.Load(); got != 3 {
		t.Fatalf("fresh no-auth ack count = %d, want 3", got)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteNoAuthAcknowledgementDisabledWhenServerReportsRealAuthReady(t *testing.T) {
	var connectionCount atomic.Int32
	var ackCount atomic.Int32
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connIndex := connectionCount.Add(1)
		handshaken := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			if !handshaken {
				if req.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "conn %d first method = %q, want handshake", connIndex, req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				handshaken = true
				continue
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1", WorkspaceRoot: "/tmp/workspace-a"}))); err != nil {
					reportHandlerError(handlerErrs, "send attach response: %w", err)
					return
				}
			case protocol.MethodAuthAcknowledgeNoAuth:
				ackCount.Add(1)
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: true}))); err != nil {
					reportHandlerError(handlerErrs, "send ack response: %w", err)
					return
				}
			case protocol.MethodRuntimeSubmitUserTurn:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{Message: "ok"}))); err != nil {
					reportHandlerError(handlerErrs, "send submit response: %w", err)
				}
				return
			default:
				reportHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()
	remote.noAuthAck.Store(true)
	if _, err := remote.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{ClientRequestID: "submit-1", SessionID: "session-1", Text: "hi"}); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if got := ackCount.Load(); got != 1 {
		t.Fatalf("ack count = %d, want 1", got)
	}
	if remote.NoAuthBootstrapAcknowledgementEnabled() {
		t.Fatal("expected no-auth acknowledgement policy to be disabled after real auth ready response")
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteRealAuthCompletionDisablesNoAuthAcknowledgementPolicy(t *testing.T) {
	var connectionCount atomic.Int32
	var ackCount atomic.Int32
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connIndex := connectionCount.Add(1)
		handshaken := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			if !handshaken {
				if req.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "conn %d first method = %q, want handshake", connIndex, req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				handshaken = true
				continue
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1", WorkspaceRoot: "/tmp/workspace-a"}))); err != nil {
					reportHandlerError(handlerErrs, "send attach response: %w", err)
					return
				}
			case protocol.MethodAuthAcknowledgeNoAuth:
				ackCount.Add(1)
				if connIndex > 1 {
					reportHandlerError(handlerErrs, "fresh connection sent stale no-auth acknowledgement after real auth")
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.AuthAcknowledgeNoAuthResponse{NoAuthSelected: true}))); err != nil {
					reportHandlerError(handlerErrs, "send no-auth ack response: %w", err)
					return
				}
			case protocol.MethodAuthCompleteBootstrap:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.AuthCompleteBootstrapResponse{AuthReady: true, MethodType: "api_key"}))); err != nil {
					reportHandlerError(handlerErrs, "send complete response: %w", err)
					return
				}
			case protocol.MethodRuntimeSubmitUserTurn:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{Message: "ok"}))); err != nil {
					reportHandlerError(handlerErrs, "send submit response: %w", err)
				}
				return
			default:
				reportHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if err := remote.EnableNoAuthBootstrapAcknowledgement(context.Background()); err != nil {
		t.Fatalf("EnableNoAuthBootstrapAcknowledgement: %v", err)
	}
	if _, err := remote.CompleteAuthBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{Mode: serverapi.AuthBootstrapModeAPIKey, APIKey: "real-key"}); err != nil {
		t.Fatalf("CompleteAuthBootstrap: %v", err)
	}
	if remote.NoAuthBootstrapAcknowledgementEnabled() {
		t.Fatal("expected real auth completion to disable no-auth acknowledgement policy")
	}
	if _, err := remote.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{ClientRequestID: "submit-1", SessionID: "session-1", Text: "hi"}); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if got := ackCount.Load(); got != 1 {
		t.Fatalf("ack count = %d, want only initial enable acknowledgement", got)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteDoesNotAcknowledgeNoAuthWhenPolicyDisabled(t *testing.T) {
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		handshaken := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			if !handshaken {
				if req.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "first method = %q, want handshake", req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				handshaken = true
				continue
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1", WorkspaceRoot: "/tmp/workspace-a"}))); err != nil {
					reportHandlerError(handlerErrs, "send attach response: %w", err)
					return
				}
			case protocol.MethodAuthAcknowledgeNoAuth:
				reportHandlerError(handlerErrs, "unexpected no-auth acknowledgement while policy disabled")
				return
			case protocol.MethodRuntimeSubmitUserTurn:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{Message: "ok"}))); err != nil {
					reportHandlerError(handlerErrs, "send submit response: %w", err)
				}
				return
			default:
				reportHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if _, err := remote.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{ClientRequestID: "submit-1", SessionID: "session-1", Text: "hi"}); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
}
