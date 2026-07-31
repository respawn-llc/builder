package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"core/server/launch"
	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"

	tea "github.com/charmbracelet/bubbletea"
)

func sessionLifecycleStringPtr(value string) *string { return &value }

func TestRunSessionLifecycleReturnsMissingWorkspaceFailure(t *testing.T) {
	missingWorkspace := filepath.Join(t.TempDir(), "workspace-removed")
	containerDir := t.TempDir()
	persistence := sessiontest.NewPersistence()
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   missingWorkspace,
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		containerDir:       containerDir,
		sessionPersistence: persistence,
		projectID:          "project-1",
		projectViewClient: projectBindingFlowStubProjectViewService{
			resolveResp: serverapi.ProjectResolvePathResponse{
				CanonicalRoot: missingWorkspace,
				Binding: &serverapi.ProjectBinding{
					ProjectID:       "project-1",
					WorkspaceID:     "workspace-1",
					CanonicalRoot:   missingWorkspace,
					WorkspaceStatus: string(clientui.ProjectAvailabilityAvailable),
				},
			},
		},
		prepareRuntime: func(_ context.Context, plan sessionLaunchPlan, _ io.Writer, _ string) (*runtimeLaunchPlan, error) {
			_, _, _, err := runtimewire.NewLocalToolRegistryBinding(runtimewire.LocalToolRegistryOptions{
				WorkspaceRoot:       plan.ExecutionTarget.EffectiveWorkdir,
				OwnerSessionID:      plan.SessionID,
				Enabled:             []toolspec.ID{toolspec.ToolPatch},
				MinimumExecToBgTime: 15 * time.Second,
				ShellOutputMaxChars: 16_000,
				SupportsVision:      true,
			})
			return nil, err
		},
	}

	createIntent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	err := runSessionLifecycleWithOptions(context.Background(), server, nil, sessionLifecycleOptions{Intent: &createIntent})
	if err == nil {
		t.Fatal("expected startup error for missing workspace")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup error = %v, want os.ErrNotExist", err)
	}
}

func TestRunSessionLifecycleAppliesInitialAgentOverride(t *testing.T) {
	workspaceRoot := t.TempDir()
	stopErr := errors.New("stop after launch plan")
	var got serverapi.SessionPlanRequest
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   workspaceRoot,
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		projectID: "project-1",
		projectViewClient: sessionLifecycleProjectViewClient(metadata.Binding{
			ProjectID:   "project-1",
			WorkspaceID: "workspace-1",
		}, workspaceRoot, nil),
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			got = req
			return serverapi.SessionPlanResponse{}, stopErr
		}},
	}

	createIntent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	err := runSessionLifecycleWithOptions(context.Background(), server, nil, sessionLifecycleOptions{
		Intent:    &createIntent,
		Overrides: serverapi.RunPromptOverrides{AgentRole: sessionLifecycleStringPtr("worker")},
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("runSessionLifecycle error = %v, want %v", err, stopErr)
	}
	if got.Mode != serverapi.SessionLaunchModeInteractive || got.Intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
		t.Fatalf("launch request = %+v, want forced new interactive session", got)
	}
	if got.Overrides.AgentRole == nil || *got.Overrides.AgentRole != "worker" {
		t.Fatalf("agent override = %v, want worker", got.Overrides.AgentRole)
	}
}

