package sessionview

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/metadata"
	"core/server/session"
	"core/server/worktree"
	"core/shared/auth"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type sessionExecutionEnvironmentAuthClient struct {
	response serverapi.AuthStatusResponse
	err      error
	calls    int
}

type sessionExecutionEnvironmentGitRunner struct {
	output   []byte
	err      error
	exitCode int
}

func (r sessionExecutionEnvironmentGitRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return append([]byte(nil), r.output...), r.err
}

func (r sessionExecutionEnvironmentGitRunner) Run(context.Context, string, ...string) ([]byte, int, error) {
	exitCode := r.exitCode
	if r.err != nil && exitCode == 0 {
		exitCode = 1
	}
	return append([]byte(nil), r.output...), exitCode, r.err
}

type sessionExecutionEnvironmentFixture struct {
	metadata      *metadata.Store
	store         *session.Store
	sessionID     runtimeids.SessionID
	workspaceRoot string
}

func (c *sessionExecutionEnvironmentAuthClient) GetAuthStatus(
	context.Context,
	serverapi.AuthStatusRequest,
) (serverapi.AuthStatusResponse, error) {
	c.calls++
	return c.response, c.err
}

func newSessionExecutionEnvironmentStore(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
		sessionViewTestPersistence.Options()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	return store
}

func TestEnvironmentImmutabilityOnResolutionFailure(t *testing.T) {
	store := newSessionExecutionEnvironmentStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}

	service := NewService(NewStaticSessionResolver(store), nil, nil)
	_, _ = service.GetSessionExecutionEnvironment(context.Background(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: sessionID,
	})

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("execution-environment read mutated session events")
	}
}

func TestSessionExecutionEnvironmentRejectsMismatchedIdentity(t *testing.T) {
	store := newSessionExecutionEnvironmentStore(t)
	otherID, err := runtimeids.ParseSessionID("other-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	service := NewService(NewStaticSessionResolver(store), nil, nil)
	if _, err := service.GetSessionExecutionEnvironment(context.Background(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: otherID,
	}); err == nil {
		t.Fatal("environment read accepted a response for another session")
	}
}

func TestSessionExecutionEnvironmentMapsMissingModelToTypedUnavailable(t *testing.T) {
	store := newSessionExecutionEnvironmentStore(t)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	service := NewService(NewStaticSessionResolver(store), nil, nil)
	service.app = config.App{}

	response, err := service.GetSessionExecutionEnvironment(context.Background(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("GetSessionExecutionEnvironment: %v", err)
	}
	reason, ok := response.Environment.Model.UnavailableReason()
	if !ok || reason != serverapi.SessionExecutionModelUnavailableNotConfigured {
		t.Fatalf("model unavailable reason = %q/%v", reason, ok)
	}
}

func TestSessionExecutionEnvironmentAuthUsesEffectiveProviderAndSerializes(t *testing.T) {
	for _, test := range []struct {
		name     string
		method   auth.MethodType
		expected serverapi.SessionExecutionAuthMethod
	}{
		{name: "none", method: auth.MethodNone, expected: serverapi.SessionExecutionAuthMethodNone},
		{name: "oauth", method: auth.MethodOAuth, expected: serverapi.SessionExecutionAuthMethodOAuth},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newSessionExecutionEnvironmentStore(t)
			sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
			if err != nil {
				t.Fatalf("ParseSessionID: %v", err)
			}
			authClient := &sessionExecutionEnvironmentAuthClient{
				response: serverapi.AuthStatusResponse{Auth: serverapi.AuthStatusInfo{
					Visible: true,
					Method:  test.method,
				}},
			}
			service := NewService(
				NewStaticSessionResolver(store),
				nil,
				staticExecutionTargetResolver{target: clientui.SessionExecutionTarget{}},
			).WithExecutionEnvironmentConfig(config.App{
				Settings: config.Settings{Model: "gpt-5.6-sol"},
			}).WithExecutionEnvironmentAuth(authClient)

			response, err := service.GetSessionExecutionEnvironment(context.Background(), serverapi.SessionExecutionEnvironmentRequest{
				SessionID: sessionID,
			})
			if err != nil {
				t.Fatalf("GetSessionExecutionEnvironment: %v", err)
			}
			value, ok := response.Environment.Auth.Value()
			if !ok || value.Provider != "openai" || value.Method != test.expected {
				t.Fatalf("auth field = %+v/%v, want available openai %s", value, ok, test.expected)
			}
			if _, err := response.MarshalJSON(); err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			if authClient.calls != 1 {
				t.Fatalf("auth status calls = %d, want 1", authClient.calls)
			}
		})
	}
}

