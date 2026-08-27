package metadata

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
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
	sourceRoot := t.TempDir()
	cfg := loadMetadataTestConfig(t, sourceRoot, filepath.Join(t.TempDir(), "persistence"))
	store := openInMemoryMetadataTestStore(t, cfg.PersistenceRoot)
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
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	return sessionRetargetFixture{
		store:         store,
		config:        cfg,
		source:        source,
		targetProject: targetProject,
		session:       sessionStore,
	}
}

func (f sessionRetargetFixture) plan(t *testing.T, root string, projectID *string) SessionWorkspaceRetargetPlan {
	t.Helper()
	plan, err := f.store.PlanSessionWorkspaceRetarget(t.Context(), SessionWorkspaceRetargetRequest{
		SessionID:     f.session.Meta().SessionID,
		WorkspaceRoot: root,
		ProjectID:     projectID,
	})
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	return plan
}

func (f sessionRetargetFixture) commit(t *testing.T, plan SessionWorkspaceRetargetPlan) SessionWorkspaceRetargetResult {
	t.Helper()
	result, err := f.store.CommitSessionWorkspaceRetarget(t.Context(), plan, time.Now().UTC())
	if err != nil {
		t.Fatalf("CommitSessionWorkspaceRetarget: %v", err)
	}
	return result
}

