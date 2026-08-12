package projectview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

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
	svc := newProjectViewMetadataService(t, store)

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
	svc := newProjectViewMetadataService(t, store)

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
	svc := newProjectViewMetadataService(t, store)
	svc.workflowExecution = projectViewQuiescentExecution{err: workflowexecution.ErrTaskExecutionNotQuiescent}

	if _, err := svc.DeleteProject(ctx, serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID}); !errors.Is(err, workflowexecution.ErrTaskExecutionNotQuiescent) {
		t.Fatalf("DeleteProject error = %v, want %v", err, workflowexecution.ErrTaskExecutionNotQuiescent)
	}
	if _, err := svc.GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: binding.ProjectID}); err != nil {
		t.Fatalf("GetProjectOverview after rejected delete: %v", err)
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
	svc := newProjectViewMetadataService(t, store)
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

func TestServiceProjectDeleteSurfacesArtifactCleanupFailureAfterCommit(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	projectRoot := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID)
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(projectRoot, "sessions")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	svc := newProjectViewMetadataService(t, store)

	_, err := svc.DeleteProject(context.Background(), serverapi.ProjectDeleteRequest{ProjectID: binding.ProjectID})

	if err == nil || !errors.Is(err, ErrSessionArtifactEscapesRoot) {
		t.Fatalf("DeleteProject error = %v, want cleanup escape rejection", err)
	}
	if _, err := svc.GetProjectOverview(context.Background(), serverapi.ProjectGetOverviewRequest{ProjectID: binding.ProjectID}); err == nil {
		t.Fatal("project metadata remained after post-commit cleanup failure")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("outside file should remain: %v", err)
	}
}

func TestDeleteProjectSessionArtifactsRejectsRelativeProjectIDs(t *testing.T) {
	persistenceRoot := t.TempDir()
	sharedSessionsRoot := filepath.Join(persistenceRoot, "sessions")
	if err := os.MkdirAll(sharedSessionsRoot, 0o755); err != nil {
		t.Fatalf("create shared sessions root: %v", err)
	}
	marker := filepath.Join(sharedSessionsRoot, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write shared sessions marker: %v", err)
	}

	for _, projectID := range []string{".", ".."} {
		t.Run(projectID, func(t *testing.T) {
			if err := deleteProjectSessionArtifacts(persistenceRoot, projectID); err == nil {
				t.Fatalf("deleteProjectSessionArtifacts(%q) succeeded", projectID)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("shared sessions marker changed: %v", err)
			}
		})
	}
}

func TestMetadataServicePaginatesProjectWorkspaceCatalog(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	first := attachProjectViewWorkspace(t, store, binding.ProjectID)
	time.Sleep(2 * time.Millisecond)
	second := attachProjectViewWorkspace(t, store, binding.ProjectID)
	svc := newProjectViewMetadataService(t, store)

	page1, err := svc.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{
		ProjectID: binding.ProjectID,
		Offset:    0,
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("ListProjectWorkspaces page1: %v", err)
	}
	if got := catalogWorkspaceIDs(page1.Workspaces); len(got) != 2 || got[0] != binding.WorkspaceID || got[1] != second.WorkspaceID {
		t.Fatalf("page1 workspace ids = %+v, want [%s %s]", got, binding.WorkspaceID, second.WorkspaceID)
	}
	if !page1.Workspaces[0].IsDefault || page1.Workspaces[1].IsDefault {
		t.Fatalf("page1 default markers = %+v, want only first row default", page1.Workspaces)
	}
	if page1.NextOffset == nil || *page1.NextOffset != 2 {
		t.Fatalf("page1 next offset = %v, want 2", page1.NextOffset)
	}

	page2, err := svc.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{
		ProjectID: binding.ProjectID,
		Offset:    *page1.NextOffset,
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("ListProjectWorkspaces page2: %v", err)
	}
	if got := catalogWorkspaceIDs(page2.Workspaces); len(got) != 1 || got[0] != first.WorkspaceID {
		t.Fatalf("page2 workspace ids = %+v, want [%s]", got, first.WorkspaceID)
	}
	if page2.NextOffset != nil {
		t.Fatalf("page2 next offset = %v, want nil", page2.NextOffset)
	}
}

