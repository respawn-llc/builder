package projectview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestServiceRejectsUnknownProjectID(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store, binding.ProjectID)
	if _, err := svc.GetProjectOverview(context.Background(), serverapi.ProjectGetOverviewRequest{ProjectID: "project-2"}); err == nil {
		t.Fatal("expected GetProjectOverview to reject unknown project")
	}
	if _, err := svc.ListSessionPage(context.Background(), serverapi.SessionPageRequest{
		ProjectID: "project-2",
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  20,
		Position:  serverapi.NewestSessionPagePosition(),
	}); err == nil {
		t.Fatal("expected ListSessionPage to reject unknown project")
	}
}

func TestServiceDeletesProjectMetadataAndSessionArtifacts(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	sessionDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	created, err := session.Create(sessionDir, filepath.Base(sessionDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := created.SetName("delete-me"); err != nil {
		t.Fatalf("persist session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created.Dir(), "events.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write session event: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, binding.ProjectID)

	deleted, err := svc.DeleteProject(context.Background(), serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("delete response = %+v, want deleted", deleted)
	}
	if _, err := os.Stat(created.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session dir stat = %v, want not exists", err)
	}
	if _, err := svc.GetProjectOverview(context.Background(), serverapi.ProjectGetOverviewRequest{ProjectID: binding.ProjectID}); err == nil {
		t.Fatal("expected deleted project lookup to fail")
	}
	if _, err := os.Stat(binding.CanonicalRoot); err != nil {
		t.Fatalf("workspace root should remain: %v", err)
	}
}

func TestServiceDeletesProjectWithBacklogTasks(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newProjectViewMetadataStore(t)
	workflowStore, err := workflowstore.New(store)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflow, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Backlog Board"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflow.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Backlog", Body: "Body"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, binding.ProjectID)

	deleted, err := svc.DeleteProject(ctx, serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("delete response = %+v, want deleted without backlog blockers", deleted)
	}
	if _, err := svc.GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: binding.ProjectID}); err == nil {
		t.Fatal("expected deleted backlog-only project lookup to fail")
	}
}

func TestServiceProjectDeleteRequiresWorkflowExecution(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc, err := NewMetadataService(store, binding.ProjectID)
	if err != nil {
		t.Fatalf("NewMetadataService: %v", err)
	}
	if _, err := svc.DeleteProject(context.Background(), serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID}); err == nil {
		t.Fatal("DeleteProject succeeded without the shared workflow execution permit")
	}
}

func TestServiceProjectDeleteRevalidatesWorkflowTasksAtCommit(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newProjectViewMetadataStore(t)
	workflowStore, err := workflowstore.New(store)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowRecord, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Quiescence Board"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowRecord.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Blocked", Body: "Body"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, binding.ProjectID)
	svc.workflowExecution = projectViewQuiescentExecution{err: workflowexecution.ErrTaskExecutionNotQuiescent}

	if _, err := svc.DeleteProject(ctx, serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID}); !errors.Is(err, workflowexecution.ErrTaskExecutionNotQuiescent) {
		t.Fatalf("DeleteProject error = %v, want %v", err, workflowexecution.ErrTaskExecutionNotQuiescent)
	}
	if _, err := svc.GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: binding.ProjectID}); err != nil {
		t.Fatalf("GetProjectOverview after rejected delete: %v", err)
	}
}

