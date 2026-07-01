package app

import (
	"context"
	"core/cli/app/internal/projectbinding"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/rollbacktarget"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRunSessionLifecycleMissingWorkspacePrepareRuntimeSuggestsRebind(t *testing.T) {
	missingWorkspace := filepath.Join(t.TempDir(), "workspace-removed")
	containerDir := t.TempDir()
	newWorkspace := t.TempDir()
	t.Chdir(newWorkspace)
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   missingWorkspace,
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		containerDir: containerDir,
		projectID:    "project-1",
		projectViewClient: client.NewLoopbackProjectViewClient(projectBindingFlowStubProjectViewService{
			resolveResp: serverapi.ProjectResolvePathResponse{
				CanonicalRoot: missingWorkspace,
				Binding: &serverapi.ProjectBinding{
					ProjectID:       "project-1",
					WorkspaceID:     "workspace-1",
					CanonicalRoot:   missingWorkspace,
					WorkspaceStatus: string(clientui.ProjectAvailabilityAvailable),
				},
			},
		}),
		prepareRuntime: func(_ context.Context, plan sessionLaunchPlan, _ io.Writer, _ string) (*runtimeLaunchPlan, error) {
			_, _, _, err := buildToolRegistry(
				plan.WorkspaceRoot,
				plan.SessionID,
				[]toolspec.ID{toolspec.ToolPatch},
				15*time.Second,
				16_000,
				false,
				true,
				nil,
				nil,
			)
			return nil, err
		},
	}

	err := runSessionLifecycle(context.Background(), server, nil, "")
	if err == nil {
		t.Fatal("expected startup error for missing workspace")
	}
	summaries, listErr := session.ListSessions(containerDir)
	if listErr != nil {
		t.Fatalf("ListSessions: %v", listErr)
	}
	if len(summaries) != 1 {
		t.Fatalf("session count = %d, want 1", len(summaries))
	}
	want := `workspace root ` + strconv.Quote(missingWorkspace) + ` is missing; run ` + "`kent rebind " + strconv.Quote(summaries[0].SessionID) + " " + strconv.Quote(newWorkspace) + "`"
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeSkipsPromptWhenWorkspaceUnchanged(t *testing.T) {
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(context.Background(), &testEmbeddedServer{cfg: config.App{WorkspaceRoot: "/tmp/workspace", Settings: config.Settings{Theme: "dark"}}}, sessionLaunchPlan{
		SessionID:                    "session-1",
		SelectedViaPicker:            true,
		SelectedSessionWorkspaceRoot: "/tmp/workspace",
	})
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

	action, err := maybeHandlePickedSessionWorkspaceChange(context.Background(), &testEmbeddedServer{cfg: config.App{WorkspaceRoot: aliasRoot, Settings: config.Settings{Theme: "dark"}}}, sessionLaunchPlan{
		SessionID:                    "session-1",
		SelectedViaPicker:            true,
		SelectedSessionWorkspaceRoot: realRoot,
	})
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

func TestMaybeHandlePickedSessionWorkspaceChangeLookupFailureReturnsPicker(t *testing.T) {
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(context.Background(), &testEmbeddedServer{cfg: config.App{WorkspaceRoot: "/tmp/workspace", Settings: config.Settings{Theme: "dark"}}}, sessionLaunchPlan{
		SessionID:                            "session-1",
		SelectedViaPicker:                    true,
		SelectedSessionWorkspaceLookupFailed: true,
	})
	if err != nil {
		t.Fatalf("maybeHandlePickedSessionWorkspaceChange: %v", err)
	}
	if action != sessionWorkspaceChangePickAgain {
		t.Fatalf("action = %v, want pick again", action)
	}
	if promptCalls != 0 {
		t.Fatalf("prompt calls = %d, want 0", promptCalls)
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
	projectViews := sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, []clientui.SessionSummary{{SessionID: store.Meta().SessionID, UpdatedAt: time.Now().UTC()}})

	originalPicker := runSessionPickerFlow
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() {
		runSessionPickerFlow = originalPicker
		runWorkspaceChangePromptFlow = originalPrompt
	}()

	pickerCalls := 0
	runSessionPickerFlow = func(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
		pickerCalls++
		for _, summary := range summaries {
			if summary.SessionID == store.Meta().SessionID {
				picked := summary
				return sessionPickerResult{Session: &picked}, nil
			}
		}
		t.Fatalf("picker summaries missing session %q", store.Meta().SessionID)
		return sessionPickerResult{}, nil
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

	launchCalls := 0
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
			return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{ExecutionTarget: clientui.SessionExecutionTarget{WorkspaceRoot: previousWorkspace}}}}, nil
		}},
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchCalls++
			if req.SelectedSessionID != store.Meta().SessionID {
				t.Fatalf("selected session id = %q, want %q", req.SelectedSessionID, store.Meta().SessionID)
			}
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      store.Meta().SessionID,
				WorkspaceRoot:  cfg.WorkspaceRoot,
				ActiveSettings: config.Settings{Theme: "dark"},
			}}, nil
		}},
		prepareRuntime: func(_ context.Context, plan sessionLaunchPlan, _ io.Writer, _ string) (*runtimeLaunchPlan, error) {
			prepareCalls++
			if plan.SessionID != store.Meta().SessionID {
				t.Fatalf("prepared session = %q, want %q", plan.SessionID, store.Meta().SessionID)
			}
			if plan.WorkspaceRoot != cfg.WorkspaceRoot {
				t.Fatalf("prepared workspace = %q, want %q", plan.WorkspaceRoot, cfg.WorkspaceRoot)
			}
			if plan.SelectedViaPicker {
				t.Fatal("did not expect replanned explicit session to remain picker-selected")
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
	if launchCalls != 2 {
		t.Fatalf("launch calls = %d, want 2", launchCalls)
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
	store := createAttachedAuthoritativeAppSession(t, cfg.PersistenceRoot, binding.ProjectID, previousWorkspace)
	projectViews := sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, []clientui.SessionSummary{{SessionID: store.Meta().SessionID, UpdatedAt: time.Now().UTC()}})

	originalPicker := runSessionPickerFlow
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() {
		runSessionPickerFlow = originalPicker
		runWorkspaceChangePromptFlow = originalPrompt
	}()

	pickerCalls := 0
	runSessionPickerFlow = func(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
		pickerCalls++
		if pickerCalls == 1 {
			for _, summary := range summaries {
				if summary.SessionID == store.Meta().SessionID {
					picked := summary
					return sessionPickerResult{Session: &picked}, nil
				}
			}
			t.Fatalf("picker summaries missing session %q", store.Meta().SessionID)
		}
		return sessionPickerResult{Canceled: true}, nil
	}
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{}, nil
	}
	launchCalls := 0

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
			return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{ExecutionTarget: clientui.SessionExecutionTarget{WorkspaceRoot: previousWorkspace}}}}, nil
		}},
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchCalls++
			if req.SelectedSessionID != store.Meta().SessionID {
				t.Fatalf("selected session id = %q, want %q", req.SelectedSessionID, store.Meta().SessionID)
			}
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      store.Meta().SessionID,
				WorkspaceRoot:  cfg.WorkspaceRoot,
				ActiveSettings: config.Settings{Theme: "dark"},
			}}, nil
		}},
	}

	err := runSessionLifecycle(context.Background(), server, nil, "")
	if err == nil || !errors.Is(err, projectbinding.ErrStartupCanceledByUser) {
		t.Fatalf("runSessionLifecycle error = %v, want startup canceled by user", err)
	}
	if pickerCalls != 2 {
		t.Fatalf("picker calls = %d, want 2", pickerCalls)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
	if launchCalls != 1 {
		t.Fatalf("launch calls = %d, want 1", launchCalls)
	}
}

