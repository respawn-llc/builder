package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/worktreesetup"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestMaterializeInitialTaskWorktreeRequiresResolvedCommit(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	_, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID: task.ID,
	})
	if err == nil {
		t.Fatal("MaterializeInitialTaskWorktree accepted a missing resolved commit")
	}
	row, queryErr := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if queryErr != nil {
		t.Fatalf("GetTask: %v", queryErr)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree id = %+v, want no provisional candidate", row.ManagedWorktreeID)
	}
}

func TestRestoreLockedTaskWorktreeAcceptsHealthyChangedNamedBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	runGit(t, materialized.Worktree.CanonicalRoot, "branch", "-m", "operator-renamed")

	restored, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("RestoreLockedTaskWorktree: %v", err)
	}
	if restored.Created ||
		restored.Worktree.WorktreeID != materialized.Worktree.WorktreeID ||
		restored.Worktree.CanonicalRoot != materialized.Worktree.CanonicalRoot ||
		restored.Worktree.BranchName != "operator-renamed" {
		t.Fatalf("restored worktree = %+v, want healthy renamed root reuse", restored)
	}
}

func TestRestoreLockedTaskWorktreeRejectsDetachedHead(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	runGit(t, materialized.Worktree.CanonicalRoot, "checkout", "--detach")

	_, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseDetachedHead {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want detached-head locked target error", err)
	}
}

func TestRestoreLockedTaskWorktreeRejectsDifferentRepository(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, materialized.Worktree.CanonicalRoot, true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	if err := os.MkdirAll(materialized.Worktree.CanonicalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll replacement repository root: %v", err)
	}
	initGitRepo(t, materialized.Worktree.CanonicalRoot)

	_, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseInvalidRoot {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want invalid-root locked target error", err)
	}
}

func TestRestoreLockedTaskWorktreeRejectsNonGitRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, materialized.Worktree.CanonicalRoot, true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	if err := os.MkdirAll(materialized.Worktree.CanonicalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll non-Git root: %v", err)
	}

	_, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseInvalidRoot {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want invalid-root locked target error", err)
	}
}

func TestRestoreLockedTaskWorktreeReportsInaccessibleRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	root := materialized.Worktree.CanonicalRoot
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, root, true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	if err := os.Symlink(root, root); err != nil {
		t.Fatalf("create self-referential root symlink: %v", err)
	}

	_, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseRootInaccessible {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want root-inaccessible locked target error", err)
	}
}

func TestRestoreLockedTaskWorktreeMapsGitFailure(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, materialized.Worktree.CanonicalRoot, true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	env.service.git = NewGitInspector(&selectedCommandFailingGitRunner{
		base:      execGitCommandRunner{},
		directory: env.workspaceRoot,
		arguments: []string{"show-ref", "--verify", "--quiet", "refs/heads/" + task.ShortID},
	})

	_, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseGitFailure {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want Git-failure locked target error", err)
	}
}

func TestRestoreLockedTaskWorktreeReportsConflictForRegisteredMissingRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	record, err := env.store.GetWorktreeRecordByID(env.ctx, materialized.Worktree.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	gitMetadata, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
	}
	if gitMetadata.BranchName != task.ShortID {
		t.Fatalf("persisted branch = %q, want %q (metadata %s)", gitMetadata.BranchName, task.ShortID, record.GitMetadataJSON)
	}
	oldRoot := materialized.Worktree.CanonicalRoot
	if err := os.RemoveAll(oldRoot); err != nil {
		t.Fatalf("remove worktree root: %v", err)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID:           task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseConflict {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want conflict without registration deletion", err)
	}
	if _, statErr := os.Stat(oldRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("registered missing root = %q was mutated: %v", oldRoot, statErr)
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, task.ShortID); branchErr != nil {
		t.Fatalf("BranchExists: %v", branchErr)
	} else if !exists {
		t.Fatalf("restore deleted existing branch %q", task.ShortID)
	}
}