func TestServiceProjectDeleteWaitsForConcurrentWorkflowMutation(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store, binding.ProjectID)
	entered := make(chan struct{})
	release := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- svc.mutationPermit.Run(context.Background(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	deleted := make(chan error, 1)
	go func() {
		_, err := svc.DeleteProject(context.Background(), serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID})
		deleted <- err
	}()
	select {
	case err := <-deleted:
		t.Fatalf("DeleteProject escaped concurrent workflow mutation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-held; err != nil {
		t.Fatalf("concurrent workflow mutation: %v", err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
}

func TestServiceDeleteProjectBlocksActiveSession(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	sessionDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	created, err := session.Create(sessionDir, filepath.Base(sessionDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := created.SetName("active"); err != nil {
		t.Fatalf("persist session: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, binding.ProjectID)
	svc.WithRuntimeAuthority(newProjectViewActiveRuntimeAuthority(t, store, cfg, created))

	deleted, err := svc.DeleteProject(context.Background(), serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if deleted.Deleted || len(deleted.Blockers) != 1 || deleted.Blockers[0].Code != "active_sessions" {
		t.Fatalf("delete response = %+v, want active_sessions blocker", deleted)
	}
	if _, err := os.Stat(created.Dir()); err != nil {
		t.Fatalf("session dir should remain: %v", err)
	}
}

func TestServiceProjectBlockersIncludeRuntimeMaintenance(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	created := createProjectViewSession(t, store, cfg, binding.ProjectID, cfg.WorkspaceRoot, "maintenance")
	authority, sessionID, plan := newProjectViewRuntimeAuthority(t, store, cfg, created)
	if _, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "project-view-maintenance",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, binding.ProjectID).WithRuntimeAuthority(authority)

	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- authority.RunSessionMaintenance(
			context.Background(),
			sessionID.String(),
			func(context.Context, *session.Store, *sessionruntime.ActiveRuntimeMaintenance) error {
				close(started)
				<-release
				return nil
			},
		)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for project runtime maintenance")
	}

	blockers, err := svc.projectActiveSessionBlockers(context.Background(), []string{sessionID.String()})
	if err != nil {
		t.Fatalf("project active session blockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0].Code != "active_sessions" || blockers[0].Count != 1 {
		t.Fatalf("project blockers = %+v, want one active maintenance session", blockers)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("run project session maintenance: %v", err)
	}
}

func TestServiceProjectStartBlockExcludesRuntimeMaintenance(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	created := createProjectViewSession(t, store, cfg, binding.ProjectID, cfg.WorkspaceRoot, "maintenance-block")
	authority, sessionID, plan := newProjectViewRuntimeAuthority(t, store, cfg, created)
	if _, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "project-view-maintenance-block",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, binding.ProjectID).WithRuntimeAuthority(authority)
	release, err := svc.blockSessionStarts(context.Background(), []string{sessionID.String()})
	if err != nil {
		t.Fatalf("block project session starts: %v", err)
	}
	defer release()

	err = authority.RunSessionMaintenance(
		context.Background(),
		sessionID.String(),
		func(context.Context, *session.Store, *sessionruntime.ActiveRuntimeMaintenance) error {
			t.Fatal("project-blocked runtime maintenance callback ran")
			return nil
		},
	)
	if !errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
		t.Fatalf("project-blocked runtime maintenance error = %v, want ErrSessionStartsBlocked", err)
	}
}

func TestDeleteSessionArtifactRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "projects", "project-1"), 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "projects", "project-1", "sessions")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	err := deleteSessionArtifact(root, "project-1", filepath.Join("projects", "project-1", "sessions", "keep"), true)

	if err == nil || !errors.Is(err, ErrSessionArtifactEscapesRoot) {
		t.Fatalf("deleteSessionArtifact error = %v, want escape rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("outside file should remain: %v", err)
	}
}

func TestMetadataServiceSupportsWildcardAndScopedProjectListing(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	store, _, bindingA := newProjectViewMetadataStoreForWorkspace(t, workspaceA)

	cfgB, err := config.Load(workspaceB, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load workspace B: %v", err)
	}
	bindingB, err := store.RegisterWorkspaceBinding(context.Background(), cfgB.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding B: %v", err)
	}

	wildcard := newProjectViewMetadataService(t, store, "")
	projects, err := wildcard.ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("ListProjects wildcard: %v", err)
	}
	if len(projects.Projects) != 2 {
		t.Fatalf("expected wildcard metadata service to list both projects, got %+v", projects.Projects)
	}

	scoped := newProjectViewMetadataService(t, store, bindingA.ProjectID)
	projects, err = scoped.ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("ListProjects scoped: %v", err)
	}
	if len(projects.Projects) != 1 || projects.Projects[0].ProjectID != bindingA.ProjectID {
		t.Fatalf("expected scoped metadata service to list only project A, got %+v", projects.Projects)
	}
	if _, err := scoped.GetProjectOverview(context.Background(), serverapi.ProjectGetOverviewRequest{ProjectID: bindingB.ProjectID}); err == nil {
		t.Fatal("expected scoped metadata service to reject other project overview")
	}
}

func TestMetadataServiceCreatesProjectWithoutExplicitKey(t *testing.T) {
	store, _, _ := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store, "")

	created, err := svc.CreateProject(context.Background(), serverapi.ProjectCreateRequest{
		DisplayName:   "Default Key",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if created.Binding.ProjectKey == "" {
		t.Fatalf("expected generated project key, got %+v", created.Binding)
	}
}

func TestMetadataServicePaginatesProjectWorkspacesForGUI(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	first := attachProjectViewWorkspace(t, store, binding.ProjectID)
	second := attachProjectViewWorkspace(t, store, binding.ProjectID)
	svc := newProjectViewMetadataService(t, store, "")

	page1, err := svc.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{ProjectID: binding.ProjectID, PageSize: 2})
	if err != nil {
		t.Fatalf("ListProjectWorkspaces page1: %v", err)
	}
	if got := workspaceIDs(page1.Workspaces); len(got) != 2 || got[0] != binding.WorkspaceID || got[1] != second.WorkspaceID {
		t.Fatalf("page1 workspace ids = %+v, want [%s %s]", got, binding.WorkspaceID, second.WorkspaceID)
	}
	if page1.NextPageToken == "" {
		t.Fatalf("page1 next token empty: %+v", page1)
	}

	page2, err := svc.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{ProjectID: binding.ProjectID, PageSize: 2, PageToken: page1.NextPageToken})
	if err != nil {
		t.Fatalf("ListProjectWorkspaces page2: %v", err)
	}
	if got := workspaceIDs(page2.Workspaces); len(got) != 1 || got[0] != first.WorkspaceID {
		t.Fatalf("page2 workspace ids = %+v, want [%s]", got, first.WorkspaceID)
	}
	if page2.NextPageToken != "" {
		t.Fatalf("page2 next token = %q, want empty", page2.NextPageToken)
	}
}