func TestSessionExecutionEnvironmentNonKentManagedProviderDoesNotExposeGlobalAuth(t *testing.T) {
	store := newSessionExecutionEnvironmentStore(t)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	authClient := &sessionExecutionEnvironmentAuthClient{
		response: serverapi.AuthStatusResponse{Auth: serverapi.AuthStatusInfo{
			Visible:  true,
			Method:   auth.MethodAPIKey,
			Provider: "openai",
		}},
	}
	service := NewService(
		NewStaticSessionResolver(store),
		nil,
		staticExecutionTargetResolver{target: clientui.SessionExecutionTarget{}},
	).WithExecutionEnvironmentConfig(config.App{
		Settings: config.Settings{Model: "claude-sonnet-4", ProviderOverride: "anthropic"},
	}).WithExecutionEnvironmentAuth(authClient)

	response, err := service.GetSessionExecutionEnvironment(context.Background(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("GetSessionExecutionEnvironment: %v", err)
	}
	reason, ok := response.Environment.Auth.UnavailableReason()
	if !ok || reason != serverapi.SessionExecutionAuthUnavailableNotApplicable {
		t.Fatalf("auth unavailable reason = %q/%v", reason, ok)
	}
	if authClient.calls != 0 {
		t.Fatalf("auth status calls = %d, want 0 for non-applicable provider", authClient.calls)
	}
}

func TestSessionExecutionEnvironmentMissingWorkspaceIsFieldFailure(t *testing.T) {
	store := newSessionExecutionEnvironmentStore(t)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	missingRoot := filepath.Join(t.TempDir(), "deleted-workspace")
	service := NewService(
		NewStaticSessionResolver(store),
		nil,
		staticExecutionTargetResolver{target: clientui.SessionExecutionTarget{
			WorkspaceID:           "workspace-id",
			WorkspaceRoot:         missingRoot,
			WorkspaceAvailability: "missing",
			EffectiveWorkdir:      missingRoot,
		}},
	).WithExecutionEnvironmentConfig(config.App{
		Settings: config.Settings{Model: "gpt-5.6-sol"},
	})

	response, err := service.GetSessionExecutionEnvironment(context.Background(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("GetSessionExecutionEnvironment: %v", err)
	}
	failure, ok := response.Environment.Workspace.Failure()
	if !ok || failure.Code != serverapi.SessionExecutionFieldErrorSourceFailure {
		t.Fatalf("workspace failure = %+v/%v", failure, ok)
	}
}

func TestSessionExecutionEnvironmentUnknownModelProviderIsFieldFailure(t *testing.T) {
	store := newSessionExecutionEnvironmentStore(t)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	service := NewService(NewStaticSessionResolver(store), nil, nil).
		WithExecutionEnvironmentConfig(config.App{
			Settings: config.Settings{Model: "provider-unknown-model"},
		})

	response, err := service.GetSessionExecutionEnvironment(context.Background(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("GetSessionExecutionEnvironment: %v", err)
	}
	failure, ok := response.Environment.Model.Failure()
	if !ok || failure.Code != serverapi.SessionExecutionFieldErrorInvalidConfiguration {
		t.Fatalf("model failure = %+v/%v", failure, ok)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation after field failure: %v", err)
	}
}

func TestSessionExecutionEnvironmentAuthorityMatrixIsReadOnly(t *testing.T) {
	t.Run("unlocked current model and live branch", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		markSessionExecutionEnvironmentGitRepository(t, fixture.workspaceRoot)
		authClient := &sessionExecutionEnvironmentAuthClient{
			response: serverapi.AuthStatusResponse{Auth: serverapi.AuthStatusInfo{
				Visible: true,
				Method:  auth.MethodNone,
			}},
		}
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
			WithExecutionEnvironmentAuth(authClient).
			WithExecutionEnvironmentGit(worktree.NewGitInspector(sessionExecutionEnvironmentGitRunner{
				output: []byte("worktree " + fixture.workspaceRoot + "\nHEAD abc123\nbranch refs/heads/main\n\n"),
			}))

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		model, ok := response.Environment.Model.Value()
		if !ok || model.Name != "gpt-5.6-sol" || model.Provider != "openai" || model.Locked {
			t.Fatalf("unlocked model = %+v/%v", model, ok)
		}
		branch, ok := response.Environment.Branch.Value()
		if !ok || branch.Name != "main" {
			t.Fatalf("live branch = %+v/%v", branch, ok)
		}
	})

	t.Run("locked model provider", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		if err := fixture.store.MarkModelDispatchLocked(session.LockedContract{
			Model: "claude-sonnet-4",
			ProviderContract: session.LockedProviderCapabilities{
				ProviderID: "anthropic",
			},
		}); err != nil {
			t.Fatalf("MarkModelDispatchLocked: %v", err)
		}
		authClient := &sessionExecutionEnvironmentAuthClient{
			response: serverapi.AuthStatusResponse{Auth: serverapi.AuthStatusInfo{
				Visible:  true,
				Method:   auth.MethodAPIKey,
				Provider: "openai",
			}},
		}
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
			WithExecutionEnvironmentAuth(authClient)

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		model, ok := response.Environment.Model.Value()
		if !ok || model.Provider != "anthropic" || !model.Locked {
			t.Fatalf("locked model = %+v/%v", model, ok)
		}
		if _, ok := response.Environment.Auth.UnavailableReason(); !ok {
			t.Fatalf("locked non-applicable auth = %+v", response.Environment.Auth)
		}
		if authClient.calls != 0 {
			t.Fatalf("auth status calls = %d, want 0", authClient.calls)
		}
	})

	t.Run("legacy partial locked provider infers without repair", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		if err := fixture.store.MarkModelDispatchLocked(session.LockedContract{
			Model: "gpt-5.6-sol",
		}); err != nil {
			t.Fatalf("MarkModelDispatchLocked: %v", err)
		}
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{
				Model:            "claude-sonnet-4",
				ProviderOverride: "anthropic",
			}})

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		model, ok := response.Environment.Model.Value()
		if !ok || model.Provider != "openai" || !model.Locked {
			t.Fatalf("legacy partial locked model = %+v/%v", model, ok)
		}
	})

	t.Run("missing worktree", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		missingRoot := filepath.Join(t.TempDir(), "deleted-worktree")
		target := clientui.SessionExecutionTarget{
			WorkspaceID:           "workspace-id",
			WorkspaceRoot:         fixture.workspaceRoot,
			WorkspaceAvailability: "available",
			Worktree: &clientui.SessionExecutionWorktreeTarget{
				ID:           "worktree-id",
				Root:         missingRoot,
				Availability: "missing",
			},
			EffectiveWorkdir: missingRoot,
		}
		service := NewService(
			NewStaticSessionResolver(fixture.store),
			nil,
			staticExecutionTargetResolver{target: target},
		).WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}})

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		if _, ok := response.Environment.Workspace.Failure(); !ok {
			t.Fatalf("missing-worktree workspace = %+v", response.Environment.Workspace)
		}
	})

	t.Run("detached git", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		markSessionExecutionEnvironmentGitRepository(t, fixture.workspaceRoot)
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
			WithExecutionEnvironmentGit(worktree.NewGitInspector(sessionExecutionEnvironmentGitRunner{
				output: []byte("worktree " + fixture.workspaceRoot + "\nHEAD abc123\ndetached\n\n"),
			}))

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		reason, ok := response.Environment.Branch.UnavailableReason()
		if !ok || reason != serverapi.SessionExecutionBranchUnavailableDetachedHead {
			t.Fatalf("detached branch reason = %q/%v", reason, ok)
		}
	})

	t.Run("non git workspace", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
			WithExecutionEnvironmentGit(worktree.NewGitInspector(sessionExecutionEnvironmentGitRunner{}))

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		reason, ok := response.Environment.Branch.UnavailableReason()
		if !ok || reason != serverapi.SessionExecutionBranchUnavailableNotGitRepository {
			t.Fatalf("non-git branch reason = %q/%v", reason, ok)
		}
	})

	t.Run("unrelated git exit 128 is a branch field failure", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		markSessionExecutionEnvironmentGitRepository(t, fixture.workspaceRoot)
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
			WithExecutionEnvironmentGit(worktree.NewGitInspector(sessionExecutionEnvironmentGitRunner{
				output:   []byte("opaque git diagnostic"),
				err:      errors.New("git exited"),
				exitCode: 128,
			}))

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		failure, ok := response.Environment.Branch.Failure()
		if !ok || failure.Code != serverapi.SessionExecutionFieldErrorSourceFailure {
			t.Fatalf("branch failure = %+v/%v", failure, ok)
		}
	})

	t.Run("explicit no auth", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		authClient := &sessionExecutionEnvironmentAuthClient{
			response: serverapi.AuthStatusResponse{Auth: serverapi.AuthStatusInfo{
				Visible: true,
				Method:  auth.MethodNone,
			}},
		}
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
			WithExecutionEnvironmentAuth(authClient)

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		value, ok := response.Environment.Auth.Value()
		if !ok || value.Provider != "openai" || value.Method != serverapi.SessionExecutionAuthMethodNone {
			t.Fatalf("no-auth field = %+v/%v", value, ok)
		}
	})

	t.Run("global auth provider mismatch", func(t *testing.T) {
		fixture := newSessionExecutionEnvironmentFixture(t)
		authClient := &sessionExecutionEnvironmentAuthClient{
			response: serverapi.AuthStatusResponse{Auth: serverapi.AuthStatusInfo{
				Visible:  true,
				Method:   auth.MethodAPIKey,
				Provider: "openai",
			}},
		}
		service := NewService(NewStaticSessionResolver(fixture.store), nil, fixture.metadata).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{
				Model:            "claude-sonnet-4",
				ProviderOverride: "anthropic",
			}}).
			WithExecutionEnvironmentAuth(authClient)

		response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
		reason, ok := response.Environment.Auth.UnavailableReason()
		if !ok || reason != serverapi.SessionExecutionAuthUnavailableNotApplicable {
			t.Fatalf("provider-mismatch auth reason = %q/%v", reason, ok)
		}
		if authClient.calls != 0 {
			t.Fatalf("auth status calls = %d, want 0", authClient.calls)
		}
	})
}

