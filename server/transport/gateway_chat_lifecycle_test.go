package transport

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"core/server/chatmutation"
	"core/server/sessionruntime"
	"core/shared/apicontract"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type gatewayChatLifecycleDependencies struct {
	GatewayDependencies
	chat apicontract.ChatMutationService
}

func (d *gatewayChatLifecycleDependencies) ChatMutationClient() apicontract.ChatMutationService {
	return d.chat
}

type gatewayChatLifecycleResolver struct {
	started chan context.Context
	proceed chan struct{}
	target  chatmutation.ResolvedTarget
}

func (r *gatewayChatLifecycleResolver) Resolve(
	ctx context.Context,
	_ chatmutation.TargetResolutionRequest,
) (chatmutation.ResolvedTarget, error) {
	r.started <- ctx
	select {
	case <-r.proceed:
		return r.target, nil
	case <-ctx.Done():
		return chatmutation.ResolvedTarget{}, context.Cause(ctx)
	}
}

type gatewayChatLifecyclePlanner struct {
	attachment chatmutation.RuntimeAttachment
}

func (p gatewayChatLifecyclePlanner) Open(
	context.Context,
	runtimeids.SessionID,
) (chatmutation.RuntimeAttachment, error) {
	return p.attachment, nil
}

type gatewayChatLifecycleAttachment struct {
	sessionID runtimeids.SessionID
	released  chan sessionruntime.RuntimeReleasePolicy
}

func (a *gatewayChatLifecycleAttachment) SessionID() runtimeids.SessionID {
	return a.sessionID
}

func (a *gatewayChatLifecycleAttachment) Release(
	_ context.Context,
	policy sessionruntime.RuntimeReleasePolicy,
) error {
	a.released <- policy
	return nil
}

type gatewayChatLifecycleAdmission struct {
	queueItemID runtimeids.QueueItemID
}

func (a gatewayChatLifecycleAdmission) AdmitChatUserTurn(
	context.Context,
	serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	return serverapi.ChatInputAdmissionResult{
		QueueItemID: a.queueItemID,
		Accepted:    true,
	}, nil
}

func (gatewayChatLifecycleAdmission) AdmitChatQueuedUserInput(
	context.Context,
	serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	panic("unexpected Queue admission")
}

func (gatewayChatLifecycleAdmission) AdmitManualCompaction(
	context.Context,
	serverapi.RuntimeCompactContextRequest,
) (bool, error) {
	panic("unexpected compaction admission")
}

func TestGatewayDisconnectStopsDeliveryWithoutCancelingChatOperation(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	store := createGatewayAuthoritativeSession(t, appCore)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}
	owner, err := chatmutation.NewOperationOwner(time.Second)
	if err != nil {
		t.Fatalf("NewOperationOwner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	resolver := &gatewayChatLifecycleResolver{
		started: make(chan context.Context, 1),
		proceed: make(chan struct{}),
		target:  chatmutation.ResolvedTarget{SessionID: sessionID},
	}
	attachment := &gatewayChatLifecycleAttachment{
		sessionID: sessionID,
		released:  make(chan sessionruntime.RuntimeReleasePolicy, 1),
	}
	service := chatmutation.NewService(
		owner,
		resolver,
		gatewayChatLifecyclePlanner{attachment: attachment},
		gatewayChatLifecycleAdmission{queueItemID: runtimeids.NewQueueItemID()},
	)
	gateway, err := NewGateway(
		&gatewayChatLifecycleDependencies{GatewayDependencies: appCore, chat: service},
		gatewayTestIdentity(),
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	t.Cleanup(server.Close)
	conn := dialGateway(t, server)
	handshakeGateway(t, conn)

	method := chatpb.File_kent_api_chat_chat_proto.Services().
		ByName("ChatService").
		Methods().
		ByName("Steer")
	sendGatewayDescriptor(
		t,
		conn,
		"disconnect-chat",
		method,
		chatSteerRequest(&chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		}),
	)
	operationCtx := <-resolver.started
	if err := conn.Close(); err != nil {
		t.Fatalf("close caller connection: %v", err)
	}
	select {
	case <-operationCtx.Done():
		t.Fatalf("Gateway disconnect canceled Chat operation: %v", context.Cause(operationCtx))
	default:
	}

	close(resolver.proceed)
	select {
	case policy := <-attachment.released:
		if policy != sessionruntime.RuntimeReleaseDetach {
			t.Fatalf("release policy = %v, want detach", policy)
		}
	case <-time.After(time.Second):
		t.Fatal("Chat operation did not finish after Gateway disconnect")
	}
}
