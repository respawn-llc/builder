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

func (c *sessionExecutionEnvironmentAuthClient) GetAuthStatus(
	context.Context,
	serverapi.AuthStatusRequest,
) (serverapi.AuthStatusResponse, error) {
	c.calls++
	return c.response, c.err
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

type mismatchedSessionStoreResolver struct {
	store *session.Store
}

func (r mismatchedSessionStoreResolver) ResolvePersistedSession(context.Context, string) (session.PersistedSessionRecord, error) {
	meta := r.store.Meta()
	return session.PersistedSessionRecord{SessionDir: r.store.Dir(), Meta: &meta}, nil
}

type failingExecutionTargetResolver struct {
	err error
}

func (r failingExecutionTargetResolver) ResolveSessionExecutionTarget(context.Context, string) (clientui.SessionExecutionTarget, error) {
	return clientui.SessionExecutionTarget{}, r.err
}

type sessionExecutionEnvironmentFixture struct {
	metadata      *metadata.Store
	store         *session.Store
	sessionID     runtimeids.SessionID
	workspaceRoot string
}

func TestSessionExecutionEnvironmentRejectsMismatchedIdentity(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	otherID, err := runtimeids.ParseSessionID("other-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	service := NewService(mismatchedSessionStoreResolver{store: store}, nil, nil)
	if _, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
		SessionID: otherID,
	}); err == nil {
		t.Fatal("environment read accepted a response for another session")
	}
}

func TestSessionExecutionEnvironmentCompleteResponseIsReadOnly(t *testing.T) {
	fixture := newSessionExecutionEnvironmentFixture(t)
	markSessionExecutionEnvironmentGitRepository(t, fixture.workspaceRoot)
	authClient := &sessionExecutionEnvironmentAuthClient{
		response: serverapi.AuthStatusResponse{
			Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
				Method:        serverapi.AuthStatusMethodNone,
				Provider:      serverapi.OpenAIAuthProviderFacts(),
				EnvPreference: serverapi.AuthStatusEnvPreferenceUnspecified,
			}, nil),
		},
	}
	service := NewService(newTestSessionResolver(fixture.store), nil, fixture.metadata).
		WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
		WithExecutionEnvironmentAuth(authClient).
		WithExecutionEnvironmentGit(worktree.NewGitInspector(sessionExecutionEnvironmentGitRunner{
			output: []byte("worktree " + fixture.workspaceRoot + "\nHEAD abc123\nbranch refs/heads/main\n\n"),
		}))

	response := readEnvironmentAndAssertPersistenceUnchanged(t, fixture, service)
	workspace, workspaceOK := response.Environment.Workspace.Value()
	branch, branchOK := response.Environment.Branch.Value()
	model, modelOK := response.Environment.Model.Value()
	authState, authOK := response.Environment.Auth.Value()
	if !workspaceOK || workspace.Path != fixture.workspaceRoot {
		t.Fatalf("workspace = %+v/%v, want %q", workspace, workspaceOK, fixture.workspaceRoot)
	}
	if !branchOK || branch.Name != "main" {
		t.Fatalf("branch = %+v/%v, want main", branch, branchOK)
	}
	if !modelOK || model.Name != "gpt-5.6-sol" || model.Provider != "openai" || model.Locked {
		t.Fatalf("model = %+v/%v, want unlocked OpenAI model", model, modelOK)
	}
	if !authOK || authState.Provider != "openai" || authState.Method != serverapi.SessionExecutionAuthMethodNone {
		t.Fatalf("auth = %+v/%v, want explicit OpenAI no-auth", authState, authOK)
	}
	if authClient.calls != 1 {
		t.Fatalf("auth status calls = %d, want 1", authClient.calls)
	}
}