func (f sessionRetargetFixture) reopen(t *testing.T) *session.Store {
	t.Helper()
	reopened, err := session.OpenByID(
		f.config.PersistenceRoot,
		f.session.Meta().SessionID,
		f.store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	return reopened
}

func TestPlanSessionWorkspaceRetargetRejectsForeignOnlyDefaultWithoutMutation(t *testing.T) {
	t.Parallel()
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

func TestPlanSessionWorkspaceRetargetDetectsAuthoritativeNoOpWithoutCreatingReminder(t *testing.T) {
	t.Parallel()
	fixture := newSessionRetargetFixture(t)
	plan := fixture.plan(t, fixture.source.CanonicalRoot, nil)
	if !plan.NoOp() {
		t.Fatalf("plan = %+v, want no-op", plan)
	}
	if fixture.session.Meta().RebindReminder != nil {
		t.Fatalf("no-op plan created reminder: %+v", fixture.session.Meta().RebindReminder)
	}
	result := fixture.commit(t, plan)
	if result.Binding.WorkspaceID != fixture.source.WorkspaceID {
		t.Fatalf("no-op binding = %+v, want %+v", result.Binding, fixture.source)
	}
	reopened := fixture.reopen(t)
	if reopened.Meta().RebindReminder != nil {
		t.Fatalf("no-op commit created reminder: %+v", reopened.Meta().RebindReminder)
	}
}

func TestCommitSessionWorkspaceRetargetRebindsUnlinkedSession(t *testing.T) {
	t.Parallel()
	fixture := newSessionRetargetFixture(t)
	ctx := t.Context()
	if _, err := fixture.store.db.ExecContext(
		ctx,
		"UPDATE sessions SET workspace_id = NULL, worktree_id = NULL WHERE id = ?",
		fixture.session.Meta().SessionID,
	); err != nil {
		t.Fatalf("unlink Session workspace: %v", err)
	}
	targetRoot := t.TempDir()
	plan := fixture.plan(t, targetRoot, nil)
	if plan.SourceBinding != nil || plan.SourceEffectiveWorkingDirectory != fixture.source.CanonicalRoot {
		t.Fatalf("unlinked source = binding %+v cwd %q", plan.SourceBinding, plan.SourceEffectiveWorkingDirectory)
	}
	result := fixture.commit(t, plan)
	if result.Binding.WorkspaceID == "" || result.Binding.CanonicalRoot == fixture.source.CanonicalRoot {
		t.Fatalf("unlinked Session result = %+v", result)
	}
}

func TestCommitSessionWorkspaceRetargetAttachesUnlinkedSameRootWithoutReminder(t *testing.T) {
	t.Parallel()
	fixture := newSessionRetargetFixture(t)
	ctx := t.Context()
	if _, err := fixture.store.db.ExecContext(
		ctx,
		"UPDATE sessions SET workspace_id = NULL, worktree_id = NULL WHERE id = ?",
		fixture.session.Meta().SessionID,
	); err != nil {
		t.Fatalf("unlink Session workspace: %v", err)
	}
	plan := fixture.plan(t, fixture.source.CanonicalRoot, nil)
	if plan.NoOp() {
		t.Fatalf("unlinked Session plan = %+v, want attachment apply", plan)
	}
	result := fixture.commit(t, plan)
	if result.Binding.WorkspaceID != fixture.source.WorkspaceID || result.RebindReminder != nil {
		t.Fatalf("same-root attachment result = %+v", result)
	}
}

func TestPlanSessionWorkspaceRetargetRejectsRecordedSourceAndTargetWorktrees(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		reason serverapi.SessionRetargetErrorReason
		seed   func(*testing.T, sessionRetargetFixture) string
	}{
		{
			name:   "source",
			reason: serverapi.SessionRetargetSourceWorktree,
			seed: func(t *testing.T, fixture sessionRetargetFixture) string {
				root := filepath.Join(fixture.source.CanonicalRoot, "source-worktree")
				createMetadataTestWorktree(t, t.Context(), fixture.store, fixture.source.WorkspaceID, "source-worktree", root)
				if err := fixture.store.UpdateSessionExecutionTarget(t.Context(), SessionExecutionTargetUpdate{
					SessionID: fixture.session.Meta().SessionID,
					Workspace: &SessionExecutionTargetUpdateWorkspace{ID: fixture.source.WorkspaceID},
					Worktree:  &SessionExecutionTargetUpdateWorktree{ID: "source-worktree"},
				}); err != nil {
					t.Fatalf("UpdateSessionExecutionTarget: %v", err)
				}
				return fixture.targetProject.CanonicalRoot
			},
		},
		{
			name:   "target",
			reason: serverapi.SessionRetargetTargetWorktree,
			seed: func(t *testing.T, fixture sessionRetargetFixture) string {
				return createMetadataTestWorktree(t, t.Context(), fixture.store, fixture.source.WorkspaceID, "target-worktree", filepath.Join(fixture.source.CanonicalRoot, "target-worktree"))
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRetargetFixture(t)
			targetRoot := test.seed(t, fixture)
			_, err := fixture.store.PlanSessionWorkspaceRetarget(t.Context(), SessionWorkspaceRetargetRequest{
				SessionID:     fixture.session.Meta().SessionID,
				WorkspaceRoot: targetRoot,
			})
			var retargetErr *serverapi.SessionRetargetError
			if !errors.As(err, &retargetErr) || retargetErr.Reason != test.reason {
				t.Fatalf("PlanSessionWorkspaceRetarget error = %v, want %q", err, test.reason)
			}
		})
	}
}

func TestCommitSessionWorkspaceRetargetUsesSourceBindingWhenPathIsShared(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestCommitSessionWorkspaceRetargetProjectChangeAtSameDirectoryOmitsWorkingDirectoryFact(t *testing.T) {
	t.Parallel()
	fixture := newSessionRetargetFixture(t)
	ctx := context.Background()
	if _, err := fixture.store.AttachWorkspaceToProject(
		ctx,
		fixture.targetProject.ProjectID,
		fixture.source.CanonicalRoot,
	); err != nil {
		t.Fatalf("AttachWorkspaceToProject target: %v", err)
	}
	targetProjectID := fixture.targetProject.ProjectID
	plan := fixture.plan(t, fixture.source.CanonicalRoot, &targetProjectID)
	if plan.NoOp() {
		t.Fatal("cross-project move at the same path was planned as a no-op")
	}
	result := fixture.commit(t, plan)
	reminder := result.RebindReminder
	if reminder == nil {
		t.Fatal("project change at the same directory did not create a reminder")
	}
	if reminder.SourceProject.ID != fixture.source.ProjectID ||
		reminder.TargetProject.ID != fixture.targetProject.ProjectID ||
		reminder.WorkingDirectory != nil {
		t.Fatalf("rebind reminder = %+v, want project references without working directory", reminder)
	}
}

func TestCommitSessionWorkspaceRetargetReopenPreservesLocationAndIndependentReminders(t *testing.T) {
	t.Parallel()
	fixture := newSessionRetargetFixture(t)
	worktreeReminder := session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/source"),
			WorktreePath:  filepath.Join(fixture.source.CanonicalRoot, "worktree"),
			WorkspaceRoot: fixture.source.CanonicalRoot,
			EffectiveCwd:  filepath.Join(fixture.source.CanonicalRoot, "worktree", "pkg"),
		},
	}
	if err := fixture.session.SetWorktreeReminderState(&worktreeReminder); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	targetRoot := t.TempDir()
	result := fixture.commit(t, fixture.plan(t, targetRoot, nil))
	reopened := fixture.reopen(t)
	meta := reopened.Meta()
	if meta.WorkspaceRoot != result.Binding.CanonicalRoot {
		t.Fatalf("reopened workspace root = %q, want %q", meta.WorkspaceRoot, result.Binding.CanonicalRoot)
	}
	if meta.WorktreeReminder == nil || !session.WorktreeReminderStateEqual(*meta.WorktreeReminder, *fixture.session.Meta().WorktreeReminder) {
		t.Fatalf("reopened worktree reminder = %+v, want %+v", meta.WorktreeReminder, fixture.session.Meta().WorktreeReminder)
	}
	wantRebind := session.SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: fixture.source.ProjectID, Name: fixture.source.ProjectName},
		TargetProject:    serverapi.ProjectReference{ID: fixture.source.ProjectID, Name: fixture.source.ProjectName},
		WorkingDirectory: &result.Binding.CanonicalRoot,
	}
	if meta.RebindReminder == nil || !session.SessionRebindReminderEqual(*meta.RebindReminder, wantRebind) {
		t.Fatalf("reopened rebind reminder = %+v, want %+v", meta.RebindReminder, wantRebind)
	}
}

