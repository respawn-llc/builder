package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/projectbinding"
	"core/server/metadata"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestEnsureInteractiveProjectBindingBindsRegisteredWorkspaceWithoutPrompt(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func([]clientui.ProjectSummary, string) (projectBindingPickerResult, error) {
		t.Fatal("did not expect binding picker for registered workspace")
		return projectBindingPickerResult{}, nil
	}
	runProjectNamePromptFlow = func(string, string) (string, error) {
		t.Fatal("did not expect project name prompt for registered workspace")
		return "", nil
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		projectViewClient: projectViewClient,
	}

	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	if got := bound.ProjectID(); got != binding.ProjectID {
		t.Fatalf("bound project id = %q, want %q", got, binding.ProjectID)
	}
}

func TestProjectBindingPickerMouseWheelScrolls(t *testing.T) {
	projects := make([]clientui.ProjectSummary, 0, 12)
	for i := range 12 {
		projects = append(projects, clientui.ProjectSummary{
			ProjectID:   fmt.Sprintf("project-%02d", i),
			DisplayName: fmt.Sprintf("Project %02d", i),
			RootPath:    fmt.Sprintf("/tmp/project-%02d", i),
			UpdatedAt:   time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}
	model := newProjectBindingPickerModel(projects, "dark", projectPickerOptions{
		AllowCreate:    false,
		HeaderMarkdown: serverProjectPickerHeaderMarkdown,
		HeaderFallback: serverProjectPickerHeaderFallback,
		NoticeText:     serverProjectPickerNoticeText,
		GroupLabel:     serverProjectExistingLabel,
	})
	model.height = 8

	updated, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	got := updated.(*projectBindingPickerModel)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after mouse wheel down", got.cursor)
	}
	updated, _ = got.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	got = updated.(*projectBindingPickerModel)
	if got.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after mouse wheel up", got.cursor)
	}
}

func TestProjectWorkspacePickerMouseWheelScrolls(t *testing.T) {
	workspaces := make([]clientui.ProjectWorkspaceSummary, 0, 12)
	for i := range 12 {
		workspaces = append(workspaces, clientui.ProjectWorkspaceSummary{
			WorkspaceID: fmt.Sprintf("workspace-%02d", i),
			DisplayName: fmt.Sprintf("Workspace %02d", i),
			RootPath:    fmt.Sprintf("/tmp/workspace-%02d", i),
			UpdatedAt:   time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}
	model := newProjectWorkspacePickerModel(workspaces, "dark")
	model.height = 8

	updated, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	got := updated.(*projectWorkspacePickerModel)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after mouse wheel down", got.cursor)
	}
	updated, _ = got.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	got = updated.(*projectWorkspacePickerModel)
	if got.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after mouse wheel up", got.cursor)
	}
}

func TestEnsureInteractiveProjectBindingTreatsNestedDirectoryAsUnknownWorkspace(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "subdir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}
	baseCfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	nestedCfg := loadAppTestConfig(t, nested, config.LoadOptions{})
	_, err := metadata.RegisterBinding(context.Background(), baseCfg.PersistenceRoot, baseCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	projectViewClient := newAppMetadataProjectViewClient(t, baseCfg)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func(projects []clientui.ProjectSummary, theme string) (projectBindingPickerResult, error) {
		if len(projects) != 1 {
			t.Fatalf("expected parent project to appear in picker, got %+v", projects)
		}
		return projectBindingPickerResult{CreateNew: true}, nil
	}
	runProjectNamePromptFlow = func(defaultName string, theme string) (string, error) {
		if want := filepath.Base(nested); defaultName != want {
			t.Fatalf("default project name = %q, want %q", defaultName, want)
		}
		return "Nested Project", nil
	}

	server := &testEmbeddedServer{
		cfg:               nestedCfg,
		containerDir:      filepath.Join(filepath.Join(nestedCfg.PersistenceRoot, "projects"), "project-placeholder", "sessions"),
		projectViewClient: projectViewClient,
	}

	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	resolved, err := metadata.ResolveBinding(context.Background(), nestedCfg.PersistenceRoot, nestedCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("ResolveBinding nested: %v", err)
	}
	if got := bound.ProjectID(); got != resolved.ProjectID {
		t.Fatalf("bound project id = %q, want %q", got, resolved.ProjectID)
	}
	canonicalNested, err := config.CanonicalWorkspaceRoot(nested)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot nested: %v", err)
	}
	if resolved.CanonicalRoot != canonicalNested {
		t.Fatalf("nested workspace root = %q, want %q", resolved.CanonicalRoot, canonicalNested)
	}
}