func TestMetadataServiceUpdatesProjectNameForEditPage(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store, "")

	updated, err := svc.UpdateProject(context.Background(), serverapi.ProjectUpdateRequest{
		ProjectID:   binding.ProjectID,
		DisplayName: "Edited project",
	})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if updated.Project.ProjectID != binding.ProjectID {
		t.Fatalf("updated project id = %q, want %q", updated.Project.ProjectID, binding.ProjectID)
	}
	if updated.Project.DisplayName != "Edited project" {
		t.Fatalf("updated display name = %q, want Edited project", updated.Project.DisplayName)
	}

	home, err := svc.ListProjectHome(context.Background(), serverapi.ProjectHomeListRequest{PageSize: 10})
	if err != nil {
		t.Fatalf("ListProjectHome: %v", err)
	}
	if len(home.Projects) != 1 || home.Projects[0].DisplayName != "Edited project" {
		t.Fatalf("home projects = %+v, want edited name", home.Projects)
	}
}

func TestMetadataServiceUpdatesProjectKeyForEditPage(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store, "")

	updated, err := svc.UpdateProject(context.Background(), serverapi.ProjectUpdateRequest{
		ProjectID:   binding.ProjectID,
		DisplayName: "Renamed key project",
		ProjectKey:  "ABC",
	})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if updated.Project.ProjectKey != "ABC" {
		t.Fatalf("updated project key = %q, want ABC", updated.Project.ProjectKey)
	}

	// An empty project key leaves the existing key unchanged.
	unchanged, err := svc.UpdateProject(context.Background(), serverapi.ProjectUpdateRequest{
		ProjectID:   binding.ProjectID,
		DisplayName: "Renamed key project again",
	})
	if err != nil {
		t.Fatalf("UpdateProject without key: %v", err)
	}
	if unchanged.Project.ProjectKey != "ABC" {
		t.Fatalf("project key after empty update = %q, want ABC", unchanged.Project.ProjectKey)
	}
}