func TestRestoreLockedTaskWorktreeReportsMissingBranchWithoutRecreatingFromSnapshot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	oldRoot := materialized.Worktree.CanonicalRoot
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, oldRoot, true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	if err := env.service.git.deleteBranch(env.ctx, env.workspaceRoot, task.ShortID, true); err != nil {
		t.Fatalf("delete branch: %v", err)
	}

	_, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseMissingBranch {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want missing-branch locked target error", err)
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, task.ShortID); branchErr != nil {
		t.Fatalf("BranchExists: %v", branchErr)
	} else if exists {
		t.Fatalf("restore recreated missing branch %q from historical snapshot", task.ShortID)
	}
	if _, statErr := os.Stat(oldRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("restore recreated missing root %q despite missing branch: %v", oldRoot, statErr)
	}
}

func TestRestoreLockedTaskWorktreeRebindsHealthyDeterministicRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	updated, err := env.store.Queries().UpdateTaskManagedWorktree(env.ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:              string(task.ID),
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("clear task managed worktree: %v", err)
	}
	if updated != 1 {
		t.Fatalf("clear task managed worktree updated %d rows, want 1", updated)
	}
	taskRow, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	staleRecord, err := env.store.GetWorktreeRecordByCanonicalRoot(env.ctx, materialized.Worktree.CanonicalRoot)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByCanonicalRoot: %v", err)
	}
	if !taskRow.SourceWorkspaceID.Valid || taskRow.SourceWorkspaceID.String != staleRecord.WorkspaceID {
		t.Fatalf("task source workspace = %+v, stale worktree workspace = %q", taskRow.SourceWorkspaceID, staleRecord.WorkspaceID)
	}

	restored, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("RestoreLockedTaskWorktree: %v", err)
	}
	if restored.Created ||
		restored.Worktree.WorktreeID != materialized.Worktree.WorktreeID ||
		restored.Worktree.CanonicalRoot != materialized.Worktree.CanonicalRoot {
		t.Fatalf("restored worktree = %+v, want healthy deterministic root rebound", restored)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != materialized.Worktree.WorktreeID {
		t.Fatalf("task managed worktree id = %+v, want rebound %q", row.ManagedWorktreeID, materialized.Worktree.WorktreeID)
	}
}

func TestRestoreLockedTaskWorktreeRecreatesMissingUnboundRootFromRecordedNamedBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	runGit(t, materialized.Worktree.CanonicalRoot, "branch", "-m", "operator-renamed")
	if _, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID}); err != nil {
		t.Fatalf("refresh locked worktree metadata: %v", err)
	}
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, materialized.Worktree.CanonicalRoot, true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, "operator-renamed"); err != nil {
		t.Fatalf("BranchExists: %v", err)
	} else if !exists {
		t.Fatal("operator-renamed branch is missing before restore")
	}
	updated, err := env.store.Queries().UpdateTaskManagedWorktree(env.ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:              string(task.ID),
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("clear task managed worktree: %v", err)
	}
	if updated != 1 {
		t.Fatalf("clear task managed worktree updated %d rows, want 1", updated)
	}
	taskRow, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if taskRow.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree id = %+v, want missing binding", taskRow.ManagedWorktreeID)
	}

	restored, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("RestoreLockedTaskWorktree: %v", err)
	}
	if !restored.Created || restored.CreatedBranch {
		t.Fatalf("restored worktree = %+v, want recreated unbound root from recorded branch", restored)
	}
	if restored.Worktree.WorktreeID != materialized.Worktree.WorktreeID ||
		restored.Worktree.CanonicalRoot != materialized.Worktree.CanonicalRoot ||
		restored.Worktree.BranchName != "operator-renamed" {
		t.Fatalf("restored worktree = %+v, want original worktree identity on operator-renamed branch", restored)
	}
}