func TestEnsureInteractiveProjectBindingCreatesProjectForUnknownWorkspace(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func(projects []clientui.ProjectSummary, theme string) (projectBindingPickerResult, error) {
		if len(projects) != 0 {
			t.Fatalf("expected no projects, got %+v", projects)
		}
		return projectBindingPickerResult{CreateNew: true}, nil
	}
	runProjectNamePromptFlow = func(defaultName string, theme string) (string, error) {
		if want := filepath.Base(workspace); defaultName != want {
			t.Fatalf("default project name = %q, want %q", defaultName, want)
		}
		return "Project Alpha", nil
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), "project-placeholder", "sessions"),
		projectViewClient: projectViewClient,
	}

	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	if bound.ProjectID() == "" {
		t.Fatal("expected created project id")
	}
	resolved, err := metadata.ResolveBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	if resolved.ProjectID != bound.ProjectID() {
		t.Fatalf("resolved project id = %q, want %q", resolved.ProjectID, bound.ProjectID())
	}
	if resolved.ProjectName != "Project Alpha" {
		t.Fatalf("project name = %q, want Project Alpha", resolved.ProjectName)
	}
}

func TestEnsureInteractiveProjectBindingUsesServerBrowsingForMissingServerPath(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	service := projectBindingFlowStubProjectViewService{
		resolveResp: serverapi.ProjectResolvePathResponse{
			CanonicalRoot:    cfg.WorkspaceRoot,
			PathAvailability: clientui.ProjectAvailabilityMissing,
		},
		listProjectsResp: serverapi.ProjectListResponse{Projects: []clientui.ProjectSummary{{
			ProjectID:   "project-1",
			DisplayName: "Remote Project",
			RootPath:    "/srv/project",
		}}},
		projectOverviewResp: serverapi.ProjectGetOverviewResponse{Overview: clientui.ProjectOverview{
			Project: clientui.ProjectSummary{ProjectID: "project-1", DisplayName: "Remote Project", RootPath: "/srv/project"},
			Workspaces: []clientui.ProjectWorkspaceSummary{{
				WorkspaceID: "workspace-1",
				DisplayName: "Workspace 1",
				RootPath:    "/srv/project",
			}},
		}},
	}

	originalLocalPicker := runProjectBindingPickerFlow
	originalRemotePicker := runServerProjectPickerFlow
	originalWorkspacePicker := runProjectWorkspacePickerFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalLocalPicker
		runServerProjectPickerFlow = originalRemotePicker
		runProjectWorkspacePickerFlow = originalWorkspacePicker
	})
	runProjectBindingPickerFlow = func([]clientui.ProjectSummary, string) (projectBindingPickerResult, error) {
		t.Fatal("did not expect local binding picker in server-browsing mode")
		return projectBindingPickerResult{}, nil
	}
	runServerProjectPickerFlow = func(projects []clientui.ProjectSummary, theme string) (projectBindingPickerResult, error) {
		if len(projects) != 1 || projects[0].ProjectID != "project-1" {
			t.Fatalf("unexpected server projects: %+v", projects)
		}
		picked := projects[0]
		return projectBindingPickerResult{Project: &picked}, nil
	}
	runProjectWorkspacePickerFlow = func([]clientui.ProjectWorkspaceSummary, string) (projectWorkspacePickerResult, error) {
		t.Fatal("did not expect workspace picker for single workspace project")
		return projectWorkspacePickerResult{}, nil
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), "project-placeholder", "sessions"),
		projectViewClient: client.NewLoopbackProjectViewClient(service),
	}

	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	boundServer, ok := bound.(*testEmbeddedServer)
	if !ok {
		t.Fatalf("bound server type = %T, want *testEmbeddedServer", bound)
	}
	if got := boundServer.ProjectID(); got != "project-1" {
		t.Fatalf("bound project id = %q, want project-1", got)
	}
	if got := boundServer.boundWorkspaceID; got != "workspace-1" {
		t.Fatalf("bound workspace id = %q, want workspace-1", got)
	}
}