func TestMetadataServiceSetsDefaultWorkspaceForEditPage(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	attached := attachProjectViewWorkspace(t, store, binding.ProjectID)
	svc := newProjectViewMetadataService(t, store, "")

	updated, err := svc.SetDefaultWorkspace(context.Background(), serverapi.ProjectDefaultWorkspaceSetRequest{
		ProjectID:   binding.ProjectID,
		WorkspaceID: attached.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("SetDefaultWorkspace: %v", err)
	}
	if updated.Project.PrimaryWorkspace.WorkspaceID != attached.WorkspaceID {
		t.Fatalf("updated primary workspace = %+v, want %q", updated.Project.PrimaryWorkspace, attached.WorkspaceID)
	}

	list, err := svc.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("ListProjectWorkspaces: %v", err)
	}
	if list.DefaultWorkspaceID != attached.WorkspaceID {
		t.Fatalf("default workspace = %q, want %q", list.DefaultWorkspaceID, attached.WorkspaceID)
	}
}

func TestMetadataServiceUnlinksWorkspaceForEditPage(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	attached := attachProjectViewWorkspace(t, store, binding.ProjectID)
	svc := newProjectViewMetadataService(t, store, "")

	blocked, err := svc.UnlinkWorkspaceFromProject(context.Background(), serverapi.ProjectWorkspaceUnlinkRequest{
		ProjectID:   binding.ProjectID,
		WorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("UnlinkWorkspaceFromProject blocked: %v", err)
	}
	if blocked.Unlinked || !hasWorkspaceUnlinkBlocker(blocked.Blockers, "default_workspace") {
		t.Fatalf("blocked unlink = %+v, want default workspace blocker", blocked)
	}

	unlinked, err := svc.UnlinkWorkspaceFromProject(context.Background(), serverapi.ProjectWorkspaceUnlinkRequest{
		ProjectID:   binding.ProjectID,
		WorkspaceID: attached.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("UnlinkWorkspaceFromProject: %v", err)
	}
	if !unlinked.Unlinked || len(unlinked.Blockers) != 0 {
		t.Fatalf("unlink result = %+v, want success", unlinked)
	}
	list, err := svc.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("ListProjectWorkspaces: %v", err)
	}
	if len(list.Workspaces) != 1 || list.Workspaces[0].WorkspaceID != binding.WorkspaceID {
		t.Fatalf("workspaces after unlink = %+v, want only default", list.Workspaces)
	}
}

func TestMetadataServiceUnlinkWorkspaceBlocksActiveRuntimeSession(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	attached := attachProjectViewWorkspace(t, store, binding.ProjectID)
	created := createProjectViewSession(t, store, cfg, binding.ProjectID, attached.CanonicalRoot, "active-workspace")
	svc := newProjectViewMetadataService(t, store, binding.ProjectID)
	svc.WithRuntimeAuthority(newProjectViewActiveRuntimeAuthority(t, store, cfg, created))

	unlinked, err := svc.UnlinkWorkspaceFromProject(context.Background(), serverapi.ProjectWorkspaceUnlinkRequest{
		ProjectID:   binding.ProjectID,
		WorkspaceID: attached.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("UnlinkWorkspaceFromProject: %v", err)
	}
	if unlinked.Unlinked || len(unlinked.Blockers) != 1 || unlinked.Blockers[0].Code != "active_sessions" {
		t.Fatalf("unlink response = %+v, want active_sessions blocker", unlinked)
	}
}

func TestMetadataServiceGetsProjectEditForGUI(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	attachProjectViewWorkspace(t, store, binding.ProjectID)
	svc := newProjectViewMetadataService(t, store, "")

	edit, err := svc.GetProjectEdit(context.Background(), serverapi.ProjectEditGetRequest{ProjectID: binding.ProjectID, PageSize: 1})
	if err != nil {
		t.Fatalf("GetProjectEdit: %v", err)
	}
	if edit.ProjectID != binding.ProjectID || edit.ProjectKey != binding.ProjectKey || edit.DisplayName != binding.ProjectName {
		t.Fatalf("edit identity = %+v, want %s/%s/%s", edit, binding.ProjectID, binding.ProjectKey, binding.ProjectName)
	}
	if edit.DefaultWorkspaceID != binding.WorkspaceID {
		t.Fatalf("default workspace = %q, want %q", edit.DefaultWorkspaceID, binding.WorkspaceID)
	}
	if got := workspaceIDs(edit.Workspaces); len(got) != 1 || got[0] != binding.WorkspaceID {
		t.Fatalf("edit page1 workspaces = %+v, want default workspace %q", got, binding.WorkspaceID)
	}
	if edit.NextPageToken == "" {
		t.Fatalf("edit next token empty: %+v", edit)
	}
}

func TestMetadataServiceListsProjectHomeForGUI(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store, "")
	created, err := svc.CreateProject(context.Background(), serverapi.ProjectCreateRequest{
		DisplayName:   "GUI Home",
		ProjectKey:    "HOME",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	workflowStore, err := workflowstore.New(store)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflow, err := workflowStore.CreateWorkflow(context.Background(), workflowstore.CreateWorkflowRequest{Name: "Default Board"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(context.Background(), created.Binding.ProjectID, workflow.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}

	firstPage, err := svc.ListProjectHome(context.Background(), serverapi.ProjectHomeListRequest{PageSize: 1})
	if err != nil {
		t.Fatalf("ListProjectHome first page: %v", err)
	}
	if len(firstPage.Projects) != 1 {
		t.Fatalf("first page count = %d, want 1: %+v", len(firstPage.Projects), firstPage.Projects)
	}
	if firstPage.NextPageToken == "" {
		t.Fatalf("expected next page token: %+v", firstPage)
	}
	first := firstPage.Projects[0]
	if first.ProjectID != created.Binding.ProjectID || first.ProjectKey != "HOME" {
		t.Fatalf("first project = %+v, want created HOME project", first)
	}
	if first.PrimaryWorkspace.WorkspaceID != created.Binding.WorkspaceID || !first.PrimaryWorkspace.IsPrimary {
		t.Fatalf("primary workspace = %+v, want %q", first.PrimaryWorkspace, created.Binding.WorkspaceID)
	}
	if first.DefaultWorkflowID != string(workflow.ID) || first.DefaultWorkflowName != "Default Board" || !first.DefaultWorkflowValid {
		t.Fatalf("default workflow = %+v, want linked workflow %s", first, workflow.ID)
	}
	if first.WorkflowCount != 1 {
		t.Fatalf("workflow count = %d, want 1", first.WorkflowCount)
	}
	if first.AttentionCount != 0 {
		t.Fatalf("attention count = %d, want 0", first.AttentionCount)
	}
	if firstPage.GeneratedAtUnixMs <= 0 {
		t.Fatalf("generated_at_unix_ms = %d, want positive", firstPage.GeneratedAtUnixMs)
	}

	secondPage, err := svc.ListProjectHome(context.Background(), serverapi.ProjectHomeListRequest{PageSize: 1, PageToken: firstPage.NextPageToken})
	if err != nil {
		t.Fatalf("ListProjectHome second page: %v", err)
	}
	if len(secondPage.Projects) != 1 {
		t.Fatalf("second page count = %d, want 1: %+v", len(secondPage.Projects), secondPage.Projects)
	}
	second := secondPage.Projects[0]
	if second.ProjectID != binding.ProjectID {
		t.Fatalf("second project = %+v, want initial project %s", second, binding.ProjectID)
	}
	if second.DefaultWorkflowValid || second.DefaultWorkflowID != "" || second.DefaultWorkflowName != "" {
		t.Fatalf("empty default workflow = %+v, want invalid empty default workflow", second)
	}
	if _, err := svc.ListProjectHome(context.Background(), serverapi.ProjectHomeListRequest{PageToken: "bad"}); err == nil {
		t.Fatal("expected invalid page token error")
	}
}

func TestMetadataServiceListsScopedProjectHomeBeforePagination(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	unscoped := newProjectViewMetadataService(t, store, "")
	created, err := unscoped.CreateProject(context.Background(), serverapi.ProjectCreateRequest{
		DisplayName:   "Newer GUI Home",
		ProjectKey:    "NEW",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	scoped := newProjectViewMetadataService(t, store, binding.ProjectID)

	home, err := scoped.ListProjectHome(context.Background(), serverapi.ProjectHomeListRequest{PageSize: 1})
	if err != nil {
		t.Fatalf("ListProjectHome scoped: %v", err)
	}
	if len(home.Projects) != 1 || home.Projects[0].ProjectID != binding.ProjectID || home.NextPageToken != "" {
		t.Fatalf("scoped home = %+v, created project %s should not displace scoped project", home, created.Binding.ProjectID)
	}
}

func TestMetadataServiceResolveProjectPathLeavesNestedDirectoryUnbound(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}
	store, _, _ := newProjectViewMetadataStoreForWorkspace(t, workspace)
	svc := newProjectViewMetadataService(t, store, "")

	resolved, err := svc.ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: nested})
	if err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	if resolved.Binding != nil {
		t.Fatalf("expected nested path to remain unbound, got %+v", resolved.Binding)
	}
}

func TestMetadataServiceResolveProjectPathMapsRegisteredWorktreeRootToProject(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree root: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot worktree: %v", err)
	}
	if err := store.UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID:            "worktree-task",
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: canonicalWorktreeRoot,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, "")

	resolved, err := svc.ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: worktreeRoot})
	if err != nil {
		t.Fatalf("ResolveProjectPath worktree root: %v", err)
	}
	if resolved.CanonicalRoot != canonicalWorktreeRoot {
		t.Fatalf("canonical root = %q, want worktree root %q", resolved.CanonicalRoot, canonicalWorktreeRoot)
	}
	if resolved.Binding == nil || resolved.Binding.ProjectID != binding.ProjectID || resolved.Binding.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("resolved binding = %+v, want owning project/workspace %+v", resolved.Binding, binding)
	}
}