func TestRestoreLockedTaskWorktreeDoesNotInferUnboundBranchFromTaskShortID(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, materialized.Worktree.CanonicalRoot, true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	updated, err := env.store.Queries().UpdateTaskManagedWorktree(env.ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:              string(task.ID),
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("clear task managed worktree: %v", err)
	}
	if updated != 1 {
		t.Fatalf("clear task managed worktree updated %d rows, want 1", updated)
	}
	if err := env.store.DeleteWorktreeRecordByID(env.ctx, materialized.Worktree.WorktreeID); err != nil {
		t.Fatalf("DeleteWorktreeRecordByID: %v", err)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseMissingBranch {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want missing-branch without short-id inference", err)
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, task.ShortID); branchErr != nil {
		t.Fatalf("BranchExists: %v", branchErr)
	} else if !exists {
		t.Fatalf("restore deleted existing branch %q", task.ShortID)
	}
	if _, statErr := os.Stat(materialized.Worktree.CanonicalRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("restore recreated unbound root %q from task short ID: %v", materialized.Worktree.CanonicalRoot, statErr)
	}
}

func TestRestoreLockedTaskWorktreeReportsConflictingDeterministicRootRecord(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	updated, err := env.store.Queries().UpdateTaskManagedWorktree(env.ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:              string(task.ID),
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("clear task managed worktree: %v", err)
	}
	if updated != 1 {
		t.Fatalf("clear task managed worktree updated %d rows, want 1", updated)
	}
	otherRoot := filepath.Join(t.TempDir(), "other-workspace")
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll other workspace: %v", err)
	}
	initGitRepo(t, otherRoot)
	otherWorkspace, err := env.store.AttachWorkspaceToProject(env.ctx, env.binding.ProjectID, otherRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, materialized.Worktree.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	record.WorkspaceID = otherWorkspace.WorkspaceID
	if err := env.store.UpsertWorktreeRecord(env.ctx, record); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	taskRow, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if taskRow.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree id = %+v, want missing binding", taskRow.ManagedWorktreeID)
	}
	if !taskRow.SourceWorkspaceID.Valid || taskRow.SourceWorkspaceID.String != env.binding.WorkspaceID {
		t.Fatalf("task source workspace = %+v, want %q", taskRow.SourceWorkspaceID, env.binding.WorkspaceID)
	}
	persistedRecord, err := env.store.GetWorktreeRecordByCanonicalRoot(env.ctx, materialized.Worktree.CanonicalRoot)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByCanonicalRoot: %v", err)
	}
	if persistedRecord.WorkspaceID != otherWorkspace.WorkspaceID || otherWorkspace.WorkspaceID == env.binding.WorkspaceID {
		t.Fatalf("persisted worktree workspace = %q, other workspace = %q, source workspace = %q", persistedRecord.WorkspaceID, otherWorkspace.WorkspaceID, env.binding.WorkspaceID)
	}
	if info, statErr := os.Stat(materialized.Worktree.CanonicalRoot); statErr != nil || !info.IsDir() {
		t.Fatalf("deterministic root unavailable before restore: info=%v err=%v", info, statErr)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseConflict {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want conflict locked target error", err)
	}
}

func TestMaterializeInitialTaskWorktreeCreatesShortIDBranchWithoutControllerLease(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	if resp.Worktree.WorktreeID == "" {
		t.Fatalf("worktree response = %+v", resp.Worktree)
	}
	if !resp.Created || !resp.CreatedBranch {
		t.Fatalf("created flags = created:%t branch:%t, want true/true", resp.Created, resp.CreatedBranch)
	}
	if !resp.Worktree.Managed || !resp.Worktree.CreatedBranch {
		t.Fatalf("worktree provenance = %+v, want managed created branch", resp.Worktree)
	}
	if resp.Worktree.BranchName != task.ShortID {
		t.Fatalf("branch name = %q, want task short id %q", resp.Worktree.BranchName, task.ShortID)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", task.ShortID); !strings.Contains(got, task.ShortID) {
		t.Fatalf("branch list = %q, want task branch %q", got, task.ShortID)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != resp.Worktree.WorktreeID {
		t.Fatalf("task managed worktree id = %+v, want %q", row.ManagedWorktreeID, resp.Worktree.WorktreeID)
	}
}

func TestMaterializeInitialTaskWorktreeCreatesFromResolvedCommitAndRecordsImmutableBase(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	resolvedBase, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, "after-resolution.txt"), []byte("advance source\n"), 0o644); err != nil {
		t.Fatalf("write source advancement: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", "after-resolution.txt")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "advance source after resolution")

	resp, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedBase,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	if !resp.Created {
		t.Fatalf("materialized response = %+v, want newly created candidate", resp)
	}
	if got := runGit(t, resp.Worktree.CanonicalRoot, "rev-parse", "HEAD"); got != resolvedBase.CommitOID {
		t.Fatalf("worktree HEAD = %q, want resolved base %q", got, resolvedBase.CommitOID)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, resp.Worktree.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if record.CreationBaseCommitOID == nil || *record.CreationBaseCommitOID != resolvedBase.CommitOID {
		t.Fatalf("creation base commit oid = %v, want %q", record.CreationBaseCommitOID, resolvedBase.CommitOID)
	}
}