func TestRunSessionLifecycleRejectsDifferentAgentRoleForLockedContinuation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	reviewerSettings := cfg.Settings
	reviewerSettings.Model = "gpt-5.6-sol"
	workerSettings := cfg.Settings
	workerSettings.Model = "gpt-5.4-mini"
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"reviewer": {Settings: reviewerSettings},
		"worker":   {Settings: workerSettings},
	}
	prepareAppTestPersistenceRoot(t, cfg.PersistenceRoot)
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store, err := session.Create(containerDir, filepath.Base(filepath.Clean(cfg.WorkspaceRoot)), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("reviewer")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "gpt-5.6-sol", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	service := sessionlaunch.NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      metadataStore.AuthoritativeSessionStoreOptions(),
		PersistedSessions: metadataStore,
	}).WithRuntimeAuthority(authority)
	server := &testEmbeddedServer{
		cfg:               cfg,
		projectID:         binding.ProjectID,
		projectViewClient: sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, nil),
		sessionLaunch:     service,
	}

	sessionID := sessionLifecycleSessionID(t, store.Meta().SessionID)
	openIntent := serverapi.OpenExistingSessionLaunchIntent(sessionID)
	err = runSessionLifecycleWithOptions(ctx, server, nil, sessionLifecycleOptions{
		Intent:    &openIntent,
		Overrides: serverapi.RunPromptOverrides{AgentRole: sessionLifecycleStringPtr("worker")},
	})
	if !errors.Is(err, launch.ErrLockedAgentRoleChange) {
		t.Fatalf("runSessionLifecycle error = %v, want locked role change", err)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeCanonicalizesAliases(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		&testEmbeddedServer{cfg: config.App{WorkspaceRoot: aliasRoot, Settings: config.Settings{Theme: "dark"}}},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         realRoot,
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("maybeHandlePickedSessionWorkspaceChange: %v", err)
	}
	if action != sessionWorkspaceChangeProceed {
		t.Fatalf("action = %v, want proceed", action)
	}
	if promptCalls != 0 {
		t.Fatalf("prompt calls = %d, want 0", promptCalls)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeUsesRemoteServerBindingRoot(t *testing.T) {
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	var promptedCurrentRoot string
	runWorkspaceChangePromptFlow = func(_ string, currentRoot string, _ string) (workspaceChangePromptResult, error) {
		promptCalls++
		promptedCurrentRoot = currentRoot
		return workspaceChangePromptResult{}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		&remoteAppServer{
			cfg: config.App{
				WorkspaceRoot: "/source-client-workspace",
				Settings:      config.Settings{Theme: "dark"},
			},
			retarget: &sessionWorkspaceRetargetContext{workspaceRoot: "/active-server-workspace", theme: "dark"},
		},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("maybeHandlePickedSessionWorkspaceChange: %v", err)
	}
	if action != sessionWorkspaceChangePickAgain {
		t.Fatalf("action = %v, want pick again", action)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
	if promptedCurrentRoot != "/active-server-workspace" {
		t.Fatalf("prompt current root = %q, want server binding root", promptedCurrentRoot)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeRejectsMissingBindingContext(t *testing.T) {
	_, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		narrowSessionLifecycleServer{},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err == nil {
		t.Fatal("expected missing workspace retarget context error")
	}
}

func TestRunSessionLifecyclePickerWorkspaceChangeYesRetargetsSessionAndReplans(t *testing.T) {
	home := t.TempDir()
	currentWorkspace := t.TempDir()
	previousWorkspace := t.TempDir()
	t.Setenv("HOME", home)

	cfg := loadAppTestConfig(t, currentWorkspace, config.LoadOptions{})
	binding := mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	store := createAttachedAuthoritativeAppSession(t, cfg.PersistenceRoot, binding.ProjectID, previousWorkspace)
	projectViews := sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, []clientui.SessionSummary{sessionLifecycleSessionSummary(t, store.Meta().SessionID, time.Now().UTC())})

	originalPicker := runSessionPickerFlow
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() {
		runSessionPickerFlow = originalPicker
		runWorkspaceChangePromptFlow = originalPrompt
	}()

	launchCalls := 0
	pickerCalls := 0
	runSessionPickerFlow = func(context.Context, sessionPageLoader, string, sessionPickerHeaderInfo) (sessionPickerResult, error) {
		pickerCalls++
		return newSessionPickerOpenResult(sessionLifecycleSessionID(t, store.Meta().SessionID)), nil
	}
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(selectedRoot string, currentRoot string, theme string) (workspaceChangePromptResult, error) {
		promptCalls++
		if comparableWorkspaceChangeRoot(selectedRoot) != mustCanonicalPath(t, previousWorkspace) {
			t.Fatalf("selected root = %q, want %q", selectedRoot, mustCanonicalPath(t, previousWorkspace))
		}
		if currentRoot != cfg.WorkspaceRoot {
			t.Fatalf("current root = %q, want %q", currentRoot, cfg.WorkspaceRoot)
		}
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	stopErr := errors.New("stop after prepare")
	prepareCalls := 0
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   cfg.WorkspaceRoot,
			PersistenceRoot: cfg.PersistenceRoot,
			Settings:        config.Settings{Theme: "dark"},
		},
		projectID:         binding.ProjectID,
		projectViewClient: projectViews,
		sessionViewClient: stubSessionViewClient{getSessionMainView: func(_ context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
			if req.SessionID != store.Meta().SessionID {
				return serverapi.SessionMainViewResponse{}, errors.New("unexpected session id")
			}
			targetRoot := previousWorkspace
			if launchCalls > 0 {
				targetRoot = cfg.WorkspaceRoot
			}
			return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{ExecutionTarget: clientui.SessionExecutionTarget{
				WorkspaceRoot:         targetRoot,
				WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
				EffectiveWorkdir:      targetRoot,
			}}}}, nil
		}},
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchCalls++
			reopened := openAuthoritativeAppSession(t, cfg.PersistenceRoot, store.Meta().SessionID)
			if comparableWorkspaceChangeRoot(reopened.Meta().WorkspaceRoot) != mustCanonicalPath(t, cfg.WorkspaceRoot) {
				t.Fatalf("session plan ran before workspace retarget: workspace=%q want=%q", reopened.Meta().WorkspaceRoot, mustCanonicalPath(t, cfg.WorkspaceRoot))
			}
			selectedID, present := req.Intent.SessionID()
			if !present || selectedID.String() != store.Meta().SessionID {
				t.Fatalf("selected session id = %q/%v, want %q/true", selectedID.String(), present, store.Meta().SessionID)
			}
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      store.Meta().SessionID,
				ActiveSettings: config.Settings{Theme: "dark"},
			}}, nil
		}},
		prepareRuntime: func(_ context.Context, plan sessionLaunchPlan, _ io.Writer, _ string) (*runtimeLaunchPlan, error) {
			prepareCalls++
			if plan.SessionID != store.Meta().SessionID {
				t.Fatalf("prepared session = %q, want %q", plan.SessionID, store.Meta().SessionID)
			}
			if plan.ExecutionTarget.EffectiveWorkdir != cfg.WorkspaceRoot {
				t.Fatalf("prepared workspace = %q, want %q", plan.ExecutionTarget.EffectiveWorkdir, cfg.WorkspaceRoot)
			}
			return nil, stopErr
		},
	}

	err := runSessionLifecycle(context.Background(), server, nil, "")
	if !errors.Is(err, stopErr) {
		t.Fatalf("runSessionLifecycle error = %v, want %v", err, stopErr)
	}
	if pickerCalls != 1 {
		t.Fatalf("picker calls = %d, want 1", pickerCalls)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
	if launchCalls != 1 {
		t.Fatalf("launch calls = %d, want 1", launchCalls)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
	}
	reopened := openAuthoritativeAppSession(t, cfg.PersistenceRoot, store.Meta().SessionID)
	if comparableWorkspaceChangeRoot(reopened.Meta().WorkspaceRoot) != mustCanonicalPath(t, cfg.WorkspaceRoot) {
		t.Fatalf("session workspace = %q, want %q", reopened.Meta().WorkspaceRoot, mustCanonicalPath(t, cfg.WorkspaceRoot))
	}
}