func TestRunSessionLifecycleStalePickedSessionReturnsToPickerAndOpensAnother(t *testing.T) {
	home := t.TempDir()
	currentWorkspace := t.TempDir()
	t.Setenv("HOME", home)

	cfg := loadAppTestConfig(t, currentWorkspace, config.LoadOptions{})
	binding := mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	validStore := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	staleSessionID := "missing-session"
	projectViews := sessionLifecycleProjectViewClient(binding, cfg.WorkspaceRoot, []clientui.SessionSummary{{SessionID: staleSessionID, UpdatedAt: time.Now().UTC()}, {SessionID: validStore.Meta().SessionID, UpdatedAt: time.Now().UTC().Add(-time.Minute)}})

	originalPicker := runSessionPickerFlow
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() {
		runSessionPickerFlow = originalPicker
		runWorkspaceChangePromptFlow = originalPrompt
	}()

	pickerCalls := 0
	runSessionPickerFlow = func(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
		pickerCalls++
		for _, summary := range summaries {
			if pickerCalls == 1 && summary.SessionID == staleSessionID {
				picked := summary
				return sessionPickerResult{Session: &picked}, nil
			}
			if pickerCalls == 2 && summary.SessionID == validStore.Meta().SessionID {
				picked := summary
				return sessionPickerResult{Session: &picked}, nil
			}
		}
		t.Fatalf("unexpected picker call %d with summaries %+v", pickerCalls, summaries)
		return sessionPickerResult{}, nil
	}
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	launchCalls := 0
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
				return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{ExecutionTarget: clientui.SessionExecutionTarget{WorkspaceRoot: cfg.WorkspaceRoot}}}}, nil
			default:
				return serverapi.SessionMainViewResponse{}, errors.New("unexpected session id")
			}
		}},
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchCalls++
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      req.SelectedSessionID,
				WorkspaceRoot:  cfg.WorkspaceRoot,
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
	if launchCalls != 2 {
		t.Fatalf("launch calls = %d, want 2", launchCalls)
	}
	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
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
			if req.SelectedSessionID != store.Meta().SessionID {
				t.Fatalf("selected session id = %q, want %q", req.SelectedSessionID, store.Meta().SessionID)
			}
			return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
				SessionID:      store.Meta().SessionID,
				WorkspaceRoot:  cfg.WorkspaceRoot,
				ActiveSettings: config.Settings{Theme: "dark"},
			}}, nil
		}},
		prepareRuntime: func(_ context.Context, plan sessionLaunchPlan, _ io.Writer, _ string) (*runtimeLaunchPlan, error) {
			if plan.WorkspaceRoot != cfg.WorkspaceRoot {
				t.Fatalf("prepared workspace = %q, want %q", plan.WorkspaceRoot, cfg.WorkspaceRoot)
			}
			if plan.SelectedViaPicker {
				t.Fatal("did not expect explicit session id to be marked picker-selected")
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

func sessionLifecycleProjectViewClient(binding metadata.Binding, workspaceRoot string, sessions []clientui.SessionSummary) client.ProjectViewClient {
	return client.NewLoopbackProjectViewClient(projectBindingFlowStubProjectViewService{
		resolveResp: serverapi.ProjectResolvePathResponse{
			CanonicalRoot: workspaceRoot,
			Binding: &serverapi.ProjectBinding{
				ProjectID:       binding.ProjectID,
				WorkspaceID:     binding.WorkspaceID,
				CanonicalRoot:   workspaceRoot,
				WorkspaceStatus: string(clientui.ProjectAvailabilityAvailable),
			},
		},
		projectOverviewResp: serverapi.ProjectGetOverviewResponse{Overview: clientui.ProjectOverview{Sessions: sessions}},
	})
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
		workspaceRoot,
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

func TestResolveSessionActionResumeReopensPicker(t *testing.T) {
	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{},
		nil,
		"",
		UITransition{Action: UIActionResume},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if !resolved.ShouldContinue {
		t.Fatal("expected lifecycle to continue for resume action")
	}
	if resolved.NextSessionID != "" {
		t.Fatalf("expected empty session id to force picker, got %q", resolved.NextSessionID)
	}
	if resolved.ForceNewSession {
		t.Fatal("did not expect force-new for resume action")
	}
	if resolved.ParentSessionID != "" {
		t.Fatalf("expected no parent session id on resume, got %q", resolved.ParentSessionID)
	}
	if resolved.InitialPrompt != "" || resolved.InitialInput != "" {
		t.Fatalf("expected no initial payload on resume, got prompt=%q input=%q", resolved.InitialPrompt, resolved.InitialInput)
	}
}

func TestResolveSessionActionExitStaysClientLocal(t *testing.T) {
	resolved, err := resolveSessionAction(
		context.Background(),
		nil,
		nil,
		"",
		UITransition{Exit: true},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if resolved.ShouldContinue {
		t.Fatal("expected exit transition not to continue")
	}
}

func TestResolveSessionActionNewSessionUsesForceNewFlow(t *testing.T) {
	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{},
		nil,
		"",
		UITransition{Action: UIActionNewSession, InitialPrompt: "hello", ParentSessionID: "parent-1"},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if !resolved.ShouldContinue {
		t.Fatal("expected lifecycle to continue for new session action")
	}
	if !resolved.ForceNewSession {
		t.Fatal("expected force-new session flow")
	}
	if resolved.NextSessionID != "" {
		t.Fatalf("expected empty session id for force-new flow, got %q", resolved.NextSessionID)
	}
	if resolved.ParentSessionID != "parent-1" {
		t.Fatalf("expected parent session id passthrough, got %q", resolved.ParentSessionID)
	}
	if resolved.InitialPrompt != "hello" || resolved.InitialInput != "" {
		t.Fatalf("expected initial prompt passthrough, got prompt=%q input=%q", resolved.InitialPrompt, resolved.InitialInput)
	}
}

func TestResolveSessionActionPreservesInitialPromptHistoryRecorded(t *testing.T) {
	client := &recordingSessionLifecycleClient{
		resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
			if !req.Transition.InitialPromptHistoryRecorded {
				t.Fatal("expected transition request to preserve initial prompt-history flag")
			}
			return serverapi.SessionResolveTransitionResponse{
				InitialPrompt:                req.Transition.InitialPrompt,
				InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
				ForceNewSession:              true,
				ShouldContinue:               true,
			}, nil
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
	if !resolved.InitialPromptHistoryRecorded {
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
	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{background: manager},
		nil,
		"",
		UITransition{Action: UIActionNewSession, InitialPrompt: "hello", ParentSessionID: "parent-1"},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if !resolved.ShouldContinue || !resolved.ForceNewSession {
		t.Fatalf("expected new-session transition, shouldContinue=%t forceNew=%t", resolved.ShouldContinue, resolved.ForceNewSession)
	}
	if resolved.NextSessionID != "" || resolved.InitialPrompt != "hello" || resolved.InitialInput != "" {
		t.Fatalf("unexpected transition payload nextSessionID=%q initialPrompt=%q initialInput=%q", resolved.NextSessionID, resolved.InitialPrompt, resolved.InitialInput)
	}

	testServer := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   workdir,
			PersistenceRoot: root,
			Settings:        config.Settings{Theme: "dark"},
		},
		containerDir: root,
	}
	planner := &launchPlanner{server: testServer}
	storePlan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{
		Mode:              launchModeInteractive,
		SelectedSessionID: resolved.NextSessionID,
		ForceNewSession:   resolved.ForceNewSession,
		ParentSessionID:   resolved.ParentSessionID,
	})
	if err != nil {
		t.Fatalf("open or create next session: %v", err)
	}
	store, err := testServer.sessionStoreRegistry().ResolveStore(context.Background(), storePlan.SessionID)
	if err != nil {
		t.Fatalf("resolve planned session from registry: %v", err)
	}
	if store == nil {
		t.Fatal("expected planned session store in registry")
	}
	if store.Meta().ParentSessionID != "parent-1" {
		t.Fatalf("expected parent session id preserved across new session transition, got %q", store.Meta().ParentSessionID)
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
		cfg.WorkspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
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
	if err := metadataStore.UpdateSessionExecutionTargetByID(ctx, parent.Meta().SessionID, binding.WorkspaceID, "worktree-review-lifecycle", "pkg"); err != nil {
		t.Fatalf("UpdateSessionExecutionTargetByID parent: %v", err)
	}

	model := newProjectedStaticUIModel(
		WithUISessionID(parent.Meta().SessionID),
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
	)
	model.input = "/review pkg"
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
	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(ctx, sessionLaunchRequest{
		Mode:            launchModeInteractive,
		ForceNewSession: resolved.ForceNewSession,
		ParentSessionID: resolved.ParentSessionID,
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	child := openAuthoritativeAppSession(t, cfg.PersistenceRoot, plan.SessionID)
	childMeta := child.Meta()
	if childMeta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("child parent session id = %q, want %q", childMeta.ParentSessionID, parent.Meta().SessionID)
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
	if target.WorktreeID != "worktree-review-lifecycle" || target.CwdRelpath != "pkg" {
		t.Fatalf("child target = %+v, want parent worktree target", target)
	}
	if target.EffectiveWorkdir != filepath.Join(canonicalWorktreeRoot, "pkg") {
		t.Fatalf("child effective workdir = %q, want %q", target.EffectiveWorkdir, filepath.Join(canonicalWorktreeRoot, "pkg"))
	}
}

func TestResolveSessionActionForkRollbackTeleportsToForkWithPrompt(t *testing.T) {
	root := t.TempDir()
	store := createAppRuntimeSessionAt(t, root, "workspace-x", "/tmp/work")
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{cfg: config.App{PersistenceRoot: root}, containerDir: root},
		nil,
		store.Meta().SessionID,
		UITransition{Action: UIActionForkRollback, InitialPrompt: "edited user message", ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, store, 1))},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if !resolved.ShouldContinue {
		t.Fatal("expected lifecycle to continue for fork rollback action")
	}
	if resolved.ForceNewSession {
		t.Fatal("did not expect force-new for fork rollback action")
	}
	if resolved.ParentSessionID != "" {
		t.Fatalf("expected no deferred parent for pre-created fork session, got %q", resolved.ParentSessionID)
	}
	if resolved.NextSessionID == "" {
		t.Fatal("expected target fork session id")
	}
	if resolved.NextSessionID == store.Meta().SessionID {
		t.Fatalf("expected fork session id to differ from parent, got %q", resolved.NextSessionID)
	}
	if resolved.InitialPrompt != "edited user message" || resolved.InitialInput != "" {
		t.Fatalf("expected initial prompt passthrough, got prompt=%q input=%q", resolved.InitialPrompt, resolved.InitialInput)
	}
}

func TestForkRollbackLifecycleDoesNotPersistEditedPromptAsSourceDraft(t *testing.T) {
	root := t.TempDir()
	store := createAppRuntimeSessionAt(t, root, "workspace-x", "/tmp/work")
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	m := newProjectedStaticUIModel()
	testSetRollbackEditing(m, 0, 0)
	m.input = "edited user message"
	server := &testEmbeddedServer{cfg: config.App{PersistenceRoot: root}, containerDir: root}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected quit cmd for rollback fork")
	}
	if err := persistSessionDraftToServer(context.Background(), server, store.Meta().SessionID, updated); err != nil {
		t.Fatalf("persist source draft: %v", err)
	}
	reopenedSource, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen source store: %v", err)
	}
	if reopenedSource.Meta().InputDraft != "" {
		t.Fatalf("expected no persisted source draft after fork handoff, got %q", reopenedSource.Meta().InputDraft)
	}

	resolved, err := resolveSessionAction(context.Background(), server, nil, reopenedSource.Meta().SessionID, updated.Transition())
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if resolved.InitialPrompt != "edited user message" {
		t.Fatalf("expected fork prompt passthrough, got %q", resolved.InitialPrompt)
	}
	if resolved.InitialInput != "" {
		t.Fatalf("expected no fork input draft payload, got %q", resolved.InitialInput)
	}
}

func TestResolveSessionActionOpenSessionUsesTargetID(t *testing.T) {
	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{},
		nil,
		"",
		UITransition{Action: UIActionOpenSession, TargetSessionID: "session-42", InitialInput: "draft reply"},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if !resolved.ShouldContinue {
		t.Fatal("expected lifecycle to continue for open session action")
	}
	if resolved.NextSessionID != "session-42" {
		t.Fatalf("expected target session id passthrough, got %q", resolved.NextSessionID)
	}
	if resolved.InitialPrompt != "" {
		t.Fatalf("expected no initial prompt, got %q", resolved.InitialPrompt)
	}
	if resolved.InitialInput != "draft reply" {
		t.Fatalf("expected initial input passthrough, got %q", resolved.InitialInput)
	}
	if resolved.ParentSessionID != "" {
		t.Fatalf("expected no parent session id, got %q", resolved.ParentSessionID)
	}
	if resolved.ForceNewSession {
		t.Fatal("did not expect force-new session")
	}
}

func TestSessionLaunchInitialInputUsesNarrowLifecycleClientFallback(t *testing.T) {
	got := sessionLaunchInitialInputFromServer(
		context.Background(),
		narrowSessionLifecycleServer{},
		"session-1",
		"typed draft",
	)
	if got != "typed draft" {
		t.Fatalf("initial input = %q, want fallback transition input", got)
	}
}

func TestPersistSessionDraftUsesNarrowLifecycleClient(t *testing.T) {
	var captured serverapi.SessionPersistInputDraftRequest
	client := &recordingSessionLifecycleClient{
		persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
			captured = req
			return serverapi.SessionPersistInputDraftResponse{}, nil
		},
	}
	model := &uiModel{}
	model.input = "draft from ui"
	if err := persistSessionDraftToServer(context.Background(), narrowSessionLifecycleServer{lifecycle: client}, " session-1 ", model); err != nil {
		t.Fatalf("persist draft: %v", err)
	}
	if captured.SessionID != "session-1" {
		t.Fatalf("session id = %q, want trimmed session-1", captured.SessionID)
	}
	if captured.Input != "draft from ui" {
		t.Fatalf("input = %q, want ui draft", captured.Input)
	}
	if captured.ClientRequestID == "" {
		t.Fatal("expected client request id")
	}
}

func TestResolveSessionActionReauthenticatesThroughNarrowServer(t *testing.T) {
	reauthCalls := 0
	client := &recordingSessionLifecycleClient{
		resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
			if req.SessionID != "session-1" {
				t.Fatalf("session id = %q, want session-1", req.SessionID)
			}
			if req.Transition.Action != UIActionOpenSession || req.Transition.TargetSessionID != "next-1" {
				t.Fatalf("transition = %+v, want open next-1", req.Transition)
			}
			return serverapi.SessionResolveTransitionResponse{
				NextSessionID:  "next-1",
				ShouldContinue: true,
				RequiresReauth: true,
			}, nil
		},
	}
	resolved, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{
			lifecycle: client,
			reauthenticate: func(context.Context, authInteractor) error {
				reauthCalls++
				return nil
			},
		},
		nil,
		" session-1 ",
		UITransition{Action: UIActionOpenSession, TargetSessionID: "next-1"},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if reauthCalls != 1 {
		t.Fatalf("reauth calls = %d, want 1", reauthCalls)
	}
	if !resolved.ShouldContinue || resolved.NextSessionID != "next-1" {
		t.Fatalf("resolved = %+v, want continue to next-1", resolved)
	}
}

