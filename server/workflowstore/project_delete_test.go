package workflowstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/serverapi"
)

func TestDeleteProjectValidatesEveryArtifactBeforeStaging(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createTestSession(t, ctx, store, binding, cfg)
	createTestSession(t, ctx, store, binding, cfg)
	validateErr := errors.New("second artifact cannot be staged")
	artifacts := &projectDeleteArtifactsFake{validateErrAt: 2, validateErr: validateErr}

	_, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: artifacts,
	})

	if !errors.Is(err, validateErr) {
		t.Fatalf("DeleteProject error = %v, want second artifact validation error", err)
	}
	if artifacts.stageCalls != 0 {
		t.Fatalf("artifact staging calls = %d, want no mutation before all validation", artifacts.stageCalls)
	}
	sessionIDs, err := store.metadata.ListProjectSessionIDs(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectSessionIDs after rejected delete: %v", err)
	}
	if len(sessionIDs) != 2 {
		t.Fatalf("session ids after rejected delete = %v, want both artifacts retained", sessionIDs)
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectAuthoritativeCurrentNodeBlockerWinsPreparationInvalidation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	artifacts := &projectDeleteArtifactsFake{}

	blockers, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		RuntimeBlocker: func(context.Context, []string) ([]serverapi.ProjectDeleteBlocker, func(), error) {
			createTestSession(t, ctx, store, binding, cfg)
			createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
			task := createDefaultTask(t, ctx, store, binding.ProjectID)
			startTask(t, ctx, store, task.ID)
			return nil, func() {}, nil
		},
		Artifacts: artifacts,
	})

	if err != nil {
		t.Fatalf("DeleteProject error = %v, want Current Node blocker", err)
	}
	if errors.Is(err, ErrProjectDeletePreparationInvalidated) {
		t.Fatalf("DeleteProject error = %v, authoritative blocker must win preparation invalidation", err)
	}
	if len(blockers) != 1 || blockers[0].Code != "non_terminal_tasks" {
		t.Fatalf("DeleteProject blockers = %+v, want Current Node non-terminal task blocker", blockers)
	}
	if artifacts.stageCalls != 1 || artifacts.restoreCalls != 1 || artifacts.staged {
		t.Fatalf("artifact lifecycle after authoritative blocker = %+v, want stage then restore", artifacts)
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectRecoversStagedArtifactsBeforeReturningPreflightBlocker(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	artifacts := &projectDeleteArtifactsFake{staged: true}

	blockers, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: artifacts,
	})

	if err != nil {
		t.Fatalf("DeleteProject error = %v, want Current Node blocker", err)
	}
	if len(blockers) != 1 || blockers[0].Code != "non_terminal_tasks" {
		t.Fatalf("DeleteProject blockers = %+v, want Current Node non-terminal task blocker", blockers)
	}
	if artifacts.presentRecoveryCalls != 1 || artifacts.staged {
		t.Fatalf("artifact lifecycle after preflight blocker = %+v, want staged artifacts restored", artifacts)
	}
	if artifacts.stageCalls != 0 {
		t.Fatalf("artifact stage calls = %d, want no new staging for blocked delete", artifacts.stageCalls)
	}
}

func TestDeleteProjectRestoresStagedArtifactsWhenPreparedSessionSetChanges(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	artifacts := &projectDeleteArtifactsFake{}

	_, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		RuntimeBlocker: func(context.Context, []string) ([]serverapi.ProjectDeleteBlocker, func(), error) {
			createTestSession(t, ctx, store, binding, cfg)
			return nil, func() {}, nil
		},
		Artifacts: artifacts,
	})

	if !errors.Is(err, ErrProjectDeletePreparationInvalidated) {
		t.Fatalf("DeleteProject error = %v, want %v", err, ErrProjectDeletePreparationInvalidated)
	}
	if artifacts.stageCalls != 1 || artifacts.restoreCalls != 1 || artifacts.staged {
		t.Fatalf("artifact lifecycle after session-set invalidation = %+v, want stage then restore", artifacts)
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectRuntimePreparationDoesNotBlockUnrelatedMetadata(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	started := make(chan struct{})
	release := make(chan struct{})
	artifacts := &projectDeleteArtifactsFake{}
	deleted := make(chan error, 1)
	go func() {
		_, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
			ProjectID: binding.ProjectID,
			RuntimeBlocker: func(context.Context, []string) ([]serverapi.ProjectDeleteBlocker, func(), error) {
				close(started)
				<-release
				return nil, func() {}, nil
			},
			Artifacts: artifacts,
		})
		deleted <- err
	}()
	<-started

	assertUnrelatedMetadataWriteCompletes(t, store)
	close(release)
	if err := <-deleted; err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
}