func TestRunSessionLifecyclePickerWorkspaceChangeNoReturnsToPicker(t *testing.T) {
	home := t.TempDir()
	currentWorkspace := t.TempDir()
	previousWorkspace := t.TempDir()
	t.Setenv("HOME", home)

	cfg := loadAppTestConfig(t, currentWorkspace, config.LoadOptions{})
	binding := mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	store := createArtifactBackedAttachedAppSession(t, cfg.PersistenceRoot, binding.ProjectID, previousWorkspace)
	metaBefore := store.Meta()
	projectViews := sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, []clientui.SessionSummary{sessionLifecycleSessionSummary(t, store.Meta().SessionID, time.Now().UTC())})

	originalPicker := runSessionPickerFlow
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() {
		runSessionPickerFlow = originalPicker
		runWorkspaceChangePromptFlow = originalPrompt
	}()

	launchCalls := 0
	pickerCalls := 0
	runSessionPickerFlow = func(context.Context, sessionPageLoader, string, sessionPickerHeaderInfo) (sessionPickerResult, error) {
		pickerCalls++
		if pickerCalls == 1 {
			return newSessionPickerOpenResult(sessionLifecycleSessionID(t, store.Meta().SessionID)), nil
		}
		return newSessionPickerCancelResult(), nil
	}
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{}, nil
	}
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   cfg.WorkspaceRoot,
			PersistenceRoot: cfg.PersistenceRoot,
			Settings:        config.Settings{Theme: "dark"},
		},
		projectID:         binding.ProjectID,
		projectViewClient: projectViews,
		sessionViewClient: stubSessionViewClient{getSessionMainView: func(_ context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
			if req.SessionID != store.Meta().SessionID {
				return serverapi.SessionMainViewResponse{}, errors.New("unexpected session id")
			}
			return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{ExecutionTarget: clientui.SessionExecutionTarget{
				WorkspaceRoot:         previousWorkspace,
				WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
				EffectiveWorkdir:      previousWorkspace,
			}}}}, nil
		}},
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchCalls++
			selectedID, present := req.Intent.SessionID()
			if !present || selectedID.String() != store.Meta().SessionID {
				t.Fatalf("selected session id = %q/%v, want %q/true", selectedID.String(), present, store.Meta().SessionID)
			}
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      store.Meta().SessionID,
				ActiveSettings: config.Settings{Theme: "dark"},
			}}, nil
		}},
	}

	err := runSessionLifecycle(context.Background(), server, nil, "")
	if err != nil {
		t.Fatalf("runSessionLifecycle error = %v, want clean lifecycle stop", err)
	}
	if pickerCalls != 2 {
		t.Fatalf("picker calls = %d, want 2", pickerCalls)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
	if launchCalls != 0 {
		t.Fatalf("launch calls = %d, want 0", launchCalls)
	}
	reopened := openAuthoritativeAppSession(t, cfg.PersistenceRoot, store.Meta().SessionID)
	metaAfter := reopened.Meta()
	if metaAfter.UpdatedAt.UnixMilli() != metaBefore.UpdatedAt.UnixMilli() || !sameOptionalSessionCategory(metaAfter.Category, metaBefore.Category) {
		t.Fatalf("declining workspace change mutated recency/category: before=%+v after=%+v", metaBefore, metaAfter)
	}
}