func TestMetadataServiceSetsDefaultWorkspaceByProjectScopedPath(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	second, err := store.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Second project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	shared, err := store.AttachWorkspaceToProject(context.Background(), second.ProjectID, binding.CanonicalRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject shared path: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(binding.CanonicalRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}

	svc := newProjectViewMetadataService(t, store)
	updated, err := svc.SetDefaultWorkspace(context.Background(), serverapi.ProjectDefaultWorkspaceSetRequest{
		ProjectID:                second.ProjectID,
		ProjectWorkspaceSelector: selector,
	})
	if err != nil {
		t.Fatalf("SetDefaultWorkspace: %v", err)
	}
	if updated.Project.PrimaryWorkspace.WorkspaceID != shared.WorkspaceID {
		t.Fatalf("updated primary workspace = %+v, want %q", updated.Project.PrimaryWorkspace, shared.WorkspaceID)
	}
}

func TestMetadataServiceDefaultWorkspaceSelectorPreservesTrueNoOp(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store)
	before, err := store.GetProjectHomeSummary(context.Background(), binding.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectHomeSummary before: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForID(binding.WorkspaceID)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	if _, err := svc.SetDefaultWorkspace(context.Background(), serverapi.ProjectDefaultWorkspaceSetRequest{
		ProjectID:                binding.ProjectID,
		ProjectWorkspaceSelector: selector,
	}); err != nil {
		t.Fatalf("SetDefaultWorkspace no-op: %v", err)
	}
	after, err := store.GetProjectHomeSummary(context.Background(), binding.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectHomeSummary after: %v", err)
	}
	if after.UpdatedAtUnixMs != before.UpdatedAtUnixMs {
		t.Fatalf("no-op changed updated_at from %d to %d", before.UpdatedAtUnixMs, after.UpdatedAtUnixMs)
	}
}

func TestMetadataServiceWorkspaceSelectorDistinguishesProjectAndBindingFailures(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store)

	unknownProjectSelector, err := serverapi.NewProjectWorkspaceSelectorForID(binding.WorkspaceID)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	if _, err := svc.SetDefaultWorkspace(context.Background(), serverapi.ProjectDefaultWorkspaceSetRequest{
		ProjectID:                "project-missing",
		ProjectWorkspaceSelector: unknownProjectSelector,
	}); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("unknown project error = %v, want ErrProjectNotFound", err)
	}

	unattachedSelector, err := serverapi.NewProjectWorkspaceSelectorForRoot(filepath.Join(t.TempDir(), "unattached"))
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	if _, err := svc.SetDefaultWorkspace(context.Background(), serverapi.ProjectDefaultWorkspaceSetRequest{
		ProjectID:                binding.ProjectID,
		ProjectWorkspaceSelector: unattachedSelector,
	}); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("unattached workspace error = %v, want ErrWorkspaceNotRegistered", err)
	}
}

func TestMetadataServiceRepeatedAttachReturnsTypedAlreadyAttachedOutcome(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store)
	selector, err := serverapi.NewProjectWorkspaceSelectorForID(binding.WorkspaceID)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	exact, err := svc.GetProjectWorkspace(context.Background(), serverapi.ProjectWorkspaceGetRequest{
		ProjectID: binding.ProjectID, ProjectWorkspaceSelector: selector,
	})
	if err != nil || exact.Result != serverapi.ProjectWorkspaceGetResultAttached ||
		exact.Workspace == nil || exact.Workspace.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("exact Workspace response = %+v, error = %v", exact, err)
	}
	root := t.TempDir()

	firstAttach, err := svc.AttachWorkspaceToProject(context.Background(), serverapi.ProjectAttachWorkspaceRequest{
		ProjectID:     binding.ProjectID,
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("first AttachWorkspaceToProject: %v", err)
	}
	if firstAttach.Outcome != serverapi.ProjectWorkspaceAttachOutcomeAttached {
		t.Fatalf("first attach outcome = %q, want attached", firstAttach.Outcome)
	}
	repeated, err := svc.AttachWorkspaceToProject(context.Background(), serverapi.ProjectAttachWorkspaceRequest{
		ProjectID:     binding.ProjectID,
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("repeated AttachWorkspaceToProject: %v", err)
	}
	if repeated.Outcome != serverapi.ProjectWorkspaceAttachOutcomeAlreadyAttached {
		t.Fatalf("repeated attach outcome = %q, want already_attached", repeated.Outcome)
	}
}

func TestMetadataServiceWorkspaceSelectorCanonicalizesMissingPath(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing", "nested")
	store, _, binding := newProjectViewMetadataStoreForWorkspace(t, missingRoot)
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(missingRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	resolved, err := store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, selector)
	if err != nil {
		t.Fatalf("ResolveProjectWorkspaceSelector: %v", err)
	}
	if resolved.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("resolved workspace id = %q, want %q", resolved.WorkspaceID, binding.WorkspaceID)
	}
}

