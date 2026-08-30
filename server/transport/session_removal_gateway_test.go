package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/client"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"
)

type sessionRemovalGatewayService struct {
	archiveStarted  chan struct{}
	archiveCanceled chan struct{}
	deleteStarted   chan struct{}
	deleteCanceled  chan struct{}
	deleteRelease   chan struct{}
	deleteCompleted chan struct{}
}

func (*sessionRemovalGatewayService) GetInitialInput(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	return serverapi.SessionInitialInputResponse{}, nil
}

func (*sessionRemovalGatewayService) PersistInputDraft(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	return serverapi.SessionPersistInputDraftResponse{}, nil
}

func (*sessionRemovalGatewayService) RetargetSessionWorkspace(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return serverapi.SessionRetargetWorkspaceResponse{}, nil
}

func (*sessionRemovalGatewayService) ResolveTransition(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	return serverapi.SessionResolveTransitionResponse{}, nil
}

func (s *sessionRemovalGatewayService) ArchiveSession(
	ctx context.Context,
	_ *sessionlaunchpb.SessionArchiveRequest,
) (*sessionlaunchpb.SessionArchiveSuccess, error) {
	close(s.archiveStarted)
	<-ctx.Done()
	close(s.archiveCanceled)
	return nil, context.Cause(ctx)
}

func (s *sessionRemovalGatewayService) DeleteSession(
	ctx context.Context,
	request *sessionlaunchpb.SessionDeleteRequest,
) (*sessionlaunchpb.SessionDeleteSuccess, error) {
	close(s.deleteStarted)
	go func() {
		<-s.deleteRelease
		close(s.deleteCompleted)
	}()
	select {
	case <-s.deleteCompleted:
		return &sessionlaunchpb.SessionDeleteSuccess{SessionId: request.SessionId}, nil
	case <-ctx.Done():
		close(s.deleteCanceled)
		return nil, context.Cause(ctx)
	}
}

type sessionRemovalGatewayDependencies struct {
	*core.Core
	lifecycle apicontract.SessionLifecycleService
}

func (d *sessionRemovalGatewayDependencies) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return d.lifecycle
}

func TestSessionArchiveDedicatedInvocationCancellationClosesGatewayContext(t *testing.T) {
	service := &sessionRemovalGatewayService{
		archiveStarted:  make(chan struct{}),
		archiveCanceled: make(chan struct{}),
	}
	remote := newSessionRemovalGatewayRemote(t, service)

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := remote.ArchiveSession(ctx, &sessionlaunchpb.SessionArchiveRequest{
			SessionId:  "session-archive",
			OutputPath: "/tmp/session-archive.tar.zst",
		})
		result <- err
	}()
	awaitSessionRemovalSignal(t, service.archiveStarted, "archive invocation")
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("ArchiveSession error = %v, want context canceled", err)
	}
	awaitSessionRemovalSignal(t, service.archiveCanceled, "archive Gateway context cancellation")
}

func TestSessionDeleteMultiplexedCancellationAndDisconnection(t *testing.T) {
	service := &sessionRemovalGatewayService{
		deleteStarted:   make(chan struct{}),
		deleteCanceled:  make(chan struct{}),
		deleteRelease:   make(chan struct{}),
		deleteCompleted: make(chan struct{}),
	}
	remote := newSessionRemovalGatewayRemote(t, service)

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := remote.DeleteSession(ctx, &sessionlaunchpb.SessionDeleteRequest{SessionId: "session-delete"})
		result <- err
	}()
	awaitSessionRemovalSignal(t, service.deleteStarted, "delete invocation")
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteSession error = %v, want context canceled", err)
	}
	select {
	case <-service.deleteCanceled:
		t.Fatal("local multiplexed cancellation canceled the Gateway invocation")
	default:
	}
	if err := remote.Close(); err != nil {
		t.Fatalf("close Remote: %v", err)
	}
	awaitSessionRemovalSignal(t, service.deleteCanceled, "delete Gateway context cancellation after disconnect")
	close(service.deleteRelease)
	awaitSessionRemovalSignal(t, service.deleteCompleted, "accepted delete completion after disconnect")
}

func newSessionRemovalGatewayRemote(
	t *testing.T,
	service apicontract.SessionLifecycleService,
) *client.Remote {
	t.Helper()
	appCore, _ := newGatewayTestCore(t, true, true)
	gateway, err := NewGateway(&sessionRemovalGatewayDependencies{
		Core:      appCore,
		lifecycle: service,
	}, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	t.Cleanup(server.Close)
	remote, err := client.DialRemoteURL(t.Context(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	return remote
}

func awaitSessionRemovalSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

var _ GatewayDependencies = (*sessionRemovalGatewayDependencies)(nil)
var _ apicontract.SessionLifecycleService = (*sessionRemovalGatewayService)(nil)
