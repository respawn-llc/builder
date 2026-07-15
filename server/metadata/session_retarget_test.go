package metadata

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
)

type sessionRetargetFixture struct {
	store         *Store
	config        config.App
	source        Binding
	targetProject Binding
	session       *session.Store
}

func newSessionRetargetFixture(t *testing.T) sessionRetargetFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	sourceRoot := t.TempDir()
	cfg, err := config.Load(sourceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	store, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.RegisterWorkspaceBinding(context.Background(), sourceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding source: %v", err)
	}
	targetProject, err := store.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Target")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace target: %v", err)
	}
	sessionStore, err := session.Create(
		filepath.Join(cfg.PersistenceRoot, "projects", source.ProjectID, "sessions"),
		source.WorkspaceName,
		source.CanonicalRoot,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return sessionRetargetFixture{
		store:         store,
		config:        cfg,
		source:        source,
		targetProject: targetProject,
		session:       sessionStore,
	}
}

func TestPlanSessionWorkspaceRetargetRejectsForeignOnlyDefaultWithoutMutation(t *testing.T) {
	fixture := newSessionRetargetFixture(t)
	targetRoot := t.TempDir()
	if _, err := fixture.store.AttachWorkspaceToProject(context.Background(), fixture.targetProject.ProjectID, targetRoot); err != nil {
		t.Fatalf("AttachWorkspaceToProject target: %v", err)
	}

	_, err := fixture.store.PlanSessionWorkspaceRetarget(context.Background(), SessionWorkspaceRetargetRequest{
		SessionID:     fixture.session.Meta().SessionID,
		WorkspaceRoot: targetRoot,
	})
	var retargetErr *serverapi.SessionRetargetError
	if !errors.As(err, &retargetErr) || retargetErr.Reason != serverapi.SessionRetargetTargetProjectRequired {
		t.Fatalf("PlanSessionWorkspaceRetarget error = %v, want target-project-required", err)
	}
	target, err := fixture.store.ResolveSessionExecutionTarget(context.Background(), fixture.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceID != fixture.source.WorkspaceID {
		t.Fatalf("session workspace = %q, want unchanged %q", target.WorkspaceID, fixture.source.WorkspaceID)
	}
}

func TestCommitSessionWorkspaceRetargetUsesSourceBindingWhenPathIsShared(t *testing.T) {
	fixture := newSessionRetargetFixture(t)
	targetRoot := t.TempDir()
	sourceBinding, err := fixture.store.AttachWorkspaceToProject(context.Background(), fixture.source.ProjectID, targetRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}
	if _, err := fixture.store.AttachWorkspaceToProject(context.Background(), fixture.targetProject.ProjectID, targetRoot); err != nil {
		t.Fatalf("AttachWorkspaceToProject target: %v", err)
	}

	plan, err := fixture.store.PlanSessionWorkspaceRetarget(context.Background(), SessionWorkspaceRetargetRequest{
		SessionID:     fixture.session.Meta().SessionID,
		WorkspaceRoot: targetRoot,
	})
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	result, err := fixture.store.CommitSessionWorkspaceRetarget(context.Background(), plan, time.Now().UTC())
	if err != nil {
		t.Fatalf("CommitSessionWorkspaceRetarget: %v", err)
	}
	if result.Binding.ProjectID != fixture.source.ProjectID || result.Binding.WorkspaceID != sourceBinding.WorkspaceID {
		t.Fatalf("binding = %+v, want source-project binding %+v", result.Binding, sourceBinding)
	}
}

func TestCommitSessionWorkspaceRetargetMovesProjectAndAutoAttachesWorkspace(t *testing.T) {
	fixture := newSessionRetargetFixture(t)
	targetRoot := t.TempDir()
	targetProjectID := fixture.targetProject.ProjectID

	plan, err := fixture.store.PlanSessionWorkspaceRetarget(context.Background(), SessionWorkspaceRetargetRequest{
		SessionID:     fixture.session.Meta().SessionID,
		WorkspaceRoot: targetRoot,
		ProjectID:     &targetProjectID,
	})
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	result, err := fixture.store.CommitSessionWorkspaceRetarget(context.Background(), plan, time.Now().UTC())
	if err != nil {
		t.Fatalf("CommitSessionWorkspaceRetarget: %v", err)
	}
	if !result.WorkspaceBindingCreated {
		t.Fatal("WorkspaceBindingCreated = false, want true")
	}
	if result.Binding.ProjectID != targetProjectID {
		t.Fatalf("target project = %q, want %q", result.Binding.ProjectID, targetProjectID)
	}
	belongs, err := fixture.store.SessionBelongsToProject(context.Background(), fixture.session.Meta().SessionID, targetProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject: %v", err)
	}
	if !belongs {
		t.Fatal("session did not move to target project")
	}
}

func TestPlanSessionWorkspaceRetargetRejectsExplicitForeignBindingWithoutMutation(t *testing.T) {
	fixture := newSessionRetargetFixture(t)
	targetRoot := t.TempDir()
	if _, err := fixture.store.AttachWorkspaceToProject(context.Background(), fixture.targetProject.ProjectID, targetRoot); err != nil {
		t.Fatalf("AttachWorkspaceToProject foreign target: %v", err)
	}
	requestedProject, err := fixture.store.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Requested")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace requested: %v", err)
	}
	before, err := fixture.store.ListProjectWorkspaces(context.Background(), requestedProject.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkspaces before: %v", err)
	}
	requestedProjectID := requestedProject.ProjectID

	_, err = fixture.store.PlanSessionWorkspaceRetarget(context.Background(), SessionWorkspaceRetargetRequest{
		SessionID:     fixture.session.Meta().SessionID,
		WorkspaceRoot: targetRoot,
		ProjectID:     &requestedProjectID,
	})
	var retargetErr *serverapi.SessionRetargetError
	if !errors.As(err, &retargetErr) || retargetErr.Reason != serverapi.SessionRetargetTargetProjectConflict {
		t.Fatalf("PlanSessionWorkspaceRetarget error = %v, want target-project-conflict", err)
	}
	after, err := fixture.store.ListProjectWorkspaces(context.Background(), requestedProject.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkspaces after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("requested project workspace count = %d, want unchanged %d", len(after), len(before))
	}
	belongs, err := fixture.store.SessionBelongsToProject(context.Background(), fixture.session.Meta().SessionID, fixture.source.ProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject source: %v", err)
	}
	if !belongs {
		t.Fatal("failed plan changed session ownership")
	}
}

func TestPlanSessionWorkspaceRetargetRejectsWorkflowOwnedCrossProject(t *testing.T) {
	fixture := newSessionRetargetFixture(t)
	if err := fixture.session.SetWorkflowSessionState(&session.WorkflowSessionState{
		RunID:      "run-1",
		TaskID:     "task-1",
		WorkflowID: "workflow-1",
	}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	targetProjectID := fixture.targetProject.ProjectID

	_, err := fixture.store.PlanSessionWorkspaceRetarget(context.Background(), SessionWorkspaceRetargetRequest{
		SessionID:     fixture.session.Meta().SessionID,
		WorkspaceRoot: t.TempDir(),
		ProjectID:     &targetProjectID,
	})
	var retargetErr *serverapi.SessionRetargetError
	if !errors.As(err, &retargetErr) || retargetErr.Reason != serverapi.SessionRetargetWorkflowOwned {
		t.Fatalf("PlanSessionWorkspaceRetarget error = %v, want workflow-owned", err)
	}
}
