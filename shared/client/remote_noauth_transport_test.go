package client

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"core/shared/textutil"
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
			kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{
				projectID: "project-1", workspaceID: "workspace-1", workspaceRoot: "/tmp/workspace-a",
			})
			if err != nil {
				reportHandlerError(handlerErrs, "conn %d setup: %v", connIndex, err)
				return
			}
			if handled {
				handshaken = handshaken || kind == remoteTestSetupHandshake
				if kind == remoteTestSetupProject {
					if connIndex > 1 && !sawAck {
						reportHandlerError(handlerErrs, "conn %d attached project before no-auth acknowledgement", connIndex)
						return
					}
					attached = true
				}
				continue
			}
			authKind, handled, err := handleRemoteTestAuthFrame(ctx, conn, event.Frame, remoteTestAuthResponse{
				acknowledge: &serverapi.AuthAcknowledgeNoAuthResponse{NoAuthSelected: true},
			})
			if err != nil {
				reportHandlerError(handlerErrs, "conn %d auth: %v", connIndex, err)
				return
			}
			if handled {
				if authKind != remoteTestAuthAcknowledge {
					reportHandlerError(handlerErrs, "conn %d sent unexpected auth operation", connIndex)
					return
				}
				sawAck = true
				if connIndex > 1 {
					freshAckCount.Add(1)
				}
				continue
			}
			req := event.Frame.Request()
			if !handshaken {
				reportHandlerError(handlerErrs, "conn %d sent application traffic before handshake", connIndex)
				return
			}
			switch req.Method {
			case protocol.MethodRuntimeSubmitUserTurn:
				if !attached {
					reportHandlerError(handlerErrs, "submit before project attach")
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{
					Message:    textutil.Value("ok"),
					ResultKind: clientui.UserTurnResultKindAssistantFinal,
				}))); err != nil {
					reportHandlerError(handlerErrs, "send submit response: %w", err)
				}
				return
			case protocol.MethodSessionSubscribeTranscript:
				if !attached {
					reportHandlerError(handlerErrs, "subscribe before project attach")
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{Stream: protocol.MethodSessionTranscriptEvent}))); err != nil {
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
	if _, err := remote.SubmitUserTurn(context.Background(), runtimeSubmitUserTurnRequestForTest("session-1", "hi")); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	sub, err := remote.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	_ = sub.Close()
	if _, err := remote.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "run-1", Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), Prompt: "hi"}, nil); err != nil {
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
			kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{
				projectID: "project-1", workspaceID: "workspace-1", workspaceRoot: "/tmp/workspace-a",
			})
			if err != nil {
				reportHandlerError(handlerErrs, "conn %d setup: %v", connIndex, err)
				return
			}
			if handled {
				handshaken = handshaken || kind == remoteTestSetupHandshake
				continue
			}
			authKind, handled, err := handleRemoteTestAuthFrame(ctx, conn, event.Frame, remoteTestAuthResponse{
				acknowledge: &serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: true},
			})
			if err != nil {
				reportHandlerError(handlerErrs, "conn %d auth: %v", connIndex, err)
				return
			}
			if handled {
				if authKind != remoteTestAuthAcknowledge {
					reportHandlerError(handlerErrs, "conn %d sent unexpected auth operation", connIndex)
					return
				}
				ackCount.Add(1)
				continue
			}
			req := event.Frame.Request()
			if !handshaken {
				reportHandlerError(handlerErrs, "conn %d sent application traffic before handshake", connIndex)
				return
			}
			switch req.Method {
			case protocol.MethodRuntimeSubmitUserTurn:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{
					Message:    textutil.Value("ok"),
					ResultKind: clientui.UserTurnResultKindAssistantFinal,
				}))); err != nil {
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
	if _, err := remote.SubmitUserTurn(context.Background(), runtimeSubmitUserTurnRequestForTest("session-1", "hi")); err != nil {
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
			kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{
				projectID: "project-1", workspaceID: "workspace-1", workspaceRoot: "/tmp/workspace-a",
			})
			if err != nil {
				reportHandlerError(handlerErrs, "conn %d setup: %v", connIndex, err)
				return
			}
			if handled {
				handshaken = handshaken || kind == remoteTestSetupHandshake
				continue
			}
			authKind, handled, err := handleRemoteTestAuthFrame(ctx, conn, event.Frame, remoteTestAuthResponse{
				acknowledge: &serverapi.AuthAcknowledgeNoAuthResponse{NoAuthSelected: true},
				complete:    &serverapi.AuthCompleteBootstrapResponse{AuthReady: true, MethodType: "api_key"},
			})
			if err != nil {
				reportHandlerError(handlerErrs, "conn %d auth: %v", connIndex, err)
				return
			}
			if handled {
				switch authKind {
				case remoteTestAuthAcknowledge:
					ackCount.Add(1)
					if connIndex > 1 {
						reportHandlerError(handlerErrs, "fresh connection sent stale no-auth acknowledgement after real auth")
						return
					}
				case remoteTestAuthComplete:
				}
				continue
			}
			req := event.Frame.Request()
			if !handshaken {
				reportHandlerError(handlerErrs, "conn %d sent application traffic before handshake", connIndex)
				return
			}
			switch req.Method {
			case protocol.MethodRuntimeSubmitUserTurn:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{
					Message:    textutil.Value("ok"),
					ResultKind: clientui.UserTurnResultKindAssistantFinal,
				}))); err != nil {
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
	if _, err := remote.SubmitUserTurn(context.Background(), runtimeSubmitUserTurnRequestForTest("session-1", "hi")); err != nil {
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
			kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{
				projectID: "project-1", workspaceID: "workspace-1", workspaceRoot: "/tmp/workspace-a",
			})
			if err != nil {
				reportHandlerError(handlerErrs, "setup: %v", err)
				return
			}
			if handled {
				handshaken = handshaken || kind == remoteTestSetupHandshake
				continue
			}
			req := event.Frame.Request()
			if !handshaken {
				reportHandlerError(handlerErrs, "application traffic before handshake")
				return
			}
			switch req.Method {
			case protocol.MethodRuntimeSubmitUserTurn:
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{
					Message:    textutil.Value("ok"),
					ResultKind: clientui.UserTurnResultKindAssistantFinal,
				}))); err != nil {
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
	if _, err := remote.SubmitUserTurn(context.Background(), runtimeSubmitUserTurnRequestForTest("session-1", "hi")); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
}