func TestRunSessionLifecycleWorkspaceChangeLookupFailureReturnsToPickerAndOpensAnother(t *testing.T) {
	home := t.TempDir()
	currentWorkspace := t.TempDir()
	t.Setenv("HOME", home)

	cfg := loadAppTestConfig(t, currentWorkspace, config.LoadOptions{})
	binding := mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	staleStore := createArtifactBackedAttachedAppSession(t, cfg.PersistenceRoot, binding.ProjectID, cfg.WorkspaceRoot)
	validStore := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	staleSessionID := staleStore.Meta().SessionID
	metaBefore := staleStore.Meta()
	projectViews := sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, []clientui.SessionSummary{
		sessionLifecycleSessionSummary(t, staleSessionID, time.Now().UTC()),
		sessionLifecycleSessionSummary(t, validStore.Meta().SessionID, time.Now().UTC().Add(-time.Minute)),
	})

	originalPicker := runSessionPickerFlow
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() {
		runSessionPickerFlow = originalPicker
		runWorkspaceChangePromptFlow = originalPrompt
	}()

	launchCalls := 0
	pickerCalls := 0
	runSessionPickerFlow = func(_ context.Context, _ sessionPageLoader, _ string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
		pickerCalls++
		switch pickerCalls {
		case 1:
			if header.Notice != nil {
				t.Fatalf("first picker notice = %+v, want none", header.Notice)
			}
			return newSessionPickerOpenResult(sessionLifecycleSessionID(t, staleSessionID)), nil
		case 2:
			if launchCalls != 0 {
				t.Fatalf("workspace lookup failure planned before picker retry: launchCalls=%d", launchCalls)
			}
			if header.Notice == nil ||
				header.Notice.Kind != startupPickerNoticeError ||
				header.Notice.Text == "" ||
				!errors.Is(header.Notice.Diagnostic, session.ErrSessionNotFound) {
				t.Fatalf("workspace lookup notice = %+v, want generic surfaced failure", header.Notice)
			}
			return newSessionPickerOpenResult(sessionLifecycleSessionID(t, validStore.Meta().SessionID)), nil
		}
		t.Fatalf("unexpected picker call %d", pickerCalls)
		return nil, nil
	}
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	stopErr := errors.New("stop after prepare recovered")
	prepareCalls := 0
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   cfg.WorkspaceRoot,
			PersistenceRoot: cfg.PersistenceRoot,
			Settings:        config.Settings{Theme: "dark"},
		},
		projectID:         binding.ProjectID,
		projectViewClient: projectViews,
		sessionViewClient: stubSessionViewClient{getSessionMainView: func(_ context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
			switch req.SessionID {
			case staleSessionID:
				return serverapi.SessionMainViewResponse{}, session.ErrSessionNotFound
			case validStore.Meta().SessionID:
				return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{ExecutionTarget: clientui.SessionExecutionTarget{
					WorkspaceRoot:         cfg.WorkspaceRoot,
					WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
					EffectiveWorkdir:      cfg.WorkspaceRoot,
				}}}}, nil
			default:
				return serverapi.SessionMainViewResponse{}, errors.New("unexpected session id")
			}
		}},
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchCalls++
			selectedID, _ := req.Intent.SessionID()
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      selectedID.String(),
				ActiveSettings: config.Settings{Theme: "dark"},
			}}, nil
		}},
		prepareRuntime: func(_ context.Context, plan sessionLaunchPlan, _ io.Writer, _ string) (*runtimeLaunchPlan, error) {
			prepareCalls++
			if plan.SessionID != validStore.Meta().SessionID {
				t.Fatalf("prepared session = %q, want %q", plan.SessionID, validStore.Meta().SessionID)
			}
			return nil, stopErr
		},
	}

	err := runSessionLifecycle(context.Background(), server, nil, "")
	if !errors.Is(err, stopErr) {
		t.Fatalf("runSessionLifecycle error = %v, want %v", err, stopErr)
	}
	if pickerCalls != 2 {
		t.Fatalf("picker calls = %d, want 2", pickerCalls)
	}
	if promptCalls != 0 {
		t.Fatalf("prompt calls = %d, want 0", promptCalls)
	}
	if launchCalls != 1 {
		t.Fatalf("launch calls = %d, want 1", launchCalls)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
	}
	reopened := openAuthoritativeAppSession(t, cfg.PersistenceRoot, staleSessionID)
	metaAfter := reopened.Meta()
	if metaAfter.UpdatedAt.UnixMilli() != metaBefore.UpdatedAt.UnixMilli() || !sameOptionalSessionCategory(metaAfter.Category, metaBefore.Category) {
		t.Fatalf("workspace lookup failure mutated recency/category: before=%+v after=%+v", metaBefore, metaAfter)
	}
}

func TestRunSessionLifecycleExplicitSessionIDBypassesWorkspaceChangePrompt(t *testing.T) {
	home := t.TempDir()
	currentWorkspace := t.TempDir()
	previousWorkspace := t.TempDir()
	t.Setenv("HOME", home)

	cfg := loadAppTestConfig(t, currentWorkspace, config.LoadOptions{})
	binding := mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	store := createAttachedAuthoritativeAppSession(t, cfg.PersistenceRoot, binding.ProjectID, previousWorkspace)
	projectViews := sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, nil)

	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	launchCalls := 0
	stopErr := errors.New("stop after prepare explicit")
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   cfg.WorkspaceRoot,
			PersistenceRoot: cfg.PersistenceRoot,
			Settings:        config.Settings{Theme: "dark"},
		},
		projectID:         binding.ProjectID,
		projectViewClient: projectViews,
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchCalls++
			selectedID, present := req.Intent.SessionID()
			if !present || selectedID.String() != store.Meta().SessionID {
				t.Fatalf("selected session id = %q/%v, want %q/true", selectedID.String(), present, store.Meta().SessionID)
			}
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      store.Meta().SessionID,
				ActiveSettings: config.Settings{Theme: "dark"},
			}}, nil
		}},
		prepareRuntime: func(_ context.Context, plan sessionLaunchPlan, _ io.Writer, _ string) (*runtimeLaunchPlan, error) {
			if plan.ExecutionTarget.EffectiveWorkdir != cfg.WorkspaceRoot {
				t.Fatalf("prepared workspace = %q, want %q", plan.ExecutionTarget.EffectiveWorkdir, cfg.WorkspaceRoot)
			}
			return nil, stopErr
		},
	}

	err := runSessionLifecycle(context.Background(), server, nil, store.Meta().SessionID)
	if !errors.Is(err, stopErr) {
		t.Fatalf("runSessionLifecycle error = %v, want %v", err, stopErr)
	}
	if promptCalls != 0 {
		t.Fatalf("prompt calls = %d, want 0", promptCalls)
	}
	if launchCalls != 1 {
		t.Fatalf("launch calls = %d, want 1", launchCalls)
	}
}