func TestEnsureInteractiveProjectBindingRebindsSameProjectToResolvedWorkspace(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	service := projectBindingFlowStubProjectViewService{
		resolveResp: serverapi.ProjectResolvePathResponse{
			CanonicalRoot: cfg.WorkspaceRoot,
			Binding: &serverapi.ProjectBinding{
				ProjectID:     "project-1",
				WorkspaceID:   "workspace-b",
				CanonicalRoot: cfg.WorkspaceRoot,
			},
		},
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), "project-1", "sessions"),
		projectID:         "project-1",
		boundWorkspaceID:  "workspace-a",
		projectViewClient: client.NewLoopbackProjectViewClient(service),
	}

	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	boundServer, ok := bound.(*testEmbeddedServer)
	if !ok {
		t.Fatalf("bound server type = %T, want *testEmbeddedServer", bound)
	}
	if got := boundServer.ProjectID(); got != "project-1" {
		t.Fatalf("bound project id = %q, want project-1", got)
	}
	if got := boundServer.boundWorkspaceID; got != "workspace-b" {
		t.Fatalf("bound workspace id = %q, want workspace-b", got)
	}
}

func TestEnsureInteractiveProjectBindingAttachesUnknownWorkspaceToExistingProject(t *testing.T) {
	newAppTestHome(t)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	cfgA := loadAppTestConfig(t, workspaceA, config.LoadOptions{})
	bindingA, err := metadata.RegisterBinding(context.Background(), cfgA.PersistenceRoot, cfgA.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding A: %v", err)
	}

	cfgB := loadAppTestConfig(t, workspaceB, config.LoadOptions{})
	projectViewClient := newAppMetadataProjectViewClient(t, cfgB)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func(projects []clientui.ProjectSummary, theme string) (projectBindingPickerResult, error) {
		if len(projects) != 1 || projects[0].ProjectID != bindingA.ProjectID {
			t.Fatalf("unexpected projects: %+v", projects)
		}
		picked := projects[0]
		return projectBindingPickerResult{Project: &picked}, nil
	}
	runProjectNamePromptFlow = func(string, string) (string, error) {
		t.Fatal("did not expect project name prompt when attaching to existing project")
		return "", nil
	}

	server := &testEmbeddedServer{
		cfg:               cfgB,
		containerDir:      filepath.Join(filepath.Join(cfgB.PersistenceRoot, "projects"), bindingA.ProjectID, "sessions"),
		projectViewClient: projectViewClient,
	}

	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	if bound.ProjectID() != bindingA.ProjectID {
		t.Fatalf("bound project id = %q, want %q", bound.ProjectID(), bindingA.ProjectID)
	}
	resolved, err := metadata.ResolveBinding(context.Background(), cfgB.PersistenceRoot, cfgB.WorkspaceRoot)
	if err != nil {
		t.Fatalf("ResolveBinding B: %v", err)
	}
	if resolved.ProjectID != bindingA.ProjectID {
		t.Fatalf("workspace B project id = %q, want %q", resolved.ProjectID, bindingA.ProjectID)
	}
}