func TestMetadataServicePlansInteractiveLocalUnboundWorkspace(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	workspace := t.TempDir()
	svc := newProjectViewMetadataService(t, store, "")

	plan, err := svc.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: workspace, Mode: serverapi.ProjectBindingPlanModeInteractive})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindLocalUnbound {
		t.Fatalf("plan kind = %q, want %q", plan.Kind, serverapi.ProjectBindingPlanKindLocalUnbound)
	}
	if len(plan.Projects) != 1 || plan.Projects[0].ProjectID != binding.ProjectID {
		t.Fatalf("plan projects = %+v, want registered project %q", plan.Projects, binding.ProjectID)
	}
}

func TestMetadataServicePlansAmbiguousDuplicateWorkspaceBinding(t *testing.T) {
	store, cfg, first := newProjectViewMetadataStore(t)
	second, err := store.CreateProjectForWorkspace(context.Background(), cfg.WorkspaceRoot, "second")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace second: %v", err)
	}
	svc := newProjectViewMetadataService(t, store, "")

	if _, err := svc.ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: cfg.WorkspaceRoot}); !errors.Is(err, serverapi.ErrWorkspaceBindingAmbiguous) {
		t.Fatalf("ResolveProjectPath duplicate binding error = %v, want ErrWorkspaceBindingAmbiguous", err)
	}
	plan, err := svc.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: cfg.WorkspaceRoot, Mode: serverapi.ProjectBindingPlanModeInteractive})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding interactive duplicate: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindServerWorkspaceSelection {
		t.Fatalf("interactive plan kind = %q, want %q", plan.Kind, serverapi.ProjectBindingPlanKindServerWorkspaceSelection)
	}
	if len(plan.Projects) != 2 {
		t.Fatalf("interactive plan projects = %+v, want two projects", plan.Projects)
	}
	if plan.CanonicalRoot != first.CanonicalRoot || second.CanonicalRoot != first.CanonicalRoot {
		t.Fatalf("duplicate canonical roots = %q/%q, want %q", first.CanonicalRoot, second.CanonicalRoot, cfg.WorkspaceRoot)
	}

	plan, err = svc.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: cfg.WorkspaceRoot, Mode: serverapi.ProjectBindingPlanModeHeadless})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding headless duplicate: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous {
		t.Fatalf("headless plan kind = %q, want %q", plan.Kind, serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous)
	}
}