func TestRetargetInteractiveSessionWorkspaceUsesNarrowServer(t *testing.T) {
	var captured serverapi.SessionRetargetWorkspaceRequest
	client := &recordingSessionLifecycleClient{
		retargetSessionWorkspace: func(_ context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
			captured = req
			return serverapi.SessionRetargetWorkspaceResponse{}, nil
		},
	}
	if err := retargetInteractiveSessionWorkspace(
		context.Background(),
		narrowSessionLifecycleServer{
			lifecycle: client,
			cfg:       config.App{WorkspaceRoot: " /tmp/current-workspace "},
		},
		" session-1 ",
	); err != nil {
		t.Fatalf("retarget workspace: %v", err)
	}
	if captured.SessionID != "session-1" {
		t.Fatalf("session id = %q, want trimmed session-1", captured.SessionID)
	}
	if captured.WorkspaceRoot != "/tmp/current-workspace" {
		t.Fatalf("workspace root = %q, want trimmed /tmp/current-workspace", captured.WorkspaceRoot)
	}
	if captured.ClientRequestID == "" {
		t.Fatal("expected client request id")
	}
}

func TestShouldRetryStartupUpdateNoticeUntilShown(t *testing.T) {
	if shouldRetryStartupUpdateNotice(&uiModel{}, true) != true {
		t.Fatal("expected retry when startup update notice was not shown")
	}
	if shouldRetryStartupUpdateNotice(&uiModel{uiStatusFeatureState: uiStatusFeatureState{startupUpdateShown: true}}, true) {
		t.Fatal("did not expect retry after startup update notice was shown")
	}
	if shouldRetryStartupUpdateNotice(&uiModel{}, false) {
		t.Fatal("did not expect retry when startup update notices are disabled")
	}
}

type narrowSessionLifecycleServer struct {
	lifecycle      client.SessionLifecycleClient
	cfg            config.App
	reauthenticate func(context.Context, authInteractor) error
}

func (s narrowSessionLifecycleServer) SessionLifecycleClient() client.SessionLifecycleClient {
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