func TestEnsureInteractiveProjectBindingFormatsMissingSelectedProjectError(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func([]clientui.ProjectSummary, string) (projectBindingPickerResult, error) {
		picked := clientui.ProjectSummary{ProjectID: "project-missing", DisplayName: "Missing Project"}
		return projectBindingPickerResult{Project: &picked}, nil
	}
	runProjectNamePromptFlow = func(string, string) (string, error) {
		t.Fatal("did not expect project name prompt when attaching to existing project")
		return "", nil
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), "project-placeholder", "sessions"),
		projectViewClient: projectViewClient,
	}

	_, err := ensureInteractiveProjectBinding(context.Background(), server)
	if !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("ensureInteractiveProjectBinding error = %v, want ErrProjectNotFound", err)
	}
	if got := err.Error(); !strings.Contains(got, "Restart Kent and choose another project") || !strings.Contains(got, "project-missing") {
		t.Fatalf("error = %q, want missing project picker guidance", got)
	}
}

func TestEnsureInteractiveProjectBindingReturnsCancelWhenPickerAborts(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func([]clientui.ProjectSummary, string) (projectBindingPickerResult, error) {
		return projectBindingPickerResult{Canceled: true}, nil
	}
	runProjectNamePromptFlow = func(string, string) (string, error) {
		t.Fatal("did not expect project name prompt after picker cancel")
		return "", nil
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), "project-placeholder", "sessions"),
		projectViewClient: projectViewClient,
	}

	if _, err := ensureInteractiveProjectBinding(context.Background(), server); err == nil || !errors.Is(err, projectbinding.ErrStartupCanceledByUser) {
		t.Fatalf("expected startup canceled error, got %v", err)
	}
	if _, err := metadata.ResolveBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("ResolveBinding after picker cancel = %v, want ErrWorkspaceNotRegistered", err)
	}
}

func TestEnsureInteractiveProjectBindingReturnsCancelWhenProjectNamingAborts(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func([]clientui.ProjectSummary, string) (projectBindingPickerResult, error) {
		return projectBindingPickerResult{CreateNew: true}, nil
	}
	runProjectNamePromptFlow = func(string, string) (string, error) {
		return "", context.Canceled
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), "project-placeholder", "sessions"),
		projectViewClient: projectViewClient,
	}

	if _, err := ensureInteractiveProjectBinding(context.Background(), server); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from project name prompt, got %v", err)
	}
	if _, err := metadata.ResolveBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("ResolveBinding after naming cancel = %v, want ErrWorkspaceNotRegistered", err)
	}
}