func TestCommitSessionWorkspaceRetargetTransactionFailurePreservesLocationAndBothReminders(t *testing.T) {
	t.Parallel()
	fixture := newSessionRetargetFixture(t)
	ctx := context.Background()
	worktreeReminder := session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeExit,
		WorktreeContext: session.WorktreeContext{
			WorktreePath:  filepath.Join(fixture.source.CanonicalRoot, "old-worktree"),
			WorkspaceRoot: fixture.source.CanonicalRoot,
			EffectiveCwd:  fixture.source.CanonicalRoot,
		},
	}
	if err := fixture.session.SetWorktreeReminderState(&worktreeReminder); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	oldDirectory := fixture.source.CanonicalRoot
	oldRebind := session.SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: fixture.source.ProjectID, Name: fixture.source.ProjectName},
		TargetProject:    serverapi.ProjectReference{ID: fixture.source.ProjectID, Name: fixture.source.ProjectName},
		WorkingDirectory: &oldDirectory,
	}
	if err := fixture.session.SetSessionRebindReminder(&oldRebind); err != nil {
		t.Fatalf("SetSessionRebindReminder: %v", err)
	}
	beforeTarget, err := fixture.store.ResolveSessionExecutionTarget(ctx, fixture.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget before: %v", err)
	}
	beforeMeta := fixture.session.Meta()
	plan := fixture.plan(t, t.TempDir(), nil)
	if _, err := fixture.store.db.ExecContext(ctx, `
CREATE TRIGGER fail_session_retarget_combined_write
BEFORE UPDATE ON sessions
BEGIN
    SELECT RAISE(ABORT, 'injected combined session retarget failure');
END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	if _, err := fixture.store.CommitSessionWorkspaceRetarget(ctx, plan, time.Now().UTC()); err == nil {
		t.Fatal("CommitSessionWorkspaceRetarget succeeded despite injected failure")
	}
	afterTarget, err := fixture.store.ResolveSessionExecutionTarget(ctx, fixture.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget after: %v", err)
	}
	record, err := fixture.store.ResolvePersistedSession(ctx, fixture.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession after: %v", err)
	}
	if afterTarget.WorkspaceID != beforeTarget.WorkspaceID ||
		afterTarget.WorkspaceRoot != beforeTarget.WorkspaceRoot ||
		afterTarget.EffectiveWorkdir != beforeTarget.EffectiveWorkdir {
		t.Fatalf("location changed after rollback: before=%+v after=%+v", beforeTarget, afterTarget)
	}
	if record.Meta.WorktreeReminder == nil ||
		!session.WorktreeReminderStateEqual(*record.Meta.WorktreeReminder, *beforeMeta.WorktreeReminder) {
		t.Fatalf("worktree reminder changed after rollback: before=%+v after=%+v", beforeMeta.WorktreeReminder, record.Meta.WorktreeReminder)
	}
	if record.Meta.RebindReminder == nil ||
		!session.SessionRebindReminderEqual(*record.Meta.RebindReminder, *beforeMeta.RebindReminder) {
		t.Fatalf("rebind reminder changed after rollback: before=%+v after=%+v", beforeMeta.RebindReminder, record.Meta.RebindReminder)
	}
}

func TestPlanSessionWorkspaceRetargetRejectsExplicitForeignBindingWithoutMutation(t *testing.T) {
	t.Parallel()
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

func TestSessionSnapshotImportAndRetargetRemoveLegacyWorkflowMetadata(t *testing.T) {
	t.Parallel()
	fixture := newSessionRetargetFixture(t)
	ctx := context.Background()
	sessionID := fixture.session.Meta().SessionID
	if _, err := fixture.store.db.ExecContext(ctx, `
UPDATE sessions
SET metadata_json = json_set(metadata_json, '$.workflow_session', json_object('run_id', 'stale-run'))
WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("seed stale workflow metadata: %v", err)
	}
	record, err := fixture.store.ResolvePersistedSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession before import: %v", err)
	}
	if err := fixture.store.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: record.SessionDir,
		Meta:       *record.Meta,
	}); err != nil {
		t.Fatalf("ImportSessionSnapshot: %v", err)
	}
	assertWorkflowSessionMetadataAbsent(t, fixture.store, sessionID)
	if _, err := fixture.store.db.ExecContext(ctx, `
UPDATE sessions
SET metadata_json = json_set(metadata_json, '$.workflow_session', json_object('run_id', 'stale-run'))
WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("restore stale workflow metadata: %v", err)
	}
	targetProjectID := fixture.targetProject.ProjectID
	plan, err := fixture.store.PlanSessionWorkspaceRetarget(ctx, SessionWorkspaceRetargetRequest{
		SessionID:     sessionID,
		WorkspaceRoot: t.TempDir(),
		ProjectID:     &targetProjectID,
	})
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	if _, err := fixture.store.CommitSessionWorkspaceRetarget(ctx, plan, time.Now().UTC()); err != nil {
		t.Fatalf("CommitSessionWorkspaceRetarget: %v", err)
	}
	assertWorkflowSessionMetadataAbsent(t, fixture.store, sessionID)
}

func assertWorkflowSessionMetadataAbsent(t *testing.T, store *Store, sessionID string) {
	t.Helper()
	var workflowMetadata sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT json_type(metadata_json, '$.workflow_session')
FROM sessions
WHERE id = ?`, sessionID).Scan(&workflowMetadata); err != nil {
		t.Fatalf("query workflow session metadata: %v", err)
	}
	if workflowMetadata.Valid {
		t.Fatalf("workflow session metadata = %q, want absent", workflowMetadata.String)
	}
}