type stubSessionLaunchClient struct {
	planSession func(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error)
}

func (s stubSessionLaunchClient) PlanSession(ctx context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	if s.planSession == nil {
		return serverapi.SessionPlanResponse{}, errors.New("session launch stub is required")
	}
	return s.planSession(ctx, req)
}

func sessionLifecycleProjectViewClient(binding metadata.Binding, workspaceRoot string, sessions []clientui.SessionSummary) apicontract.ProjectViewService {
	return sessionLifecycleProjectViewService{
		projectBindingFlowStubProjectViewService: projectBindingFlowStubProjectViewService{
			resolveResp: serverapi.ProjectResolvePathResponse{
				CanonicalRoot: workspaceRoot,
				Binding: &serverapi.ProjectBinding{
					ProjectID:       binding.ProjectID,
					WorkspaceID:     binding.WorkspaceID,
					CanonicalRoot:   workspaceRoot,
					WorkspaceStatus: string(clientui.ProjectAvailabilityAvailable),
				},
			},
		},
		sessions: sessions,
	}
}

type sessionLifecycleProjectViewService struct {
	projectBindingFlowStubProjectViewService
	sessions []clientui.SessionSummary
}

func (s sessionLifecycleProjectViewService) ListSessionPage(_ context.Context, request serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	return serverapi.SessionPageResponse{
		ProjectID: request.ProjectID,
		Category:  request.Category,
		Sessions:  s.sessions,
	}, nil
}

func sessionLifecycleSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return sessionID
}

func sessionLifecycleSessionSummary(t *testing.T, raw string, updatedAt time.Time) clientui.SessionSummary {
	t.Helper()
	return clientui.SessionSummary{
		SessionID: sessionLifecycleSessionID(t, raw),
		Category:  sessioncontract.SessionCategoryMain,
		UpdatedAt: updatedAt,
	}
}

func sameOptionalSessionCategory(left *sessioncontract.SessionCategory, right *sessioncontract.SessionCategory) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func createAttachedAuthoritativeAppSession(t *testing.T, persistenceRoot string, projectID string, workspaceRoot string) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	if _, err := metadataStore.AttachWorkspaceToProject(context.Background(), projectID, workspaceRoot); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	store, err := session.Create(
		filepath.Join(filepath.Join(config.App{PersistenceRoot: persistenceRoot}.PersistenceRoot, "projects"), projectID, "sessions"),
		filepath.Base(filepath.Clean(workspaceRoot)),
		workspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("EnsureDurable: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	return store
}

func createArtifactBackedAttachedAppSession(t *testing.T, persistenceRoot string, projectID string, workspaceRoot string) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	if _, err := metadataStore.AttachWorkspaceToProject(context.Background(), projectID, workspaceRoot); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	store, err := session.Create(
		filepath.Join(filepath.Join(persistenceRoot, "projects"), projectID, "sessions"),
		filepath.Base(filepath.Clean(workspaceRoot)),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("EnsureDurable: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	return store
}

func mustCanonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return comparableWorkspaceChangeRoot(canonical)
}

func TestResolveSessionActionNewSessionUsesForceNewFlow(t *testing.T) {
	previousSessionID, err := runtimeids.ParseSessionID("parent-1")
	if err != nil {
		t.Fatalf("parse previous session id: %v", err)
	}
	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{},
		nil,
		"",
		UITransition{Action: UIActionNewSession, InitialPrompt: "hello", PreviousSessionID: &previousSessionID},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	parent := requireSessionCreateDestination(t, resolved)
	if parent == nil || *parent != "parent-1" {
		t.Fatalf("expected parent session id passthrough, got %+v", parent)
	}
	preparation, present := resolved.LaunchPreparation()
	if !present {
		t.Fatal("new-session result omitted launch preparation")
	}
	prompt, present := preparation.InitialPrompt()
	if !present || prompt.Text != "hello" {
		t.Fatalf("expected initial prompt passthrough, got %+v", prompt)
	}
}

func TestResolveSessionActionPreservesInitialPromptHistoryRecorded(t *testing.T) {
	client := &recordingSessionLifecycleClient{
		resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
			if !req.Transition.InitialPromptHistoryRecorded {
				t.Fatal("expected transition request to preserve initial prompt-history flag")
			}
			prompt := serverapi.SessionInitialPromptMetadata{
				Text:            req.Transition.InitialPrompt,
				HistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
			}
			return serverapi.LaunchSessionDirective(
				serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
				serverapi.NewSessionLaunchPreparation(
					&prompt,
					serverapi.RestoreStoredDraftSessionDraftDisposition(),
					serverapi.SessionAuthPreparationKeepCurrent,
				),
			), nil
		},
	}

	resolved, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{lifecycle: client},
		nil,
		"session-1",
		UITransition{Action: UIActionNewSession, InitialPrompt: "expanded prompt", InitialPromptHistoryRecorded: true},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	preparation, present := resolved.LaunchPreparation()
	if !present {
		t.Fatal("resolved transition omitted launch preparation")
	}
	prompt, present := preparation.InitialPrompt()
	if !present || !prompt.HistoryRecorded {
		t.Fatal("expected resolved transition to preserve initial prompt-history flag")
	}
}

