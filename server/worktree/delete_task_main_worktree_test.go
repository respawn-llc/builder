package worktree

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/worktreecontract"
)

func TestDeleteTaskWorktreeBlocksGitMainBeforeCleanupSideEffects(t *testing.T) {
	env := newStructuredTaskDeletionEnv(t)
	targetRoot := filepath.Join(t.TempDir(), "git-main")
	runner := &structuredTaskDeleteGitRunner{
		listOutput: []byte(
			"worktree " + targetRoot + "\nHEAD " + strings.Repeat("a", 40) + "\nbranch refs/heads/main\n\n" +
				"worktree " + env.workspaceRoot + "\nHEAD " + strings.Repeat("b", 40) + "\nbranch refs/heads/workspace\n",
		),
	}
	env.service.git = NewGitInspector(runner)
	task, workflowStore := createTaskWorktreeTestTaskWithSource(t, env, env.binding.WorkspaceID)
	worktreeID := "task-git-main"
	if err := env.store.UpsertWorktreeRecord(env.ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   env.binding.WorkspaceID,
		CanonicalRoot: targetRoot,
		DisplayName:   "Git main",
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := env.store.Queries().BindInitialTaskManagedWorktree(env.ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		TaskID:            string(task.ID),
	}); err != nil {
		t.Fatalf("BindInitialTaskManagedWorktree: %v", err)
	}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("a", 40)
	if err := workflowStore.LockTaskExecutionTarget(env.ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   env.binding.WorkspaceID,
			SourceWorkspaceRoot: env.workspaceRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: worktreeID,
				Root:       targetRoot,
			},
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, worktreeID, "pkg")
	reminder := &session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			WorktreePath:  targetRoot,
			WorkspaceRoot: env.workspaceRoot,
			EffectiveCwd:  filepath.Join(targetRoot, "pkg"),
		},
	}
	if err := env.session.SetWorktreeReminderState(reminder); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	beforeTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget before delete: %v", err)
	}
	beforeReminder := env.session.Meta().WorktreeReminder
	beforeTask, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask before delete: %v", err)
	}
	beforeRecord, err := env.store.GetWorktreeRecordByID(env.ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID before delete: %v", err)
	}

	_, err = env.service.DeleteTaskWorktree(env.ctx, DeleteTaskWorktreeRequest{TaskID: string(task.ID)})
	if !errors.Is(err, worktreecontract.ErrWorktreeBlocked) {
		t.Fatalf("DeleteTaskWorktree error = %v, want ErrWorktreeBlocked", err)
	}
	if runner.statusCalls != 0 || runner.removeCalls != 0 {
		t.Fatalf("Git cleanup calls = status=%d remove=%d, want none before blocked result", runner.statusCalls, runner.removeCalls)
	}
	afterTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget after delete: %v", err)
	}
	if !reflect.DeepEqual(afterTarget, beforeTarget) {
		t.Fatalf("Session target changed: before=%+v after=%+v", beforeTarget, afterTarget)
	}
	afterReminder := env.session.Meta().WorktreeReminder
	if !session.WorktreeReminderStateEqual(*afterReminder, *beforeReminder) {
		t.Fatalf("Session reminder changed: before=%+v after=%+v", beforeReminder, afterReminder)
	}
	afterTask, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after delete: %v", err)
	}
	if afterTask.ManagedWorktreeID != beforeTask.ManagedWorktreeID ||
		afterTask.ExecutionTargetMode != beforeTask.ExecutionTargetMode ||
		afterTask.ExecutionTargetCommitOid != beforeTask.ExecutionTargetCommitOid {
		t.Fatalf("Task changed: before=%+v after=%+v", beforeTask, afterTask)
	}
	afterRecord, err := env.store.GetWorktreeRecordByID(env.ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID after delete: %v", err)
	}
	if afterRecord != beforeRecord {
		t.Fatalf("Worktree metadata changed: before=%+v after=%+v", beforeRecord, afterRecord)
	}
}

func newStructuredTaskDeletionEnv(t *testing.T) *serviceTestEnv {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, ".kent-test"))
	markGitRepository(t, workspace)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	store := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := store.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	workspaceRoot, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	sess := createServiceTestSession(t, store, cfg, binding)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    store.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	publisher := &serviceTestPublisher{}
	processes := &serviceTestProcessSource{}
	return &serviceTestEnv{
		t:             t,
		ctx:           ctx,
		store:         store,
		cfg:           cfg,
		binding:       binding,
		session:       sess,
		authority:     authority,
		publisher:     publisher,
		processes:     processes,
		service:       NewService(store, nil, authority, publisher, processes, ServiceOptions{BaseDir: cfg.Settings.Worktrees.BaseDir}),
		leaseID:       "lease-1",
		workspaceRoot: workspaceRoot,
		baseDir:       cfg.Settings.Worktrees.BaseDir,
	}
}

type structuredTaskDeleteGitRunner struct {
	listOutput  []byte
	statusCalls int
	removeCalls int
}

func (r *structuredTaskDeleteGitRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	output, exitCode, err := r.Run(ctx, dir, args...)
	if err != nil {
		return nil, formatGitRunError(exitCode, err, output, args...)
	}
	return output, nil
}

func (r *structuredTaskDeleteGitRunner) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	switch gitCommandKey(args...) {
	case gitCommandKey("worktree", "list", "--porcelain"):
		return append([]byte(nil), r.listOutput...), 0, nil
	case gitCommandKey("status", "--porcelain=v1", "-z"):
		r.statusCalls++
		return nil, 0, nil
	case gitCommandKey("worktree", "remove", "--force"):
		r.removeCalls++
		return nil, 1, errors.New("unexpected Git worktree removal")
	default:
		if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
			r.removeCalls++
			return nil, 1, errors.New("unexpected Git worktree removal")
		}
		return nil, 1, errors.New("unexpected Git command")
	}
}