func TestEnsureInteractiveServerBrowsingBindingUsesConfiguredServerPickerNotice(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	service := projectBindingFlowStubProjectViewService{
		listProjectsResp: serverapi.ProjectListResponse{Projects: []clientui.ProjectSummary{{
			ProjectID:   "project-1",
			DisplayName: "Remote Project",
			RootPath:    "/srv/project",
		}}},
		projectOverviewResp: serverapi.ProjectGetOverviewResponse{Overview: clientui.ProjectOverview{
			Project: clientui.ProjectSummary{ProjectID: "project-1", DisplayName: "Remote Project", RootPath: "/srv/project"},
			Workspaces: []clientui.ProjectWorkspaceSummary{{
				WorkspaceID: "workspace-1",
				DisplayName: "Workspace 1",
				RootPath:    "/srv/project",
			}},
		}},
	}

	originalRemotePicker := runServerProjectPickerFlow
	originalWorkspacePicker := runProjectWorkspacePickerFlow
	t.Cleanup(func() {
		runServerProjectPickerFlow = originalRemotePicker
		runProjectWorkspacePickerFlow = originalWorkspacePicker
	})
	runServerProjectPickerFlow = func(projects []clientui.ProjectSummary, theme string) (projectBindingPickerResult, error) {
		model := newProjectBindingPickerModel(projects, theme, projectPickerOptions{
			AllowCreate:    false,
			HeaderMarkdown: serverProjectPickerHeaderMarkdown,
			HeaderFallback: serverProjectPickerHeaderFallback,
			NoticeText:     serverProjectPickerNoticeText,
			GroupLabel:     serverProjectExistingLabel,
		})
		model.width = 240
		model.height = 12
		if got := xansi.Strip(model.View()); !strings.Contains(got, "\n\n"+serverProjectPickerNoticeText+"\n\n") {
			t.Fatalf("server browsing picker notice missing or padded unexpectedly: %q", got)
		}
		picked := projects[0]
		return projectBindingPickerResult{Project: &picked}, nil
	}
	runProjectWorkspacePickerFlow = func([]clientui.ProjectWorkspaceSummary, string) (projectWorkspacePickerResult, error) {
		t.Fatal("did not expect workspace picker for single workspace project")
		return projectWorkspacePickerResult{}, nil
	}

	server := &testEmbeddedServer{
		cfg:               cfg,
		containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), "project-placeholder", "sessions"),
		projectViewClient: client.NewLoopbackProjectViewClient(service),
	}

	bound, err := ensureInteractiveServerBrowsingBinding(context.Background(), server, service.listProjectsResp.Projects)
	if err != nil {
		t.Fatalf("ensureInteractiveServerBrowsingBinding: %v", err)
	}
	if got := bound.ProjectID(); got != "project-1" {
		t.Fatalf("bound project id = %q, want project-1", got)
	}
}

func TestEnsureInteractiveProjectBindingFormatsMissingBoundProjectError(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	server := &failingBindProjectServer{
		testEmbeddedServer: &testEmbeddedServer{
			cfg:               cfg,
			containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
			projectViewClient: projectViewClient,
		},
		bindErr: fmt.Errorf("bind project: %w", serverapi.ErrProjectNotFound),
	}

	_, err = ensureInteractiveProjectBinding(context.Background(), server)
	if !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("ensureInteractiveProjectBinding error = %v, want ErrProjectNotFound", err)
	}
	if got := err.Error(); !strings.Contains(got, "attached to missing project") || !strings.Contains(got, binding.ProjectID) {
		t.Fatalf("error = %q, want missing project guidance", got)
	}
}

func TestEnsureInteractiveProjectBindingFormatsUnavailableBoundProjectError(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	server := &failingBindProjectServer{
		testEmbeddedServer: &testEmbeddedServer{
			cfg:               cfg,
			containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
			projectViewClient: projectViewClient,
		},
		bindErr: serverapi.ProjectUnavailableError{ProjectID: binding.ProjectID, RootPath: cfg.WorkspaceRoot, Availability: clientui.ProjectAvailabilityMissing},
	}

	_, err = ensureInteractiveProjectBinding(context.Background(), server)
	if !errors.Is(err, serverapi.ErrProjectUnavailable) {
		t.Fatalf("ensureInteractiveProjectBinding error = %v, want ErrProjectUnavailable", err)
	}
	if got := err.Error(); !strings.Contains(got, "missing") || !strings.Contains(got, "Rebind affected sessions") {
		t.Fatalf("error = %q, want missing-root recovery guidance", got)
	}
}

func TestEnsureInteractiveProjectBindingFormatsInaccessibleBoundProjectError(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	projectViewClient := newAppMetadataProjectViewClient(t, cfg)

	server := &failingBindProjectServer{
		testEmbeddedServer: &testEmbeddedServer{
			cfg:               cfg,
			containerDir:      filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
			projectViewClient: projectViewClient,
		},
		bindErr: serverapi.ProjectUnavailableError{ProjectID: binding.ProjectID, RootPath: cfg.WorkspaceRoot, Availability: clientui.ProjectAvailabilityInaccessible},
	}

	_, err = ensureInteractiveProjectBinding(context.Background(), server)
	if !errors.Is(err, serverapi.ErrProjectUnavailable) {
		t.Fatalf("ensureInteractiveProjectBinding error = %v, want ErrProjectUnavailable", err)
	}
	if got := err.Error(); !strings.Contains(got, "Restore access") || !strings.Contains(got, "inaccessible") || !strings.Contains(got, "rebind affected sessions") {
		t.Fatalf("error = %q, want inaccessible-root recovery guidance", got)
	}
}