func TestDeleteProjectArtifactPreparationAndStagingDoNotHoldWriteTransaction(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	recoverStarted := make(chan struct{})
	releaseRecover := make(chan struct{})
	stageStarted := make(chan struct{})
	releaseStage := make(chan struct{})
	artifacts := &projectDeleteArtifactsFake{
		recoverStarted: recoverStarted,
		releaseRecover: releaseRecover,
		stageStarted:   stageStarted,
		releaseStage:   releaseStage,
	}
	deleted := make(chan error, 1)
	go func() {
		_, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
			ProjectID: binding.ProjectID,
			Artifacts: artifacts,
		})
		deleted <- err
	}()
	<-recoverStarted

	assertUnrelatedMetadataWriteCompletes(t, store)
	close(releaseRecover)
	<-stageStarted
	assertUnrelatedMetadataWriteCompletes(t, store)
	close(releaseStage)
	if err := <-deleted; err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
}

func TestDeleteProjectRestoresStagedArtifactsWhenDatabaseDeletionFails(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_project_delete
BEFORE DELETE ON projects
BEGIN
    SELECT RAISE(FAIL, 'project delete rejected');
END`); err != nil {
		t.Fatalf("create project delete rejection trigger: %v", err)
	}
	artifacts := &projectDeleteArtifactsFake{}

	_, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: artifacts,
	})

	if err == nil {
		t.Fatal("DeleteProject succeeded despite database rejection")
	}
	if artifacts.stageCalls != 1 || artifacts.restoreCalls != 1 || artifacts.staged {
		t.Fatalf("artifact lifecycle after database failure = %+v, want one stage then restore", artifacts)
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectReportsArtifactRestoreFailureWithDatabaseFailure(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_project_delete
BEFORE DELETE ON projects
BEGIN
    SELECT RAISE(FAIL, 'project delete rejected');
END`); err != nil {
		t.Fatalf("create project delete rejection trigger: %v", err)
	}
	restoreErr := errors.New("restore staged tree failed")
	artifacts := &projectDeleteArtifactsFake{restoreErr: restoreErr}

	_, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: artifacts,
	})

	if !errors.Is(err, restoreErr) {
		t.Fatalf("DeleteProject error = %v, want joined restore failure", err)
	}
	if artifacts.restoreCalls != 1 {
		t.Fatalf("artifact restore calls = %d, want 1", artifacts.restoreCalls)
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectRetryFinalizesCommittedTombstone(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	cleanupErr := errors.New("post-commit cleanup failed")
	artifacts := &projectDeleteArtifactsFake{finalizeErr: cleanupErr}

	_, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: artifacts,
	})
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("first DeleteProject error = %v, want post-commit cleanup failure", err)
	}
	if !artifacts.staged {
		t.Fatal("artifact tombstone was not retained after cleanup failure")
	}
	assertProjectAbsent(t, ctx, store, binding.ProjectID)

	artifacts.finalizeErr = nil
	blockers, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: artifacts,
	})
	if err != nil {
		t.Fatalf("retry DeleteProject: %v", err)
	}
	if len(blockers) != 0 || artifacts.staged || artifacts.absentRecoveryCalls != 1 {
		t.Fatalf("retry blockers=%+v artifacts=%+v, want tombstone finalized as deletion success", blockers, artifacts)
	}
}