func TestNewSessionTransitionKeepsBackgroundProcessesAlive(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'transition-job\n'; sleep 1"},
		DisplayCommand: "transition-job",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected process to move to background")
	}

	root := t.TempDir()
	persistence := sessiontest.NewPersistence()
	source, err := session.Create(
		root,
		filepath.Base(filepath.Clean(workdir)),
		workdir,
		sessioncontract.SessionCategoryMain,
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create source session: %v", err)
	}
	if err := source.EnsureDurable(); err != nil {
		t.Fatalf("persist source session: %v", err)
	}
	sourceSessionID := source.Meta().SessionID
	previousSessionID, err := runtimeids.ParseSessionID(sourceSessionID)
	if err != nil {
		t.Fatalf("parse source session id: %v", err)
	}
	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{background: manager},
		nil,
		"",
		UITransition{Action: UIActionNewSession, InitialPrompt: "hello", PreviousSessionID: &previousSessionID},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	parent := requireSessionCreateDestination(t, resolved)
	if parent == nil || *parent != sourceSessionID {
		t.Fatalf("expected new-session parent, got %+v", parent)
	}
	preparation, present := resolved.LaunchPreparation()
	if !present {
		t.Fatal("new-session result omitted launch preparation")
	}
	prompt, present := preparation.InitialPrompt()
	if !present || prompt.Text != "hello" {
		t.Fatalf("unexpected transition prompt %+v", prompt)
	}

	testServer := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   workdir,
			PersistenceRoot: root,
			Settings:        config.Settings{Theme: "dark"},
		},
		containerDir:       root,
		sessionPersistence: persistence,
	}
	planner := &launchPlanner{server: testServer}
	launchRequest := sessionLaunchRequestFromLifecycleResult(t, resolved, serverapi.RunPromptOverrides{})
	if _, err := planner.PlanSession(context.Background(), launchRequest); err != nil {
		t.Fatalf("open or create next session: %v", err)
	}
	entries := manager.List()
	if len(entries) != 1 {
		t.Fatalf("expected background process to survive session transition, got %d entries", len(entries))
	}
	if entries[0].ID != res.SessionID {
		t.Fatalf("expected surviving background process %s, got %s", res.SessionID, entries[0].ID)
	}
}