type failingBindProjectServer struct {
	*testEmbeddedServer
	bindErr error
}

type projectBindingFlowStubProjectViewService struct {
	resolveResp         serverapi.ProjectResolvePathResponse
	resolveErr          error
	listProjectsResp    serverapi.ProjectListResponse
	listProjectsErr     error
	projectOverviewResp serverapi.ProjectGetOverviewResponse
	projectOverviewErr  error
}

func (s projectBindingFlowStubProjectViewService) ListProjects(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	return s.listProjectsResp, s.listProjectsErr
}

func (projectBindingFlowStubProjectViewService) ListProjectHome(context.Context, serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return serverapi.ProjectHomeListResponse{}, errors.New("unexpected ListProjectHome call")
}

func (s projectBindingFlowStubProjectViewService) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return s.resolveResp, s.resolveErr
}

func (s projectBindingFlowStubProjectViewService) PlanWorkspaceBinding(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	if s.resolveErr != nil {
		return serverapi.ProjectBindingPlanResponse{}, s.resolveErr
	}
	resp := serverapi.ProjectBindingPlanResponse{
		CanonicalRoot:    s.resolveResp.CanonicalRoot,
		PathAvailability: s.resolveResp.PathAvailability,
		Binding:          s.resolveResp.Binding,
		Projects:         s.listProjectsResp.Projects,
	}
	if s.resolveResp.Binding != nil {
		resp.Kind = serverapi.ProjectBindingPlanKindBound
		return resp, nil
	}
	if s.resolveResp.PathAvailability == clientui.ProjectAvailabilityMissing || s.resolveResp.PathAvailability == clientui.ProjectAvailabilityInaccessible {
		resp.Kind = serverapi.ProjectBindingPlanKindServerWorkspaceSelection
		return resp, s.listProjectsErr
	}
	resp.Kind = serverapi.ProjectBindingPlanKindLocalUnbound
	return resp, s.listProjectsErr
}

func (projectBindingFlowStubProjectViewService) CreateProject(context.Context, serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	return serverapi.ProjectCreateResponse{}, errors.New("unexpected CreateProject call")
}

func (projectBindingFlowStubProjectViewService) ListProjectWorkspaces(context.Context, serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return serverapi.ProjectWorkspaceListResponse{}, errors.New("unexpected ListProjectWorkspaces call")
}

func (projectBindingFlowStubProjectViewService) AttachWorkspaceToProject(context.Context, serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	return serverapi.ProjectAttachWorkspaceResponse{}, errors.New("unexpected AttachWorkspaceToProject call")
}

func (projectBindingFlowStubProjectViewService) RebindWorkspace(context.Context, serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	return serverapi.ProjectRebindWorkspaceResponse{}, errors.New("unexpected RebindWorkspace call")
}

func (s projectBindingFlowStubProjectViewService) GetProjectOverview(context.Context, serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	return s.projectOverviewResp, s.projectOverviewErr
}

func (projectBindingFlowStubProjectViewService) ListSessionPage(context.Context, serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	return serverapi.SessionPageResponse{}, nil
}

func (s *failingBindProjectServer) ProjectID() string { return "" }

func (s *failingBindProjectServer) BindProjectWorkspace(context.Context, string, string) (interactiveSessionServer, error) {
	if s.bindErr != nil {
		return nil, s.bindErr
	}
	return s.testEmbeddedServer, nil
}