func TestDeleteProjectStagesThenFinalizesArtifactsOnSuccess(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	artifacts := &projectDeleteArtifactsFake{}

	blockers, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: artifacts,
	})

	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("DeleteProject blockers = %+v, want none", blockers)
	}
	if artifacts.stageCalls != 1 || artifacts.finalizeCalls != 1 || artifacts.restoreCalls != 0 || artifacts.staged {
		t.Fatalf("artifact lifecycle after successful deletion = %+v", artifacts)
	}
	assertProjectAbsent(t, ctx, store, binding.ProjectID)
}

type projectDeleteArtifactsNoop struct{}

func (projectDeleteArtifactsNoop) Recover(ProjectDeleteArtifactRecovery) (bool, error) {
	return false, nil
}

func (projectDeleteArtifactsNoop) Validate(ProjectSessionArtifact) error {
	return nil
}

func (projectDeleteArtifactsNoop) Stage() error {
	return nil
}

func (projectDeleteArtifactsNoop) Restore() error {
	return nil
}

func (projectDeleteArtifactsNoop) Finalize() error {
	return nil
}

type projectDeleteArtifactsFake struct {
	validateErrAt        int
	validateErr          error
	restoreErr           error
	finalizeErr          error
	validateCalls        int
	stageCalls           int
	restoreCalls         int
	finalizeCalls        int
	presentRecoveryCalls int
	absentRecoveryCalls  int
	staged               bool
	recoverStarted       chan struct{}
	releaseRecover       <-chan struct{}
	stageStarted         chan struct{}
	releaseStage         <-chan struct{}
}

func (a *projectDeleteArtifactsFake) Recover(state ProjectDeleteArtifactRecovery) (bool, error) {
	if state == ProjectDeleteArtifactRecoveryProjectPresent {
		a.presentRecoveryCalls++
		if a.recoverStarted != nil {
			close(a.recoverStarted)
			<-a.releaseRecover
		}
		if a.staged {
			a.staged = false
			return true, nil
		}
	}
	if state == ProjectDeleteArtifactRecoveryProjectAbsent {
		a.absentRecoveryCalls++
		if a.staged {
			a.staged = false
			return true, nil
		}
	}
	return false, nil
}

func (a *projectDeleteArtifactsFake) Validate(ProjectSessionArtifact) error {
	a.validateCalls++
	if a.validateErr != nil && a.validateCalls == a.validateErrAt {
		return a.validateErr
	}
	return nil
}

func (a *projectDeleteArtifactsFake) Stage() error {
	a.stageCalls++
	if a.stageStarted != nil {
		close(a.stageStarted)
		<-a.releaseStage
	}
	a.staged = true
	return nil
}

func (a *projectDeleteArtifactsFake) Restore() error {
	a.restoreCalls++
	if a.restoreErr != nil {
		return a.restoreErr
	}
	a.staged = false
	return nil
}

func (a *projectDeleteArtifactsFake) Finalize() error {
	a.finalizeCalls++
	if a.finalizeErr != nil {
		return a.finalizeErr
	}
	a.staged = false
	return nil
}

func assertProjectExists(t *testing.T, ctx context.Context, store *Store, projectID string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&count); err != nil {
		t.Fatalf("count project: %v", err)
	}
	if count != 1 {
		t.Fatalf("project count = %d, want 1", count)
	}
}

func assertProjectAbsent(t *testing.T, ctx context.Context, store *Store, projectID string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&count); err != nil {
		t.Fatalf("count project: %v", err)
	}
	if count != 0 {
		t.Fatalf("project count = %d, want 0", count)
	}
}

func assertUnrelatedMetadataWriteCompletes(t *testing.T, store *Store) {
	t.Helper()
	workspaceRoot := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := store.metadata.RegisterWorkspaceBinding(context.Background(), workspaceRoot)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unrelated metadata write: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated metadata write remained blocked during project deletion preparation")
	}
}