func TestMaterializeInitialTaskWorktreeRunsSetupAndPublishesProgressBeforeReturning(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	startedPath := filepath.Join(t.TempDir(), "started")
	releasePath := filepath.Join(t.TempDir(), "release")
	markerPath := filepath.Join(t.TempDir(), "marker")
	payloadPath := filepath.Join(t.TempDir(), "payload.json")
	scriptRelpath := filepath.Join("scripts", "task-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nprintf started > %q\ncat > %q\nwhile [ ! -f %q ]; do sleep 0.02; done\nprintf marker > %q\n", startedPath, payloadPath, releasePath, markerPath))
	env.service.setupScript = scriptRelpath
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	type materializationResult struct {
		resp TaskWorktreeMaterialization
		err  error
	}
	resultCh := make(chan materializationResult, 1)
	go func() {
		resp, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
			TaskID:           task.ID,
			SetupOperationID: setupID,
			ResolvedTarget:   resolvedTarget,
		})
		resultCh <- materializationResult{resp: resp, err: err}
	}()

	if got := waitForFileText(t, startedPath); got != "started" {
		t.Fatalf("started marker = %q, want started", got)
	}
	evt, err := sub.Next(env.ctx)
	if err != nil {
		t.Fatalf("setup event: %v", err)
	}
	if evt.Phase != serverapi.WorktreeSetupPhaseStarted || evt.SetupOperationID != setupID || evt.ScriptPath == "" || evt.WorktreeRoot == "" {
		t.Fatalf("started setup event = %+v", evt)
	}
	select {
	case result := <-resultCh:
		t.Fatalf("MaterializeInitialTaskWorktree returned before setup release: resp=%+v err=%v", result.resp, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o644); err != nil {
		t.Fatalf("release setup: %v", err)
	}
	var result materializationResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for MaterializeInitialTaskWorktree")
	}
	if result.err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", result.err)
	}
	if got := waitForFileText(t, markerPath); got != "marker" {
		t.Fatalf("setup marker = %q, want marker", got)
	}
	payload := waitForSetupPayload(t, payloadPath)
	if payload.SourceWorkspaceRoot != env.workspaceRoot || payload.WorktreeRoot != result.resp.Worktree.CanonicalRoot {
		t.Fatalf("setup payload = %+v, want source %q worktree %q", payload, env.workspaceRoot, result.resp.Worktree.CanonicalRoot)
	}
}

func TestMaterializeInitialTaskWorktreeSetupOmitsStaleParentSessionEnvironment(t *testing.T) {
	env := newServiceTestEnv(t)
	t.Setenv(setupEnvironmentKeySessionID, "stale-parent-session")
	t.Setenv(setupEnvironmentKeyWorktreeRoot, "stale-parent-worktree")
	task, _ := createTaskWorktreeTestTask(t, env)
	capture := worktreesetup.New(t, worktreesetup.Options{})
	env.service.setupScript = capture.Executable()
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	invocation, err := capture.Invocation()
	if err != nil {
		t.Fatalf("setup invocation: %v", err)
	}
	payload, err := invocation.Payload()
	if err != nil {
		t.Fatalf("setup payload: %v", err)
	}
	if payload.SessionID != nil {
		t.Fatalf("workflow setup session_id = %q, want nil", *payload.SessionID)
	}
	if err := invocation.Verify(worktreesetup.Payload{
		SourceWorkspaceRoot: env.workspaceRoot,
		BranchName:          task.ShortID,
		WorktreeRoot:        resp.Worktree.CanonicalRoot,
		ProjectID:           env.binding.ProjectID,
		WorkspaceID:         env.binding.WorkspaceID,
		WorktreeID:          resp.Worktree.WorktreeID,
		CreatedBranch:       resp.CreatedBranch,
	}); err != nil {
		t.Fatalf("workflow setup contract: %v", err)
	}
}