func TestSessionExecutionEnvironmentBranchProjection(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	sessionID := sessionExecutionEnvironmentSessionID(t, store)
	detachedRoot := t.TempDir()
	markSessionExecutionEnvironmentGitRepository(t, detachedRoot)
	subdirectoryRoot := t.TempDir()
	markSessionExecutionEnvironmentGitRepository(t, subdirectoryRoot)
	subdirectory := filepath.Join(subdirectoryRoot, "pkg")
	if err := os.MkdirAll(subdirectory, 0o755); err != nil {
		t.Fatalf("MkdirAll execution subdirectory: %v", err)
	}
	nonGitRoot := t.TempDir()
	failingRoot := t.TempDir()
	markSessionExecutionEnvironmentGitRepository(t, failingRoot)

	tests := []struct {
		name        string
		target      clientui.SessionExecutionTarget
		runner      sessionExecutionEnvironmentGitRunner
		branch      string
		unavailable serverapi.SessionExecutionBranchUnavailableReason
		failed      bool
	}{
		{
			name:   "detached head",
			target: availableSessionExecutionTarget(detachedRoot),
			runner: sessionExecutionEnvironmentGitRunner{
				output: []byte("worktree " + detachedRoot + "\nHEAD abc123\ndetached\n\n"),
			},
			unavailable: serverapi.SessionExecutionBranchUnavailableDetachedHead,
		},
		{
			name:   "execution subdirectory",
			target: availableSessionExecutionTarget(subdirectory),
			runner: sessionExecutionEnvironmentGitRunner{
				output: []byte("worktree " + subdirectoryRoot + "\nHEAD abc123\nbranch refs/heads/feature\n\n"),
			},
			branch: "feature",
		},
		{
			name:        "non git workspace",
			target:      availableSessionExecutionTarget(nonGitRoot),
			runner:      sessionExecutionEnvironmentGitRunner{},
			unavailable: serverapi.SessionExecutionBranchUnavailableNotGitRepository,
		},
		{
			name:   "unrelated git failure",
			target: availableSessionExecutionTarget(failingRoot),
			runner: sessionExecutionEnvironmentGitRunner{
				output:   []byte("opaque git diagnostic"),
				err:      errors.New("git exited"),
				exitCode: 128,
			},
			failed: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(newTestSessionResolver(store), nil, staticExecutionTargetResolver{target: test.target}).
				WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
				WithExecutionEnvironmentGit(worktree.NewGitInspector(test.runner))
			response, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
				SessionID: sessionID,
			})
			if err != nil {
				t.Fatalf("GetSessionExecutionEnvironment: %v", err)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("response validation: %v", err)
			}
			switch {
			case test.branch != "":
				branch, ok := response.Environment.Branch.Value()
				if !ok || branch.Name != test.branch {
					t.Fatalf("branch = %+v/%v, want %q", branch, ok, test.branch)
				}
			case test.failed:
				failure, ok := response.Environment.Branch.Failure()
				if !ok || failure.Code != serverapi.SessionExecutionFieldErrorSourceFailure {
					t.Fatalf("branch failure = %+v/%v", failure, ok)
				}
			default:
				reason, ok := response.Environment.Branch.UnavailableReason()
				if !ok || reason != test.unavailable {
					t.Fatalf("branch unavailable = %q/%v, want %q", reason, ok, test.unavailable)
				}
			}
		})
	}
}

func TestSessionExecutionEnvironmentFieldFailuresRemainIndependent(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	sessionID := sessionExecutionEnvironmentSessionID(t, store)
	workspaceRoot := t.TempDir()
	target := availableSessionExecutionTarget(workspaceRoot)

	t.Run("target resolution failure", func(t *testing.T) {
		service := NewService(
			newTestSessionResolver(store),
			nil,
			failingExecutionTargetResolver{err: errors.New("target unavailable")},
		).WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}})
		response, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
			SessionID: sessionID,
		})
		if err != nil {
			t.Fatalf("GetSessionExecutionEnvironment: %v", err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("response validation: %v", err)
		}
		failure, failed := response.Environment.Workspace.Failure()
		_, modelAvailable := response.Environment.Model.Value()
		if !failed || failure.Code != serverapi.SessionExecutionFieldErrorSourceFailure || !modelAvailable {
			t.Fatalf("workspace/model fields = %+v/%v model_available=%v", failure, failed, modelAvailable)
		}
	})

	t.Run("missing worktree", func(t *testing.T) {
		missingRoot := filepath.Join(t.TempDir(), "missing-worktree")
		missingTarget := availableSessionExecutionTarget(missingRoot)
		missingTarget.Worktree = &clientui.SessionExecutionWorktreeTarget{
			ID:           "worktree-id",
			Root:         missingRoot,
			Availability: "missing",
		}
		service := NewService(newTestSessionResolver(store), nil, staticExecutionTargetResolver{target: missingTarget}).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}})
		response, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
			SessionID: sessionID,
		})
		if err != nil {
			t.Fatalf("GetSessionExecutionEnvironment: %v", err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("response validation: %v", err)
		}
		failure, workspaceFailed := response.Environment.Workspace.Failure()
		branchReason, branchUnavailable := response.Environment.Branch.UnavailableReason()
		_, modelAvailable := response.Environment.Model.Value()
		if !workspaceFailed || failure.Code != serverapi.SessionExecutionFieldErrorSourceFailure ||
			!branchUnavailable || branchReason != serverapi.SessionExecutionBranchUnavailableNotGitRepository ||
			!modelAvailable {
			t.Fatalf(
				"workspace/branch/model = %+v/%v %q/%v model_available=%v",
				failure,
				workspaceFailed,
				branchReason,
				branchUnavailable,
				modelAvailable,
			)
		}
	})

	t.Run("auth lookup failure", func(t *testing.T) {
		authClient := &sessionExecutionEnvironmentAuthClient{err: errors.New("auth unavailable")}
		service := NewService(newTestSessionResolver(store), nil, staticExecutionTargetResolver{target: target}).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}}).
			WithExecutionEnvironmentAuth(authClient)
		response, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
			SessionID: sessionID,
		})
		if err != nil {
			t.Fatalf("GetSessionExecutionEnvironment: %v", err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("response validation: %v", err)
		}
		failure, failed := response.Environment.Auth.Failure()
		_, workspaceAvailable := response.Environment.Workspace.Value()
		_, modelAvailable := response.Environment.Model.Value()
		if !failed || failure.Code != serverapi.SessionExecutionFieldErrorSourceFailure ||
			!workspaceAvailable || !modelAvailable || authClient.calls != 1 {
			t.Fatalf(
				"auth/workspace/model = %+v/%v workspace_available=%v model_available=%v calls=%d",
				failure,
				failed,
				workspaceAvailable,
				modelAvailable,
				authClient.calls,
			)
		}
	})

	t.Run("non kent provider skips global auth", func(t *testing.T) {
		authClient := &sessionExecutionEnvironmentAuthClient{err: errors.New("must not be called")}
		service := NewService(newTestSessionResolver(store), nil, staticExecutionTargetResolver{target: target}).
			WithExecutionEnvironmentConfig(config.App{Settings: config.Settings{
				Model:            "claude-sonnet-4",
				ProviderOverride: "anthropic",
			}}).
			WithExecutionEnvironmentAuth(authClient)
		response, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
			SessionID: sessionID,
		})
		if err != nil {
			t.Fatalf("GetSessionExecutionEnvironment: %v", err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("response validation: %v", err)
		}
		reason, unavailable := response.Environment.Auth.UnavailableReason()
		if !unavailable || reason != serverapi.SessionExecutionAuthUnavailableNotApplicable || authClient.calls != 0 {
			t.Fatalf("auth unavailable = %q/%v calls=%d", reason, unavailable, authClient.calls)
		}
	})
}