func TestReviewTeleportLifecyclePreservesParentWorktreeContext(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	prepareAppTestPersistenceRoot(t, cfg.PersistenceRoot)
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	parent, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(filepath.Clean(cfg.WorkspaceRoot)),
		cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable parent: %v", err)
	}
	if err := parent.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: "http://review-parent.local/v1"}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	if err := parent.MarkModelDispatchLocked(session.LockedContract{Model: "locked-review-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	worktreeRoot := filepath.Join(cfg.WorkspaceRoot, "wt-review-lifecycle")
	if err := os.MkdirAll(filepath.Join(worktreeRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:              "worktree-review-lifecycle",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   canonicalWorktreeRoot,
		DisplayName:     filepath.Base(canonicalWorktreeRoot),
		Availability:    "available",
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := metadataStore.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{SessionID: parent.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: "worktree-review-lifecycle"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget parent: %v", err)
	}

	model := newProjectedStaticUIModel(
		WithUISessionID(parent.Meta().SessionID),
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
	)
	testSetMainInput(model, "/review pkg")
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected /review to quit into a new session transition")
	}
	updated := next.(*uiModel)
	if updated.exitAction != UIActionNewSession {
		t.Fatalf("action = %q, want %q", updated.exitAction, UIActionNewSession)
	}

	server := &testEmbeddedServer{cfg: cfg}
	resolved, err := resolveSessionAction(ctx, server, nil, parent.Meta().SessionID, updated.Transition())
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	intent, _ := requireAppLifecycleLaunch(t, resolved)
	launchRequest, err := sessionLaunchRequestFromIntent(intent, serverapi.RunPromptOverrides{})
	if err != nil {
		t.Fatalf("sessionLaunchRequestFromIntent: %v", err)
	}
	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(ctx, launchRequest)
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	child := openAuthoritativeAppSession(t, cfg.PersistenceRoot, plan.SessionID)
	childMeta := child.Meta()
	if childMeta.PreviousSessionID == nil || childMeta.PreviousSessionID.String() != parent.Meta().SessionID {
		t.Fatalf("child previous session id = %v, want %q", childMeta.PreviousSessionID, parent.Meta().SessionID)
	}
	if childMeta.Continuation == nil || childMeta.Continuation.OpenAIBaseURL != "http://review-parent.local/v1" {
		t.Fatalf("child continuation = %+v, want parent continuation", childMeta.Continuation)
	}
	if childMeta.Locked == nil || childMeta.Locked.Model != "locked-review-model" {
		t.Fatalf("child locked contract = %+v, want parent lock", childMeta.Locked)
	}
	target, err := metadataStore.ResolveSessionExecutionTarget(ctx, childMeta.SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget child: %v", err)
	}
	if target.Worktree == nil || target.Worktree.ID != "worktree-review-lifecycle" || target.CwdRelpath != "pkg" {
		t.Fatalf("child target = %+v, want parent worktree target", target)
	}
	if target.EffectiveWorkdir != filepath.Join(canonicalWorktreeRoot, "pkg") {
		t.Fatalf("child effective workdir = %q, want %q", target.EffectiveWorkdir, filepath.Join(canonicalWorktreeRoot, "pkg"))
	}
}

func TestResolveSessionActionOpenSessionRequiresCurrentSession(t *testing.T) {
	_, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{},
		nil,
		"",
		UITransition{Action: UIActionOpenSession, TargetSessionID: "session-42", InitialInput: textutil.Value("draft reply")},
	)
	if err == nil {
		t.Fatal("open-session navigation without a current session unexpectedly succeeded")
	}
}

func TestPersistSessionDraftIncludesStructuredRecoveryBuffers(t *testing.T) {
	var captured serverapi.SessionPersistInputDraftRequest
	client := &recordingSessionLifecycleClient{
		persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
			captured = req
			return serverapi.SessionPersistInputDraftResponse{}, nil
		},
	}
	model := newUIModelDefaults(nil)
	testSetMainInput(model, "visible draft")
	model.pendingInjected = queuedUserMessagesForTest("  pending injected\n")
	model.queued = queuedInputsForTest("\tqueued later  ")

	if err := persistSessionDraftToServer(context.Background(), narrowSessionLifecycleServer{lifecycle: client}, " session-1 ", model); err != nil {
		t.Fatalf("persistSessionDraftToServer: %v", err)
	}
	if captured.Input != "visible draft" || captured.SessionID != "session-1" {
		t.Fatalf("captured draft request = %+v", captured)
	}
	if len(captured.RecoveryBuffers) != 2 {
		t.Fatalf("recovery buffers = %+v, want pending injected and queued", captured.RecoveryBuffers)
	}
	want := []serverapi.SessionDraftRecoveryBuffer{
		{Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput, Text: "  pending injected\n"},
		{Kind: serverapi.SessionDraftRecoveryBufferQueuedInput, Text: "\tqueued later  "},
	}
	if !reflect.DeepEqual(captured.RecoveryBuffers, want) {
		t.Fatalf("recovery buffers = %+v, want ordered byte-preserved category/text %+v", captured.RecoveryBuffers, want)
	}
	if _, err := json.Marshal(captured); err != nil {
		t.Fatalf("marshal captured draft request: %v", err)
	}
}

func TestInitialRecoveryBuffersRestoreRetryAffordancesWithoutStartupSubmit(t *testing.T) {
	model := NewProjectedUIModel(nil,
		WithUIInitialInput("visible draft"),
		WithUIInitialRecoveryBuffers([]serverapi.SessionDraftRecoveryBuffer{
			{Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit, Text: "submitted before forced exit"},
			{Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput, Text: "pending steering"},
			{Kind: serverapi.SessionDraftRecoveryBufferQueuedInput, Text: "queued later"},
		}),
	).(*uiModel)

	wantInput := "visible draft\n\npending steering\n\nqueued later"
	if testMainInput(model) != wantInput {
		t.Fatalf("input = %q, want recovered visible retry input", testMainInput(model))
	}
	if model.startupSubmit != "" || model.activeSubmit.text != "" {
		t.Fatalf("recovery must not auto-submit: startup=%q active=%+v", model.startupSubmit, model.activeSubmit)
	}
	if len(model.pendingInjected) != 0 || len(model.queued) != 0 {
		t.Fatalf("recovery must not restore into operational queues: pending=%+v queued=%+v", model.pendingInjected, model.queued)
	}
	if len(model.recoveredDraftBuffers) != 2 {
		t.Fatalf("recovered buffers = %+v, want non-operational recovery affordance", model.recoveredDraftBuffers)
	}
	if model.transientStatus != "" {
		t.Fatalf("transient status = %q, want ordinary draft recovery to stay silent", model.transientStatus)
	}
}

func TestExitReleaseFailureReturnsAfterUILoopResult(t *testing.T) {
	releaseErr := errors.New("release failed")
	uiLoopReturned := false
	plan := &runtimeLaunchPlan{close: func() error {
		if !uiLoopReturned {
			t.Fatal("runtime release ran before UI loop returned")
		}
		return releaseErr
	}}
	uiLoopReturned = true
	if err := closeRuntimePlanAfterUIExit(plan, &uiModel{}); !errors.Is(err, releaseErr) {
		t.Fatalf("close error = %v, want release failure", err)
	}
}