func TestCreateWorktreeSetupReplacesStaleParentReservedEnvironment(t *testing.T) {
	env := newServiceTestEnv(t)
	t.Setenv(setupEnvironmentKeySessionID, "stale-parent-session")
	t.Setenv(setupEnvironmentKeyWorktreeRoot, "stale-parent-worktree")
	capture := worktreesetup.New(t, worktreesetup.Options{})
	env.service.setupScript = capture.Executable()

	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "req-session-contract",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/session-contract",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	invocation, err := capture.Invocation()
	if err != nil {
		t.Fatalf("setup invocation: %v", err)
	}
	payload, err := invocation.Payload()
	if err != nil {
		t.Fatalf("setup payload: %v", err)
	}
	if payload.SessionID == nil || *payload.SessionID != env.session.Meta().SessionID {
		t.Fatalf("session setup session_id = %v, want %q", payload.SessionID, env.session.Meta().SessionID)
	}
	if err := invocation.Verify(worktreesetup.Payload{
		SourceWorkspaceRoot: env.workspaceRoot,
		BranchName:          "feature/session-contract",
		WorktreeRoot:        resp.Worktree.CanonicalRoot,
		SessionID:           payload.SessionID,
		ProjectID:           env.binding.ProjectID,
		WorkspaceID:         env.binding.WorkspaceID,
		WorktreeID:          resp.Worktree.WorktreeID,
		CreatedBranch:       resp.CreatedBranch,
	}); err != nil {
		t.Fatalf("session setup contract: %v", err)
	}
}

func TestMaterializeInitialTaskWorktreeReturnsExistingManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}

	first, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{TaskID: task.ID, ResolvedTarget: base})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree first: %v", err)
	}
	second, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{TaskID: task.ID, ResolvedTarget: base})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree second: %v", err)
	}
	if second.Created || second.CreatedBranch {
		t.Fatalf("second ensure created flags = created:%t branch:%t, want false/false", second.Created, second.CreatedBranch)
	}
	if first.Worktree.WorktreeID != second.Worktree.WorktreeID {
		t.Fatalf("second worktree id = %q, want %q", second.Worktree.WorktreeID, first.Worktree.WorktreeID)
	}
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, "incompatible.txt"), []byte("new base\n"), 0o644); err != nil {
		t.Fatalf("write source advancement: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", "incompatible.txt")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "change base")
	incompatible, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD after advance: %v", err)
	}
	_, err = env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{TaskID: task.ID, ResolvedTarget: incompatible})
	var mismatch *TaskWorktreeBaseCommitMismatchError
	if !errors.As(err, &mismatch) || mismatch.RequestedCommitOID != incompatible.CommitOID || mismatch.CreationBaseCommitOID == nil || *mismatch.CreationBaseCommitOID != base.CommitOID {
		t.Fatalf("incompatible MaterializeInitialTaskWorktree error = %v, want typed base mismatch", err)
	}
}

func TestMaterializeInitialTaskWorktreeFailureRetryTrustsExistingWorktreeAndRecreatesRemovedRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "retry-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then exit 3; fi\n", countPath, countPath, countPath))
	env.service.setupScript = scriptRelpath

	_, err = env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{TaskID: task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID(), ResolvedTarget: base})
	if err == nil {
		t.Fatal("first MaterializeInitialTaskWorktree succeeded, want setup failure")
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || strings.TrimSpace(row.ManagedWorktreeID.String) == "" {
		t.Fatalf("managed worktree not attached after setup failure: %+v", row.ManagedWorktreeID)
	}
	if row.ExecutionTargetMode.Valid {
		t.Fatalf("failed setup locked execution target = %+v, want task remain unlocked", row.ExecutionTargetMode)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, row.ManagedWorktreeID.String)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if _, err := os.Stat(record.CanonicalRoot); err != nil {
		t.Fatalf("failed setup worktree root unavailable: %v", err)
	}
	if got := waitForFileText(t, countPath); got != "1" {
		t.Fatalf("setup run count after failure = %q, want 1", got)
	}

	restarted := NewService(
		env.store,
		env.service.git,
		env.runtime,
		env.runtime,
		env.processes,
		env.localNotes,
		ServiceOptions{BaseDir: env.baseDir, SetupScript: scriptRelpath},
	)
	second, err := restarted.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{TaskID: task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID(), ResolvedTarget: base})
	if err != nil {
		t.Fatalf("second MaterializeInitialTaskWorktree should trust existing root: %v", err)
	}
	if second.Created {
		t.Fatalf("second ensure created worktree, want existing trusted: %+v", second)
	}
	if got := waitForFileText(t, countPath); got != "1" {
		t.Fatalf("setup reran for existing worktree, count=%q", got)
	}

	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, record.CanonicalRoot, true); err != nil {
		t.Fatalf("remove stale worktree root: %v", err)
	}
	third, err := restarted.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{TaskID: task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID(), ResolvedTarget: base})
	if err != nil {
		t.Fatalf("third MaterializeInitialTaskWorktree should recreate removed root: %v", err)
	}
	if !third.Created {
		t.Fatalf("third ensure did not recreate worktree: %+v", third)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup run count after recreate = %q, want 2", got)
	}
}