func TestSessionExecutionEnvironmentModelFieldMapping(t *testing.T) {
	tests := []struct {
		name         string
		app          config.App
		locked       *session.LockedContract
		missing      bool
		invalid      bool
		wantName     string
		wantProvider string
	}{
		{name: "missing configuration", missing: true},
		{name: "invalid configuration", app: config.App{Settings: config.Settings{Model: "provider-unknown-model"}}, invalid: true},
		{
			name: "locked provider",
			app:  config.App{Settings: config.Settings{Model: "gpt-5.6-sol"}},
			locked: &session.LockedContract{
				Model: "claude-sonnet-4",
				ProviderContract: session.LockedProviderCapabilities{
					ProviderID: "anthropic",
				},
			},
			wantName:     "claude-sonnet-4",
			wantProvider: "anthropic",
		},
		{
			name:         "legacy partial lock",
			app:          config.App{Settings: config.Settings{Model: "claude-sonnet-4", ProviderOverride: "anthropic"}},
			locked:       &session.LockedContract{Model: "gpt-5.6-sol"},
			wantName:     "gpt-5.6-sol",
			wantProvider: "openai",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
			sessionID := sessionExecutionEnvironmentSessionID(t, store)
			if test.locked != nil {
				if err := store.MarkModelDispatchLocked(*test.locked); err != nil {
					t.Fatalf("MarkModelDispatchLocked: %v", err)
				}
			}
			service := NewService(newTestSessionResolver(store), nil, nil).
				WithExecutionEnvironmentConfig(test.app)
			response, err := service.GetSessionExecutionEnvironment(t.Context(), serverapi.SessionExecutionEnvironmentRequest{
				SessionID: sessionID,
			})
			if err != nil {
				t.Fatalf("GetSessionExecutionEnvironment: %v", err)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("response validation: %v", err)
			}
			if test.missing {
				reason, ok := response.Environment.Model.UnavailableReason()
				if !ok || reason != serverapi.SessionExecutionModelUnavailableNotConfigured {
					t.Fatalf("model unavailable = %q/%v", reason, ok)
				}
				return
			}
			if test.invalid {
				failure, ok := response.Environment.Model.Failure()
				if !ok || failure.Code != serverapi.SessionExecutionFieldErrorInvalidConfiguration {
					t.Fatalf("model failure = %+v/%v", failure, ok)
				}
				return
			}
			model, ok := response.Environment.Model.Value()
			if !ok || model.Name != test.wantName || model.Provider != test.wantProvider || !model.Locked {
				t.Fatalf("model = %+v/%v, want locked %s/%s", model, ok, test.wantName, test.wantProvider)
			}
		})
	}
}

func sessionExecutionEnvironmentSessionID(t *testing.T, store *session.Store) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func availableSessionExecutionTarget(workdir string) clientui.SessionExecutionTarget {
	return clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace-id",
		WorkspaceRoot:         workdir,
		WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		CwdRelpath:            ".",
		EffectiveWorkdir:      workdir,
	}
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
	return sessionExecutionEnvironmentFixture{
		metadata:      metadataStore,
		store:         store,
		sessionID:     sessionExecutionEnvironmentSessionID(t, store),
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