func TestMetadataServiceWorkspaceSelectorCanonicalizesMissingPathThroughSymlinkedAncestor(t *testing.T) {
	realParent := t.TempDir()
	symlinkParent := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		t.Fatalf("create symlinked workspace parent: %v", err)
	}
	workspaceRoot := filepath.Join(symlinkParent, "repo")
	if err := os.Mkdir(workspaceRoot, 0o755); err != nil {
		t.Fatalf("create workspace root: %v", err)
	}

	store, _, binding := newProjectViewMetadataStoreForWorkspace(t, workspaceRoot)
	if err := os.Remove(workspaceRoot); err != nil {
		t.Fatalf("remove workspace root: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(workspaceRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	resolved, err := store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, selector)
	if err != nil {
		t.Fatalf("ResolveProjectWorkspaceSelector: %v", err)
	}
	if resolved.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("resolved workspace id = %q, want %q", resolved.WorkspaceID, binding.WorkspaceID)
	}
}

func TestMetadataServiceWorkspaceSelectorRejectsDanglingSymlinkAncestor(t *testing.T) {
	realParent := t.TempDir()
	symlinkParent := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		t.Fatalf("create symlinked workspace parent: %v", err)
	}
	workspaceRoot := filepath.Join(symlinkParent, "repo")
	if err := os.Mkdir(workspaceRoot, 0o755); err != nil {
		t.Fatalf("create workspace root: %v", err)
	}

	store, _, binding := newProjectViewMetadataStoreForWorkspace(t, workspaceRoot)
	if err := os.Remove(workspaceRoot); err != nil {
		t.Fatalf("remove workspace root: %v", err)
	}
	if err := os.Remove(realParent); err != nil {
		t.Fatalf("remove symlink target: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(workspaceRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	_, err = store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, selector)
	if !errors.Is(err, serverapi.ErrWorkspacePathIdentity) {
		t.Fatalf("dangling symlink selector error = %v, want ErrWorkspacePathIdentity", err)
	}
}

func TestMetadataServiceWorkspaceSelectorRejectsDanglingSelectedSymlink(t *testing.T) {
	targetRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(targetRoot, 0o755); err != nil {
		t.Fatalf("create workspace root: %v", err)
	}
	selectedRoot := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(targetRoot, selectedRoot); err != nil {
		t.Fatalf("create selected workspace symlink: %v", err)
	}

	store, _, binding := newProjectViewMetadataStoreForWorkspace(t, selectedRoot)
	if err := os.Remove(targetRoot); err != nil {
		t.Fatalf("remove symlink target: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(selectedRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	_, err = store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, selector)
	if !errors.Is(err, serverapi.ErrWorkspacePathIdentity) {
		t.Fatalf("dangling selected symlink error = %v, want ErrWorkspacePathIdentity", err)
	}
}

func TestMetadataServiceWorkspaceSelectorRejectsStaleLexicalBindingAfterSymlinkReplacement(t *testing.T) {
	originalRoot := t.TempDir()
	store, _, binding := newProjectViewMetadataStoreForWorkspace(t, originalRoot)
	replacementRoot := t.TempDir()
	if err := os.Remove(originalRoot); err != nil {
		t.Fatalf("remove original workspace root: %v", err)
	}
	if err := os.Symlink(replacementRoot, originalRoot); err != nil {
		t.Fatalf("replace workspace root with symlink: %v", err)
	}

	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(originalRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	_, err = store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, selector)
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("stale lexical selector error = %v, want ErrWorkspaceNotRegistered", err)
	}
	if _, err := store.LookupWorkspaceBindingByID(context.Background(), binding.WorkspaceID); err != nil {
		t.Fatalf("original binding was removed or became unreadable: %v", err)
	}
}

func TestMetadataServiceWorkspaceSelectorRejectsStaleLexicalBindingAfterDanglingSymlinkReplacement(t *testing.T) {
	originalRoot := t.TempDir()
	store, _, binding := newProjectViewMetadataStoreForWorkspace(t, originalRoot)
	if err := os.Remove(originalRoot); err != nil {
		t.Fatalf("remove original workspace root: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), originalRoot); err != nil {
		t.Fatalf("replace workspace root with dangling symlink: %v", err)
	}

	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(originalRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	_, err = store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, selector)
	if !errors.Is(err, serverapi.ErrWorkspacePathIdentity) {
		t.Fatalf("stale lexical dangling symlink error = %v, want ErrWorkspacePathIdentity", err)
	}
}

func TestMetadataServiceWorkspaceSelectorUsesExactRootFallbackWhenCanonicalizationIsInaccessible(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	loopRoot := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink("loop", loopRoot); err != nil {
		t.Fatalf("create symlink loop: %v", err)
	}
	if _, err := store.Queries().UpdateWorkspaceBindingCanonicalRoot(context.Background(), sqlitegen.UpdateWorkspaceBindingCanonicalRootParams{
		CanonicalRootPath: loopRoot,
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		ID:                binding.WorkspaceID,
	}); err != nil {
		t.Fatalf("update workspace root fixture: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(loopRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	resolved, err := store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, selector)
	if err != nil {
		t.Fatalf("ResolveProjectWorkspaceSelector: %v", err)
	}
	if resolved.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("resolved workspace id = %q, want %q", resolved.WorkspaceID, binding.WorkspaceID)
	}

	unknownLoopRoot := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink("loop", unknownLoopRoot); err != nil {
		t.Fatalf("create unknown symlink loop: %v", err)
	}
	unknownSelector, err := serverapi.NewProjectWorkspaceSelectorForRoot(unknownLoopRoot)
	if err != nil {
		t.Fatalf("unknown workspace selector: %v", err)
	}
	_, err = store.ResolveProjectWorkspaceSelector(context.Background(), binding.ProjectID, unknownSelector)
	if !errors.Is(err, serverapi.ErrWorkspacePathIdentity) {
		t.Fatalf("unknown inaccessible path error = %v, want ErrWorkspacePathIdentity", err)
	}
}

func TestServiceListsSessionPageWithOffsetWindow(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store)
	offset := 0
	limit := 50

	response, err := svc.ListSessionPage(context.Background(), serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Offset:    &offset,
		Limit:     &limit,
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if response.ProjectID != binding.ProjectID ||
		response.Category != sessioncontract.SessionCategoryMain ||
		len(response.Sessions) != 0 ||
		response.NextOffset != nil {
		t.Fatalf("response = %+v", response)
	}
}

func TestMetadataServiceUnlinksOnlySelectedProjectBindingByPath(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	second, err := store.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Second project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	shared, err := store.AttachWorkspaceToProject(context.Background(), second.ProjectID, binding.CanonicalRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject shared path: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(binding.CanonicalRoot)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}

	svc := newProjectViewMetadataService(t, store)
	unlinked, err := svc.UnlinkWorkspaceFromProject(context.Background(), serverapi.ProjectWorkspaceUnlinkRequest{
		ProjectID:                second.ProjectID,
		ProjectWorkspaceSelector: selector,
	})
	if err != nil {
		t.Fatalf("UnlinkWorkspaceFromProject: %v", err)
	}
	if len(unlinked.Blockers) != 0 || unlinked.WorkspaceID != shared.WorkspaceID {
		t.Fatalf("unlink result = %+v, want selected workspace %q unlinked", unlinked, shared.WorkspaceID)
	}
	remainingRoot, remainingBinding, err := store.ResolveWorkspacePath(context.Background(), binding.CanonicalRoot)
	if err != nil {
		t.Fatalf("ResolveWorkspacePath after unlink: %v", err)
	}
	if remainingRoot != binding.CanonicalRoot || remainingBinding == nil || remainingBinding.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("remaining binding = root %q, binding %+v; want project A workspace %q", remainingRoot, remainingBinding, binding.WorkspaceID)
	}
}

func TestMetadataServiceUnlinkWrongProjectIDSelectorFailsBeforeMutationResolution(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	other, err := store.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Other project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForID(binding.WorkspaceID)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	svc := newProjectViewMetadataService(t, store)
	_, err = svc.UnlinkWorkspaceFromProject(context.Background(), serverapi.ProjectWorkspaceUnlinkRequest{
		ProjectID:                other.ProjectID,
		ProjectWorkspaceSelector: selector,
	})
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("wrong-project error = %v, want ErrWorkspaceNotRegistered", err)
	}
	var mutationErr *serverapi.WorkspaceMutationError
	if errors.As(err, &mutationErr) {
		t.Fatalf("wrong-project error = %+v, must not be post-resolution mutation failure", mutationErr)
	}
}

func TestMetadataServiceUnlinkWorkspaceBlocksActiveRuntimeSession(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	attached := attachProjectViewWorkspace(t, store, binding.ProjectID)
	created := createProjectViewSession(t, store, cfg, binding.ProjectID, attached.CanonicalRoot, "active-workspace")
	svc := newProjectViewMetadataService(t, store)
	svc.WithRuntimeAuthority(newProjectViewActiveRuntimeAuthority(t, store, cfg, created))

	selector, err := serverapi.NewProjectWorkspaceSelectorForID(attached.WorkspaceID)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	unlinked, err := svc.UnlinkWorkspaceFromProject(context.Background(), serverapi.ProjectWorkspaceUnlinkRequest{
		ProjectID:                binding.ProjectID,
		ProjectWorkspaceSelector: selector,
	})
	if err != nil {
		t.Fatalf("UnlinkWorkspaceFromProject: %v", err)
	}
	if len(unlinked.Blockers) != 1 || unlinked.Blockers[0].Code != "active_sessions" {
		t.Fatalf("unlink response = %+v, want active_sessions blocker", unlinked)
	}
}

func TestMetadataServiceGetsProjectEditForGUI(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	attachProjectViewWorkspace(t, store, binding.ProjectID)
	svc := newProjectViewMetadataService(t, store)

	edit, err := svc.GetProjectEdit(context.Background(), serverapi.ProjectEditGetRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetProjectEdit: %v", err)
	}
	if edit.ProjectID != binding.ProjectID || edit.ProjectKey != binding.ProjectKey || edit.DisplayName != binding.ProjectName {
		t.Fatalf("edit identity = %+v, want %s/%s/%s", edit, binding.ProjectID, binding.ProjectKey, binding.ProjectName)
	}
}

func TestMetadataServiceListsProjectHomeForGUI(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	svc := newProjectViewMetadataService(t, store)
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
	if first.DefaultWorkflowID == nil || first.DefaultWorkflowID.String() != workflow.ID.String() ||
		first.DefaultWorkflowName != "Default Board" || !first.DefaultWorkflowValid {
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
	if second.DefaultWorkflowValid || second.DefaultWorkflowID != nil || second.DefaultWorkflowName != "" {
		t.Fatalf("empty default workflow = %+v, want invalid empty default workflow", second)
	}
	if _, err := svc.ListProjectHome(context.Background(), serverapi.ProjectHomeListRequest{PageToken: "bad"}); err == nil {
		t.Fatal("expected invalid page token error")
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
	svc := newProjectViewMetadataService(t, store)

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
	svc := newProjectViewMetadataService(t, store)

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
	svc := newProjectViewMetadataService(t, store)

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
	svc := newProjectViewMetadataService(t, store)

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
	svc := newProjectViewMetadataService(t, store)

	plan, err := svc.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: filepath.Join(t.TempDir(), "missing"), Mode: serverapi.ProjectBindingPlanModeHeadless})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous {
		t.Fatalf("plan kind = %q, want %q", plan.Kind, serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous)
	}
}

func newProjectViewMetadataService(t testing.TB, store *metadata.Store) *Service {
	t.Helper()
	svc, err := NewMetadataService(store)
	if err != nil {
		t.Fatalf("NewMetadataService: %v", err)
	}
	workflowStore, err := workflowstore.New(store)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return svc.WithWorkflowExecution(workflowexecution.NewMutationPermit(), projectViewQuiescentExecution{}, workflowStore)
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

func (projectViewTestLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
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
		Settings:              settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext: func() tools.FilesystemContext {
			context, contextErr := runtimewire.NewFilesystemContext(sessionStore.Meta().WorkspaceRoot, sessionStore.Meta().WorkspaceRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if contextErr != nil {
				t.Fatalf("NewFilesystemContext: %v", contextErr)
			}
			return context
		}(),
		Client: projectViewTestLLMClient{},
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
	store := testsetup.OpenStore(t, cfg.PersistenceRoot)
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

func catalogWorkspaceIDs(workspaces []serverapi.ProjectWorkspaceCatalogRow) []string {
	ids := make([]string, 0, len(workspaces))
	for _, workspace := range workspaces {
		ids = append(ids, workspace.WorkspaceID)
	}
	return ids
}