func markSessionExecutionEnvironmentGitRepository(t *testing.T, workspaceRoot string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(workspaceRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir .git: %v", err)
	}
}

func newSessionExecutionEnvironmentFixture(t *testing.T) sessionExecutionEnvironmentFixture {
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
	store, err := session.Create(
		filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions"),
		filepath.Base(binding.CanonicalRoot),
		binding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionExecutionEnvironmentFixture{
		metadata:      metadataStore,
		store:         store,
		sessionID:     sessionID,
		workspaceRoot: binding.CanonicalRoot,
	}
}

func readEnvironmentAndAssertPersistenceUnchanged(
	t *testing.T,
	fixture sessionExecutionEnvironmentFixture,
	service *Service,
) serverapi.SessionExecutionEnvironmentResponse {
	t.Helper()
	eventsPath := filepath.Join(fixture.store.Dir(), "events.jsonl")
	beforeEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}
	beforeRecord, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.sessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession before: %v", err)
	}

	response, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: fixture.sessionID,
	})
	if err != nil {
		t.Fatalf("GetSessionExecutionEnvironment: %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation: %v", err)
	}

	afterEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if !bytes.Equal(afterEvents, beforeEvents) {
		t.Fatal("execution-environment read mutated session events")
	}
	afterRecord, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.sessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession after: %v", err)
	}
	if beforeRecord.SessionDir != afterRecord.SessionDir || !reflect.DeepEqual(beforeRecord.Meta, afterRecord.Meta) {
		t.Fatalf("execution-environment read mutated metadata row: before=%+v after=%+v", beforeRecord, afterRecord)
	}
	return response
}