func TestForcedLocalExitPropagatesDetachReleaseFailure(t *testing.T) {
	releaseErr := errors.New("detach failed")
	plan := &runtimeLaunchPlan{
		close:       func() error { t.Fatal("normal close must not run"); return nil },
		detachClose: func() error { return releaseErr },
	}
	model := &uiModel{uiSessionTransitionFeatureState: uiSessionTransitionFeatureState{forcedLocalExit: true}}
	if err := closeRuntimePlanAfterUIExit(plan, model); !errors.Is(err, releaseErr) {
		t.Fatalf("detach close error = %v, want release failure", err)
	}
}

func TestSessionLifecycleProjectionFailureUsesDetachOnlyRelease(t *testing.T) {
	model := projectionFailureFinalModel(t)
	detachCalls := 0
	plan := &runtimeLaunchPlan{
		close: func() error {
			t.Fatal("normal close must not run after projection failure")
			return nil
		},
		detachClose: func() error {
			detachCalls++
			return nil
		},
	}

	if err := releaseRuntimePlanAfterUIResult(plan, model, nil); err != nil {
		t.Fatalf("release error = %v", err)
	}
	if detachCalls != 1 {
		t.Fatalf("detach calls = %d, want 1", detachCalls)
	}
}

func TestResolvedSessionActionUsesFinalModelDetachPolicy(t *testing.T) {
	model := projectionFailureFinalModel(t)
	detachCalls := 0
	plan := &runtimeLaunchPlan{
		close: func() error {
			t.Fatal("normal close must not run for forced final model")
			return nil
		},
		detachClose: func() error {
			detachCalls++
			return nil
		},
	}
	server := narrowSessionLifecycleServer{lifecycle: &recordingSessionLifecycleClient{
		resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionDirective, error) {
			return serverapi.SelectSessionDirective(serverapi.SessionAuthPreparationKeepCurrent), nil
		},
	}}

	if _, err := resolveAndReleaseSessionAction(
		context.Background(),
		server,
		nil,
		"session",
		UITransition{Action: UIActionExit},
		plan,
		model,
	); err != nil {
		t.Fatalf("resolve and release forced final model: %v", err)
	}
	if detachCalls != 1 {
		t.Fatalf("detach calls = %d, want 1", detachCalls)
	}
}

func TestSessionLifecycleProjectionFailureDraftErrorStillUsesDetachOnlyRelease(t *testing.T) {
	model := projectionFailureFinalModel(t)
	persistErr := errors.New("persist draft failed")
	detachCalls := 0
	plan := &runtimeLaunchPlan{
		close: func() error {
			t.Fatal("normal close must not run after projection failure")
			return nil
		},
		detachClose: func() error {
			detachCalls++
			return nil
		},
	}

	err := releaseRuntimePlanAfterUIResult(plan, model, persistErr)
	if !errors.Is(err, persistErr) {
		t.Fatalf("release error = %v, want persistence failure", err)
	}
	if detachCalls != 1 {
		t.Fatalf("detach calls = %d, want 1", detachCalls)
	}
}

func TestSessionLifecycleProjectionFailurePreservesDraftAndDetachErrors(t *testing.T) {
	model := projectionFailureFinalModel(t)
	persistErr := errors.New("persist draft failed")
	detachErr := errors.New("detach failed")
	plan := &runtimeLaunchPlan{
		close: func() error {
			t.Fatal("normal close must not run after projection failure")
			return nil
		},
		detachClose: func() error {
			return detachErr
		},
	}

	err := releaseRuntimePlanAfterUIResult(plan, model, persistErr)
	if !errors.Is(err, persistErr) || !errors.Is(err, detachErr) {
		t.Fatalf("release error = %v, want persistence and detach failures", err)
	}
}

type narrowSessionLifecycleServer struct {
	lifecycle      apicontract.SessionLifecycleService
	cfg            config.App
	reauthenticate func(context.Context, authInteractor) error
}

func (s narrowSessionLifecycleServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return s.lifecycle
}

func (s narrowSessionLifecycleServer) Config() config.App {
	return s.cfg
}

func (s narrowSessionLifecycleServer) Reauthenticate(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	if s.reauthenticate == nil {
		return nil
	}
	return s.reauthenticate(ctx, interactor)
}

type recordingSessionLifecycleClient struct {
	getInitialInput          func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error)
	persistInputDraft        func(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error)
	retargetSessionWorkspace func(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error)
	resolveTransition        func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error)
}

func (c *recordingSessionLifecycleClient) Close() error {
	return nil
}

func (c *recordingSessionLifecycleClient) GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	if c.getInitialInput == nil {
		return serverapi.SessionInitialInputResponse{}, errors.New("unexpected GetInitialInput call")
	}
	return c.getInitialInput(ctx, req)
}

func (c *recordingSessionLifecycleClient) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	if c.persistInputDraft == nil {
		return serverapi.SessionPersistInputDraftResponse{}, errors.New("unexpected PersistInputDraft call")
	}
	return c.persistInputDraft(ctx, req)
}

func (c *recordingSessionLifecycleClient) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if c.retargetSessionWorkspace == nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("unexpected RetargetSessionWorkspace call")
	}
	return c.retargetSessionWorkspace(ctx, req)
}

func (c *recordingSessionLifecycleClient) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if c.resolveTransition == nil {
		return serverapi.SessionResolveTransitionResponse{}, errors.New("unexpected ResolveTransition call")
	}
	return c.resolveTransition(ctx, req)
}
