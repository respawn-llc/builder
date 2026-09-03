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
	tests := []struct {
		name              string
		request           func(context.Context, *client.Remote, string, string) error
		afterCancellation func(*client.Remote, string) error
	}{
		{name: "archive", request: func(ctx context.Context, remote *client.Remote, sessionID, outputPath string) error {
			_, err := remote.ArchiveSession(ctx, &sessionlaunchpb.SessionArchiveRequest{SessionId: sessionID, OutputPath: outputPath})
			return err
		}, afterCancellation: func(_ *client.Remote, outputPath string) error { _, err := os.Stat(outputPath); return err }},
		{name: "delete", request: func(ctx context.Context, remote *client.Remote, sessionID, _ string) error {
			_, err := remote.DeleteSession(ctx, &sessionlaunchpb.SessionDeleteRequest{SessionId: sessionID})
			return err
		}, afterCancellation: func(remote *client.Remote, _ string) error { return remote.Close() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRemovalGatewayFixture(t)
			sessionID, err := runtimeids.ParseSessionID(fixture.session.Meta().SessionID)
			if err != nil {
				t.Fatalf("parse Session ID: %v", err)
			}
			outputPath := filepath.Join(t.TempDir(), "session.tar.zst")
			ctx, cancel := context.WithCancel(t.Context())
			requestDone := make(chan error, 1)
			go func() {
				requestDone <- test.request(ctx, fixture.remote, sessionID.String(), outputPath)
			}()
			select {
			case <-fixture.deleteGate.started:
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for %s metadata removal", test.name)
			}

			cancel()
			if err := <-requestDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("%s request error = %v, want context canceled", test.name, err)
			}
			if err := test.afterCancellation(fixture.remote, outputPath); err != nil {
				t.Fatalf("%s post-cancellation behavior: %v", test.name, err)
			}
			if _, err := fixture.metadata.ResolvePersistedSession(
				t.Context(),
				sessionID.String(),
			); err != nil {
				t.Fatalf("Session changed before accepted %s completed: %v", test.name, err)
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
				t.Fatalf("Session admission released before accepted %s completed: %v", test.name, err)
			case <-time.After(20 * time.Millisecond):
			}

			close(fixture.deleteGate.release)
			if err := <-competingAdmission; err != nil {
				t.Fatalf("Session admission after accepted %s completion: %v", test.name, err)
			}
			if _, err := fixture.metadata.ResolvePersistedSession(
				t.Context(),
				sessionID.String(),
			); !errors.Is(err, session.ErrSessionNotFound) {
				t.Fatalf("Session after accepted %s completion = %v, want absent", test.name, err)
			}
		})
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
	service := sessionservice.NewGlobalSessionLifecycleService(persistenceRoot, authority, nil).WithPersistedSessionResolver(deleteGate)
	return sessionRemovalGatewayFixture{remote: newSessionRemovalGatewayRemote(t, service), authority: authority, metadata: metadataStore, session: persisted, deleteGate: deleteGate}
}

func newSessionRemovalGatewayRemote(t *testing.T, service apicontract.SessionLifecycleService) *client.Remote {
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

var _ GatewayDependencies = (*sessionRemovalGatewayDependencies)(nil)
