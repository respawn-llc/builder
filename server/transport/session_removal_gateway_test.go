package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/sessionservice"
	"core/shared/apicontract"
	"core/shared/client"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type sessionRemovalGatewayDependencies struct {
	*core.Core
	lifecycle apicontract.SessionLifecycleService
}

func (d *sessionRemovalGatewayDependencies) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return d.lifecycle
}

type blockingSessionRemovalMetadata struct {
	*metadata.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *blockingSessionRemovalMetadata) DeleteSession(ctx context.Context, sessionID string) error {
	m.once.Do(func() { close(m.started) })
	select {
	case <-m.release:
		return m.Store.DeleteSession(ctx, sessionID)
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

type sessionRemovalGatewayFixture struct {
	remote     *client.Remote
	authority  *sessionruntime.Authority
	metadata   *metadata.Store
	session    *session.Store
	deleteGate *blockingSessionRemovalMetadata
}

func TestSessionRemovalGatewayCancellationRetainsAcceptedWorkAndAdmission(t *testing.T) {
	for _, operation := range []string{"archive", "delete"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newSessionRemovalGatewayFixture(t)
			sessionID, err := runtimeids.ParseSessionID(fixture.session.Meta().SessionID)
			if err != nil {
				t.Fatalf("parse Session ID: %v", err)
			}
			outputPath := filepath.Join(t.TempDir(), "session.tar.zst")
			ctx, cancel := context.WithCancel(t.Context())
			requestDone := make(chan error, 1)
			go func() {
				if operation == "archive" {
					_, requestErr := fixture.remote.ArchiveSession(
						ctx,
						&sessionlaunchpb.SessionArchiveRequest{
							SessionId:  sessionID.String(),
							OutputPath: outputPath,
						},
					)
					requestDone <- requestErr
					return
				}
				_, requestErr := fixture.remote.DeleteSession(
					ctx,
					&sessionlaunchpb.SessionDeleteRequest{SessionId: sessionID.String()},
				)
				requestDone <- requestErr
			}()
			awaitSessionRemovalSignal(t, fixture.deleteGate.started, operation+" metadata removal")

			cancel()
			if err := <-requestDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("%s request error = %v, want context canceled", operation, err)
			}
			if operation == "delete" {
				if err := fixture.remote.Close(); err != nil {
					t.Fatalf("close delete Remote: %v", err)
				}
			}
			if _, err := fixture.metadata.ResolvePersistedSession(
				t.Context(),
				sessionID.String(),
			); err != nil {
				t.Fatalf("Session changed before accepted %s completed: %v", operation, err)
			}
			if operation == "archive" {
				if _, err := os.Stat(outputPath); err != nil {
					t.Fatalf("published archive before metadata removal: %v", err)
				}
			}

			competingAdmission := make(chan error, 1)
			go func() {
				competingAdmission <- fixture.authority.WithDestructiveSessionAdmission(
					context.Background(),
					sessionID,
					func(context.Context) error { return nil },
				)
			}()
			select {
			case err := <-competingAdmission:
				t.Fatalf("Session admission released before accepted %s completed: %v", operation, err)
			case <-time.After(20 * time.Millisecond):
			}

			close(fixture.deleteGate.release)
			awaitSessionAbsent(t, fixture.metadata, sessionID.String())
			if err := <-competingAdmission; err != nil {
				t.Fatalf("Session admission after accepted %s completion: %v", operation, err)
			}
		})
	}
}

func TestSessionRemovalPreDecodeAuthenticationFailureReturnsInternalResult(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	service := sessionlaunchpb.File_kent_api_session_launch_session_lifecycle_proto.Services().
		ByName("SessionLifecycleService")

	archiveResult := &sessionlaunchpb.SessionArchiveResult{}
	callGatewayDescriptor(
		t,
		conn,
		"archive-before-auth",
		service.Methods().ByName("Archive"),
		&sessionlaunchpb.SessionArchiveRequest{
			SessionId:  "11111111-1111-4111-8111-111111111111",
			OutputPath: "/tmp/pre-auth.tar.zst",
		},
		archiveResult,
	)
	if failure := archiveResult.GetError(); failure == nil ||
		failure.Code != "internal_failure" ||
		failure.GetInternalFailure() == nil {
		t.Fatalf("archive pre-auth result = %+v, want internal failure", archiveResult)
	}

	deleteResult := &sessionlaunchpb.SessionDeleteResult{}
	callGatewayDescriptor(
		t,
		conn,
		"delete-before-auth",
		service.Methods().ByName("Delete"),
		&sessionlaunchpb.SessionDeleteRequest{
			SessionId: "11111111-1111-4111-8111-111111111111",
		},
		deleteResult,
	)
	if failure := deleteResult.GetError(); failure == nil ||
		failure.Code != "internal_failure" ||
		failure.GetInternalFailure() == nil {
		t.Fatalf("delete pre-auth result = %+v, want internal failure", deleteResult)
	}
}

func newSessionRemovalGatewayFixture(t *testing.T) sessionRemovalGatewayFixture {
	t.Helper()
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(t.Context(), workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	persisted, err := session.Create(
		filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions"),
		binding.WorkspaceName,
		binding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: persistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() { _ = authority.Close(context.Background()) })
	deleteGate := &blockingSessionRemovalMetadata{
		Store:   metadataStore,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := sessionservice.NewGlobalSessionLifecycleService(
		persistenceRoot,
		authority,
		nil,
	).WithPersistedSessionResolver(deleteGate)

	return sessionRemovalGatewayFixture{
		remote:     newSessionRemovalGatewayRemote(t, service),
		authority:  authority,
		metadata:   metadataStore,
		session:    persisted,
		deleteGate: deleteGate,
	}
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

func awaitSessionAbsent(t *testing.T, store *metadata.Store, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, err := store.ResolvePersistedSession(t.Context(), sessionID)
		if errors.Is(err, session.ErrSessionNotFound) {
			return
		}
		if err != nil {
			t.Fatalf("resolve Session after removal: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Session %s remained after accepted removal completed", sessionID)
}

var _ GatewayDependencies = (*sessionRemovalGatewayDependencies)(nil)
var _ sessionservice.SessionRemovalMetadata = (*blockingSessionRemovalMetadata)(nil)