func TestMetadataServicePlansHeadlessSingleRemoteWorkspace(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store, "")

	plan, err := svc.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: filepath.Join(t.TempDir(), "missing"), Mode: serverapi.ProjectBindingPlanModeHeadless})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindHeadlessRemoteSelected || plan.Workspace == nil {
		t.Fatalf("plan = %+v, want selected remote workspace", plan)
	}
	if plan.Workspace.ProjectID != binding.ProjectID || plan.Workspace.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("selected workspace = %+v, want %s/%s", plan.Workspace, binding.ProjectID, binding.WorkspaceID)
	}
}

func TestMetadataServicePlansHeadlessAmbiguousRemoteWorkspaces(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	attachProjectViewWorkspace(t, store, binding.ProjectID)
	svc := newProjectViewMetadataService(t, store, "")

	plan, err := svc.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: filepath.Join(t.TempDir(), "missing"), Mode: serverapi.ProjectBindingPlanModeHeadless})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous {
		t.Fatalf("plan kind = %q, want %q", plan.Kind, serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous)
	}
}

func newProjectViewMetadataService(t testing.TB, store *metadata.Store, projectID string) *Service {
	t.Helper()
	svc, err := NewMetadataService(store, projectID)
	if err != nil {
		t.Fatalf("NewMetadataService: %v", err)
	}
	return svc.WithWorkflowExecution(workflowexecution.NewMutationPermit(), projectViewQuiescentExecution{})
}

