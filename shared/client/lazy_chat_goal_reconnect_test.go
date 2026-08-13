package client

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	servertransport "core/server/transport"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type reconnectCountingTransport struct {
	base             rpcwire.WebSocketTransport
	materializations atomic.Int32
	goalSets         atomic.Int32
	first            *reconnectCountingConn
	firstOnce        sync.Once
}

func (t *reconnectCountingTransport) Dial(ctx context.Context, endpoint rpcwire.Endpoint) (rpcwire.Conn, error) {
	conn, err := t.base.Dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	wrapped := newReconnectCountingConn(conn, &t.materializations, &t.goalSets)
	t.firstOnce.Do(func() {
		t.first = wrapped
	})
	return wrapped, nil
}

type reconnectCountingConn struct {
	inner        rpcwire.Conn
	materializes *atomic.Int32
	goalSets     *atomic.Int32
	closed       chan struct{}
	closedOnce   sync.Once
}

func newReconnectCountingConn(inner rpcwire.Conn, materializations, goalSets *atomic.Int32) *reconnectCountingConn {
	conn := &reconnectCountingConn{
		inner:        inner,
		materializes: materializations,
		goalSets:     goalSets,
		closed:       make(chan struct{}),
	}
	go func() {
		<-inner.Closed()
		conn.closedOnce.Do(func() { close(conn.closed) })
	}()
	return conn
}

func (c *reconnectCountingConn) Send(ctx context.Context, frame rpcwire.Frame) error {
	switch frame.Method {
	case protocol.MethodSessionWorkspaceChatMaterialize:
		c.materializes.Add(1)
	case protocol.MethodRuntimeGoalSet:
		c.goalSets.Add(1)
	}
	return c.inner.Send(ctx, frame)
}

func (c *reconnectCountingConn) Events() <-chan rpcwire.Event {
	return c.inner.Events()
}

func (c *reconnectCountingConn) Closed() <-chan struct{} {
	return c.closed
}

func (c *reconnectCountingConn) Close() error {
	return c.inner.Close()
}

func (c *reconnectCountingConn) Drop() error {
	return c.inner.Close()
}

func TestRemoteLazyChatMaterializationReconnectDoesNotReplayGoal(t *testing.T) {
	appCore, server := newLazyChatReconnectServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	endpoint, err := rpcwire.ParseWebSocketEndpoint("ws" + server.URL[len("http"):])
	if err != nil {
		t.Fatalf("ParseWebSocketEndpoint: %v", err)
	}
	intent, err := newRemoteDefaultProjectAttachmentIntent(appCore.ProjectID())
	if err != nil {
		t.Fatalf("newRemoteDefaultProjectAttachmentIntent: %v", err)
	}
	transport := &reconnectCountingTransport{base: rpcwire.NewWebSocketTransport()}
	remote, err := dialRemoteWithTransport(
		t.Context(),
		remoteDialPlan{endpoints: []rpcwire.Endpoint{endpoint}},
		transport,
		intent,
	)
	if err != nil {
		t.Fatalf("dialRemoteWithTransport: %v", err)
	}
	defer func() { _ = remote.Close() }()

	draft := "reconnect-safe unsent composer draft"
	launch, err := remote.WorkspaceChatDraft(t.Context(), serverapi.WorkspaceChatDraftRequest{
		Operation: serverapi.WorkspaceChatDraftOperation{
			Kind:    serverapi.WorkspaceChatDraftUpdateMessage,
			Message: &draft,
		},
	})
	if err != nil {
		t.Fatalf("WorkspaceChatDraft update: %v", err)
	}
	if launch.Message != draft {
		t.Fatalf("workspace draft = %q, want %q", launch.Message, draft)
	}
	materialized, err := remote.MaterializeWorkspaceChat(t.Context(), serverapi.WorkspaceChatMaterializeRequest{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	if materialized.SessionID.IsZero() || !materialized.SessionID.IsCanonicalUUIDv4() {
		t.Fatalf("materialized Session identity = %q, want canonical UUIDv4", materialized.SessionID)
	}
	first := transport.first
	if first == nil {
		t.Fatal("materialization did not use the first physical connection")
	}
	if err := first.Drop(); err != nil {
		t.Fatalf("DropFirstConnection: %v", err)
	}
	select {
	case <-first.Closed():
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first physical connection closure")
	}

	pollCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var page serverapi.SessionPageResponse
	for {
		page, err = remote.ListSessionPage(pollCtx, serverapi.SessionPageRequest{
			ProjectID: appCore.ProjectID(),
			Category:  sessioncontract.SessionCategoryMain,
			Limit:     remoteTestIntPointer(20),
		})
		if err == nil {
			break
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Session list did not reconnect before deadline: %v", err)
		}
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Session list reconnect failed with non-transient error: %v", err)
		}
		select {
		case <-pollCtx.Done():
			t.Fatalf("Session list did not reconnect: %v", pollCtx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != materialized.SessionID {
		t.Fatalf("reconnected Session page = %+v, want exactly %q", page.Sessions, materialized.SessionID)
	}
	input, err := remote.GetInitialInput(t.Context(), serverapi.SessionInitialInputRequest{
		SessionID: materialized.SessionID.String(),
	})
	if err != nil {
		t.Fatalf("GetInitialInput after reconnect: %v", err)
	}
	if input.Input != draft {
		t.Fatalf("reconnected composer draft = %q, want %q", input.Input, draft)
	}
	goal, err := remote.ShowGoal(t.Context(), serverapi.RuntimeGoalShowRequest{
		SessionID: materialized.SessionID.String(),
	})
	if err != nil {
		t.Fatalf("ShowGoal after reconnect: %v", err)
	}
	if goal.Goal != nil {
		t.Fatalf("reconnected Goal = %+v, want nil", goal.Goal)
	}
	if got := transport.materializations.Load(); got != 1 {
		t.Fatalf("materialization requests = %d, want one", got)
	}
	if got := transport.goalSets.Load(); got != 0 {
		t.Fatalf("Goal Set requests = %d, want zero", got)
	}
}

func newLazyChatReconnectServer(t *testing.T) (*core.Core, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   config.LoadOptions{ConfigRoot: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(t.Context(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	if _, err := authSupport.AuthManager.SwitchMethod(t.Context(), auth.Method{
		Type:   auth.MethodAPIKey,
		APIKey: &auth.APIKeyMethod{Key: "reconnect-test-key"},
	}, true); err != nil {
		t.Fatalf("SwitchMethod: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := core.New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	if appCore.ProjectID() != binding.ProjectID {
		t.Fatalf("Core ProjectID = %q, want %q", appCore.ProjectID(), binding.ProjectID)
	}
	gateway, err := servertransport.NewGateway(appCore, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "lazy-chat-reconnect-test",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return appCore, httptest.NewServer(gateway.Handler())
}