func TestMaterializeInitialTaskWorktreeUsesTaskSourceWorkspace(t *testing.T) {
	env := newServiceTestEnv(t)
	sourceRoot := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll source root: %v", err)
	}
	initGitRepo(t, sourceRoot)
	source, err := env.store.AttachWorkspaceToProject(env.ctx, env.binding.ProjectID, sourceRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}
	task, _ := createTaskWorktreeTestTaskWithSource(t, env, source.WorkspaceID)
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, sourceRoot)

	resp, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	if resp.Worktree.WorktreeID == "" || !strings.Contains(resp.Worktree.CanonicalRoot, source.WorkspaceID) {
		t.Fatalf("worktree = %+v, want root under source workspace id %q", resp.Worktree, source.WorkspaceID)
	}
	if got := runGit(t, sourceRoot, "branch", "--list", task.ShortID); !strings.Contains(got, task.ShortID) {
		t.Fatalf("source branch list = %q, want task branch %q", got, task.ShortID)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", task.ShortID); strings.Contains(got, task.ShortID) {
		t.Fatalf("primary branch list = %q, did not expect task branch %q", got, task.ShortID)
	}
}

func TestMaterializeInitialTaskWorktreeHandlesRootCollisionAndReportsBranchCollision(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	baseRoot, err := defaultWorktreeRoot(env.baseDir, env.binding.WorkspaceID, task.ShortID)
	if err != nil {
		t.Fatalf("defaultWorktreeRoot: %v", err)
	}
	if err := os.MkdirAll(baseRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll collision root: %v", err)
	}
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree root collision: %v", err)
	}
	if resp.Worktree.CanonicalRoot == baseRoot {
		t.Fatalf("worktree root = %q, want suffixed root because base exists", resp.Worktree.CanonicalRoot)
	}
	if !strings.HasSuffix(resp.Worktree.CanonicalRoot, filepath.Base(baseRoot)+"-2") {
		t.Fatalf("worktree root = %q, want -2 suffix from existing collision behavior", resp.Worktree.CanonicalRoot)
	}

	otherTask, _ := createTaskWorktreeTestTask(t, env)
	runGit(t, env.workspaceRoot, "branch", otherTask.ShortID)
	_, err = env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         otherTask.ID,
		ResolvedTarget: resolvedTarget,
	})
	var branchCollision *TaskBranchCollisionError
	if !errors.As(err, &branchCollision) || branchCollision.BranchName != otherTask.ShortID {
		t.Fatalf("MaterializeInitialTaskWorktree branch collision error = %v, want task branch collision", err)
	}
}

func TestDeleteWorktreeBlocksNonTerminalTaskManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	created, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}

	_, err = env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		ClientRequestID: "req-delete-task-worktree",
		SessionID:       env.session.Meta().SessionID,
		WorktreeID:      created.Worktree.WorktreeID,
	})
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree error = %v, want ErrWorktreeBlocked", err)
	}
}

func TestDeleteWorktreeAllowsTerminalTaskManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	created, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	started, err := workflowStore.StartTask(env.ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := workflowStore.CompleteRun(env.ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	_, err = env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		ClientRequestID: "req-delete-terminal-task-worktree",
		SessionID:       env.session.Meta().SessionID,
		WorktreeID:      created.Worktree.WorktreeID,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree terminal task worktree: %v", err)
	}
	if _, err := os.Stat(created.Worktree.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected task worktree removed, stat err=%v", err)
	}
}