type projectViewQuiescentExecution struct {
	err error
}

func (e projectViewQuiescentExecution) EnsureTaskQuiescent(workflow.TaskID) error {
	return e.err
}

func createProjectViewSession(t testing.TB, store *metadata.Store, cfg config.App, projectID string, workspaceRoot string, name string) *session.Store {
	t.Helper()
	sessionDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), projectID, "sessions")
	created, err := session.Create(sessionDir, filepath.Base(sessionDir), workspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := created.SetName(name); err != nil {
		t.Fatalf("persist session: %v", err)
	}
	return created
}

type projectViewTestLLMClient struct{}

func (projectViewTestLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func newProjectViewActiveRuntimeAuthority(t testing.TB, store *metadata.Store, cfg config.App, sessionStore *session.Store) *sessionruntime.Authority {
	t.Helper()
	authority, sessionID, plan := newProjectViewRuntimeAuthority(t, store, cfg, sessionStore)
	if _, err := authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenProjectViewSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
	}); err != nil {
		t.Fatalf("start active session execution: %v", err)
	}
	if _, active := authority.SessionExecution(sessionID); !active {
		t.Fatal("session execution was not registered as active")
	}
	return authority
}

func newProjectViewRuntimeAuthority(
	t testing.TB,
	store *metadata.Store,
	cfg config.App,
	sessionStore *session.Store,
) (*sessionruntime.Authority, runtimeids.SessionID, sessionruntime.AgentRuntimePlan) {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    store.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})

	settings := cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  sessionStore.Meta().WorkspaceRoot,
		Client:   projectViewTestLLMClient{},
	})
	if err != nil {
		t.Fatalf("new runtime plan: %v", err)
	}
	return authority, sessionID, plan
}

func mustOpenProjectViewSessionDescriptor(t testing.TB, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	return descriptor
}

func newProjectViewMetadataStore(t testing.TB) (*metadata.Store, config.App, metadata.Binding) {
	t.Helper()
	return newProjectViewMetadataStoreForWorkspace(t, t.TempDir())
}

func newProjectViewMetadataStoreForWorkspace(t testing.TB, workspace string) (*metadata.Store, config.App, metadata.Binding) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	store, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	return store, cfg, binding
}

func attachProjectViewWorkspace(t testing.TB, store *metadata.Store, projectID string) metadata.Binding {
	t.Helper()
	binding, err := store.AttachWorkspaceToProject(context.Background(), projectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	return binding
}

func workspaceIDs(workspaces []serverapi.ProjectWorkspaceSummary) []string {
	out := make([]string, 0, len(workspaces))
	for _, workspace := range workspaces {
		out = append(out, workspace.WorkspaceID)
	}
	return out
}

func hasWorkspaceUnlinkBlocker(blockers []serverapi.ProjectWorkspaceUnlinkBlocker, code string) bool {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}