func TestDeleteTaskWorktreeRemovesManagedWorktreeAndBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	created, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}

	resp, err := env.service.DeleteTaskWorktree(env.ctx, DeleteTaskWorktreeRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("DeleteTaskWorktree: %v", err)
	}
	if !resp.Deleted || resp.WorktreeID != created.Worktree.WorktreeID || !resp.BranchDeleted {
		t.Fatalf("DeleteTaskWorktree response = %+v, want deleted worktree and branch", resp)
	}
	if _, err := os.Stat(created.Worktree.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected task worktree removed, stat err=%v", err)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", task.ShortID); strings.Contains(got, task.ShortID) {
		t.Fatalf("branch list = %q, did not expect task branch %q", got, task.ShortID)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree id = %+v, want cleared after worktree record delete", row.ManagedWorktreeID)
	}
}

func createTaskWorktreeTestTask(t *testing.T, env *serviceTestEnv) (workflowstore.TaskRecord, *workflowstore.Store) {
	t.Helper()
	return createTaskWorktreeTestTaskWithSource(t, env, "")
}

func resolveTaskWorktreeTestHEAD(t *testing.T, env *serviceTestEnv, root string) GitRevision {
	t.Helper()
	resolvedTarget, err := env.service.git.ResolveHEAD(env.ctx, root)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	return resolvedTarget
}

func materializeAndLockTaskWorktree(t *testing.T, env *serviceTestEnv) (workflowstore.TaskRecord, TaskWorktreeMaterialization, GitRevision) {
	t.Helper()
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	materialized, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	lockTaskWorktreeExecutionTarget(t, env, workflowStore, task, materialized, resolvedTarget)
	return task, materialized, resolvedTarget
}

func lockTaskWorktreeExecutionTarget(t *testing.T, env *serviceTestEnv, store *workflowstore.Store, task workflowstore.TaskRecord, materialized TaskWorktreeMaterialization, resolvedTarget GitRevision) {
	t.Helper()
	requestedRef := resolvedTarget.RequestedRef
	commitOID := resolvedTarget.CommitOID
	_, err := store.StartTaskWithExecutionTarget(env.ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			ResolvedRef:  resolvedTarget.CanonicalRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   env.binding.WorkspaceID,
			SourceWorkspaceRoot: env.workspaceRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: materialized.Worktree.WorktreeID,
				Root:       materialized.Worktree.CanonicalRoot,
			},
		},
	})
	if err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
}

type selectedCommandFailingGitRunner struct {
	base      gitCommandRunner
	directory string
	arguments []string
}

func (r *selectedCommandFailingGitRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	output, exitCode, err := r.Run(ctx, dir, args...)
	if err != nil {
		return nil, formatGitRunError(exitCode, err, output, args...)
	}
	return output, nil
}

func (r *selectedCommandFailingGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	if strings.TrimSpace(dir) == strings.TrimSpace(r.directory) && slices.Equal(args, r.arguments) {
		return []byte("injected Git failure"), 2, errors.New("injected Git failure")
	}
	return r.base.Run(ctx, dir, args...)
}

func createTaskWorktreeTestTaskWithSource(t *testing.T, env *serviceTestEnv, sourceWorkspaceID string) (workflowstore.TaskRecord, *workflowstore.Store) {
	t.Helper()
	resolver := workflow.StaticRoleResolver{"workflow-test": true}
	store, err := workflowstore.New(env.store, workflowstore.WithRoleResolver(resolver))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	created, err := store.CreateWorkflow(env.ctx, workflowstore.CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(env.ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	startID := taskWorktreeNodeIDByKind(t, def, workflow.NodeKindStart)
	doneID := taskWorktreeNodeIDByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddNode(env.ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "implement", Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: "workflow-test", PromptTemplate: "Do work"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(env.ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(env.ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(created.ID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work"}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(env.ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(env.ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(created.ID)), Key: "done", TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	if _, err := store.LinkWorkflow(env.ctx, env.binding.ProjectID, created.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := store.CreateTask(env.ctx, workflowstore.CreateTaskRequest{ProjectID: env.binding.ProjectID, Title: "Task", Body: "Body", SourceWorkspaceID: sourceWorkspaceID})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task, store
}

func taskWorktreeNodeIDByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.NodeID {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind() == kind {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatalf("node kind %q not found", kind)
	return ""
}
