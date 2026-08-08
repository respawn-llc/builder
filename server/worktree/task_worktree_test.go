package worktree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/serverapi"
)

func taskWorktreeID(entry serverapi.WorktreeTopologyEntry) string {
	return entry.Registered.Kent.WorktreeID
}

func taskWorktreeRoot(entry serverapi.WorktreeTopologyEntry) string {
	return entry.Registered.Git.CanonicalRoot
}

func taskWorktreeBranch(entry serverapi.WorktreeTopologyEntry) string {
	if entry.Registered.Git.BranchName == nil {
		return ""
	}
	return *entry.Registered.Git.BranchName
}

func createExistingOutsideManagedWorktree(t *testing.T, env *serviceTestEnv, branch string) (string, GitRevision, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "legacy")
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", branch, root, "HEAD")
	canonicalRoot, err := config.CanonicalWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	worktrees, err := env.service.git.List(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("git.List: %v", err)
	}
	for _, worktree := range worktrees {
		if worktree.Root != canonicalRoot {
			continue
		}
		head, err := env.service.git.ResolveHEAD(env.ctx, canonicalRoot)
		if err != nil {
			t.Fatalf("ResolveHEAD: %v", err)
		}
		gitMetadata, err := marshalGitMetadata(worktree)
		if err != nil {
			t.Fatalf("marshalGitMetadata: %v", err)
		}
		return canonicalRoot, head, gitMetadata
	}
	t.Fatalf("created outside managed Worktree %q was not listed", canonicalRoot)
	return "", GitRevision{}, ""
}

func prepareManagedTaskExecutionRoot(
	ctx context.Context,
	service *Service,
	taskID workflow.TaskID,
	setupOperationID *serverapi.WorktreeSetupOperationID,
	resolvedTarget GitRevision,
) (TaskWorktreeMaterialization, error) {
	prepared, err := service.PrepareTaskExecutionRoot(ctx, TaskExecutionRootPreparationRequest{
		TaskID:           taskID,
		SetupOperationID: setupOperationID,
		ManagedTarget:    &resolvedTarget,
	})
	if prepared.Materialization == nil {
		return TaskWorktreeMaterialization{}, err
	}
	return *prepared.Materialization, err
}

func TestPrepareTaskExecutionRootReusesProvisionalWorktreeForSameCommit(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &target,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	secondTarget := target
	secondTarget.RequestedRef = "same-commit-selector"
	second, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &secondTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot second: %v", err)
	}
	if first.Root.Managed == nil || second.Root.Managed == nil {
		t.Fatalf("prepared roots = %+v then %+v, want managed roots", first.Root, second.Root)
	}
	if *first.Root.Managed != *second.Root.Managed {
		t.Fatalf("second root = %+v, want reuse of %+v", second.Root.Managed, first.Root.Managed)
	}
	if second.RetainedPreviousWorktree != nil {
		t.Fatalf("same-commit preparation retained previous worktree: %+v", second.RetainedPreviousWorktree)
	}
}

func TestPrepareTaskExecutionRootReplacesDifferentCommitCleanWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	firstTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &firstTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, "next-target.txt"), []byte("next\n"), 0o644); err != nil {
		t.Fatalf("write next target: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", "next-target.txt")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "next target")
	nextTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	replacement, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &nextTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot replacement: %v", err)
	}
	if replacement.Root.Managed == nil || first.Root.Managed == nil ||
		replacement.Root.Managed.WorktreeID == first.Root.Managed.WorktreeID {
		t.Fatalf("replacement roots = first:%+v replacement:%+v", first.Root, replacement.Root)
	}
	if replacement.RetainedPreviousWorktree != nil {
		t.Fatalf("clean replacement retained previous worktree: %+v", replacement.RetainedPreviousWorktree)
	}
	if _, err := os.Stat(first.Root.Managed.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean previous root remains: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, first.Root.Managed.WorktreeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("clean previous record remains: %v", err)
	}
	identity, err := env.service.git.ValidateManagedWorktreeIdentity(env.ctx, ManagedWorktreeIdentitySpec{
		SourceWorkspaceRoot:  env.workspaceRoot,
		ExpectedWorktreeRoot: replacement.Root.Managed.Root,
	})
	if err != nil {
		t.Fatalf("ValidateManagedWorktreeIdentity: %v", err)
	}
	branch, named := identity.NamedBranch()
	if !named {
		t.Fatal("replacement worktree has no named branch")
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, branch); branchErr != nil {
		t.Fatalf("BranchExists: %v", branchErr)
	} else if !exists {
		t.Fatalf("replacement branch %q is absent", branch)
	}
}

func TestPrepareTaskExecutionRootRetainsDifferentCommitCleanAdvancedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	firstTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &firstTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	if first.Root.Managed == nil {
		t.Fatalf("first preparation root = %+v, want managed", first.Root)
	}
	advancedPath := filepath.Join(first.Root.Managed.Root, "operator-commit.txt")
	if err := os.WriteFile(advancedPath, []byte("preserve me\n"), 0o644); err != nil {
		t.Fatalf("write operator commit: %v", err)
	}
	runGit(t, first.Root.Managed.Root, "add", "operator-commit.txt")
	runGit(t, first.Root.Managed.Root, "commit", "-q", "-m", "operator commit")
	advancedCommit := runGit(t, first.Root.Managed.Root, "rev-parse", "HEAD")
	nextTarget := advanceTaskWorktreeTestTarget(t, env)

	replacement, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &nextTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot replacement: %v", err)
	}
	if replacement.RetainedPreviousWorktree == nil {
		t.Fatal("clean advanced worktree was released instead of retained")
	}
	if got := runGit(t, first.Root.Managed.Root, "rev-parse", "HEAD"); got != advancedCommit {
		t.Fatalf("retained worktree HEAD = %q, want operator commit %q", got, advancedCommit)
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, task.ShortID); branchErr != nil {
		t.Fatalf("BranchExists: %v", branchErr)
	} else if !exists {
		t.Fatalf("operator branch %q was deleted", task.ShortID)
	}
}

func TestPrepareTaskExecutionRootOrphansDifferentCommitChangedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	firstTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &firstTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	if first.Root.Managed == nil {
		t.Fatalf("first preparation root = %+v, want managed", first.Root)
	}
	changedPath := filepath.Join(first.Root.Managed.Root, "operator-change.txt")
	if err := os.WriteFile(changedPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("change provisional worktree: %v", err)
	}
	nextTarget := advanceTaskWorktreeTestTarget(t, env)

	replacement, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &nextTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot replacement: %v", err)
	}
	if replacement.Root.Managed == nil ||
		replacement.Root.Managed.WorktreeID == first.Root.Managed.WorktreeID {
		t.Fatalf("replacement roots = first:%+v replacement:%+v", first.Root, replacement.Root)
	}
	if replacement.RetainedPreviousWorktree == nil ||
		replacement.RetainedPreviousWorktree.Worktree.Registered == nil ||
		replacement.RetainedPreviousWorktree.Worktree.Registered.Kent.WorktreeID != first.Root.Managed.WorktreeID {
		t.Fatalf("retained previous worktree = %+v, want %q", replacement.RetainedPreviousWorktree, first.Root.Managed.WorktreeID)
	}
	if got := waitForFileText(t, changedPath); got != "keep me" {
		t.Fatalf("orphaned worktree change = %q, want preserved", got)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, first.Root.Managed.WorktreeID); err != nil {
		t.Fatalf("orphaned worktree record: %v", err)
	}
	listed, err := env.service.ListWorkspaceWorktrees(env.ctx, serverapi.WorktreeWorkspaceListRequest{
		ProjectID:   env.binding.ProjectID,
		WorkspaceID: env.binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("ListWorkspaceWorktrees after replacement: %v", err)
	}
	orphanListed := false
	for _, entry := range listed.Worktrees {
		if entry.Topology.Registered != nil &&
			entry.Topology.Registered.Kent.WorktreeID == first.Root.Managed.WorktreeID &&
			entry.Topology.Registered.Kent.CanonicalRoot == first.Root.Managed.Root {
			orphanListed = true
			break
		}
	}
	if !orphanListed {
		t.Fatalf("workspace Worktree list omitted retained orphan %q at %q", first.Root.Managed.WorktreeID, first.Root.Managed.Root)
	}
	assertTaskManagedWorktree(t, env, task.ID, replacement.Root.Managed.WorktreeID)
}

func TestPrepareTaskExecutionRootSwitchesToNoManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &target,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot managed: %v", err)
	}
	if first.Root.Managed == nil {
		t.Fatalf("managed preparation root = %+v", first.Root)
	}
	changedPath := filepath.Join(first.Root.Managed.Root, "operator-change.txt")
	if err := os.WriteFile(changedPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("change provisional worktree: %v", err)
	}

	unmanaged, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot none: %v", err)
	}
	if unmanaged.Root.Managed != nil ||
		unmanaged.Root.EffectiveRoot() != env.workspaceRoot ||
		unmanaged.Materialization != nil ||
		unmanaged.SetupResult != nil {
		t.Fatalf("unmanaged preparation = %+v, want source execution root only", unmanaged)
	}
	if unmanaged.RetainedPreviousWorktree == nil ||
		unmanaged.RetainedPreviousWorktree.Worktree.Registered == nil ||
		unmanaged.RetainedPreviousWorktree.Worktree.Registered.Kent.WorktreeID != first.Root.Managed.WorktreeID {
		t.Fatalf("retained previous worktree = %+v, want %q", unmanaged.RetainedPreviousWorktree, first.Root.Managed.WorktreeID)
	}
	if got := waitForFileText(t, changedPath); got != "keep me" {
		t.Fatalf("unmanaged switch lost previous change: %q", got)
	}
	assertTaskExecutionTargetUnlocked(t, env, task.ID)
}

func TestPrepareTaskExecutionRootFailedReplacementAfterCleanRemovalLeavesNoProvisionalRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	firstTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &firstTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	nextTarget := advanceTaskWorktreeTestTarget(t, env)
	addErr := errors.New("replacement add failed")
	env.service.git = NewGitInspector(&recordingGitCommandRunner{
		delegate:        execGitCommandRunner{},
		failWorktreeAdd: addErr,
	})

	prepared, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &nextTarget,
	})
	if !errors.Is(err, addErr) {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want %v", err, addErr)
	}
	if prepared.Root.Managed != nil || prepared.Materialization != nil || prepared.RetainedPreviousWorktree != nil {
		t.Fatalf("failed clean replacement preparation = %+v, want no provisional root", prepared)
	}
	assertTaskExecutionTargetUnlocked(t, env, task.ID)
	if _, err := os.Stat(first.Root.Managed.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean previous root remains after failed replacement: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, first.Root.Managed.WorktreeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("clean previous record remains after failed replacement: %v", err)
	}
}

func TestPrepareTaskExecutionRootFailedReplacementAfterOrphaningKeepsOldWorktreeUnbound(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	firstTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &firstTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	changedPath := filepath.Join(first.Root.Managed.Root, "operator-change.txt")
	if err := os.WriteFile(changedPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("change provisional worktree: %v", err)
	}
	nextTarget := advanceTaskWorktreeTestTarget(t, env)
	addErr := errors.New("replacement add failed")
	env.service.git = NewGitInspector(&recordingGitCommandRunner{
		delegate:        execGitCommandRunner{},
		failWorktreeAdd: addErr,
	})

	prepared, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &nextTarget,
	})
	if !errors.Is(err, addErr) {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want %v", err, addErr)
	}
	if prepared.Root.Managed != nil || prepared.Materialization != nil {
		t.Fatalf("failed orphan replacement preparation = %+v, want no replacement root", prepared)
	}
	if prepared.RetainedPreviousWorktree == nil ||
		prepared.RetainedPreviousWorktree.Worktree.Registered == nil ||
		prepared.RetainedPreviousWorktree.Worktree.Registered.Kent.WorktreeID != first.Root.Managed.WorktreeID {
		t.Fatalf("retained previous worktree = %+v, want %q", prepared.RetainedPreviousWorktree, first.Root.Managed.WorktreeID)
	}
	assertTaskExecutionTargetUnlocked(t, env, task.ID)
	if got := waitForFileText(t, changedPath); got != "keep me" {
		t.Fatalf("orphaned worktree change = %q, want preserved", got)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, first.Root.Managed.WorktreeID); err != nil {
		t.Fatalf("orphaned worktree record: %v", err)
	}
}

func TestPrepareTaskExecutionRootReplacementSetupFailureRetainsNewAndPreviousWorktrees(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	firstTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &firstTarget,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	oldBranch := taskWorktreeBranch(first.Materialization.Worktree)
	changedPath := filepath.Join(first.Root.Managed.Root, "operator-change.txt")
	if err := os.WriteFile(changedPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("change provisional worktree: %v", err)
	}
	nextTarget := advanceTaskWorktreeTestTarget(t, env)
	scriptRelpath := filepath.Join("scripts", "replacement-fails-twice.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), "#!/bin/sh\nexit 7\n")
	env.service.setupScript = scriptRelpath

	prepared, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &nextTarget,
	})
	var retained *serverapi.WorktreeSetupRetainedError
	if !errors.As(err, &retained) || retained.Worktree.Registered == nil {
		t.Fatalf("PrepareTaskExecutionRoot error = %T %v, want retained replacement", err, err)
	}
	if prepared.Root.Managed == nil ||
		prepared.Materialization == nil ||
		prepared.SetupResult == nil ||
		prepared.SetupResult.Failed == nil {
		t.Fatalf("failed replacement preparation = %+v, want retained managed result", prepared)
	}
	if prepared.RetainedPreviousWorktree == nil ||
		retained.RetainedPreviousWorktree == nil ||
		prepared.RetainedPreviousWorktree.Worktree.Registered == nil ||
		prepared.RetainedPreviousWorktree.Worktree.Registered.Kent.WorktreeID != first.Root.Managed.WorktreeID ||
		retained.RetainedPreviousWorktree.Worktree.Registered == nil ||
		retained.RetainedPreviousWorktree.Worktree.Registered.Kent.WorktreeID != first.Root.Managed.WorktreeID {
		t.Fatalf("retained previous topology = preparation:%+v error:%+v", prepared.RetainedPreviousWorktree, retained.RetainedPreviousWorktree)
	}
	newWorktreeID := prepared.Root.Managed.WorktreeID
	if retained.Worktree.Registered.Kent.WorktreeID != newWorktreeID {
		t.Fatalf("retained primary worktree = %q, want %q", retained.Worktree.Registered.Kent.WorktreeID, newWorktreeID)
	}
	assertTaskManagedWorktree(t, env, task.ID, newWorktreeID)
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, first.Root.Managed.WorktreeID); err != nil {
		t.Fatalf("previous orphan record: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, newWorktreeID); err != nil {
		t.Fatalf("replacement record: %v", err)
	}
	newBranch := taskWorktreeBranch(prepared.Materialization.Worktree)
	for _, branch := range []string{oldBranch, newBranch} {
		exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, branch)
		if err != nil {
			t.Fatalf("inspect branch %q: %v", branch, err)
		}
		if !exists {
			t.Fatalf("retained branch %q was deleted", branch)
		}
	}
	if got := waitForFileText(t, changedPath); got != "keep me" {
		t.Fatalf("previous orphan change = %q, want preserved", got)
	}
}

func TestPrepareTaskExecutionRootRollsBackReservationWhenGitCreationFails(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	addErr := errors.New("injected task worktree add failure")
	env.service.git = NewGitInspector(&recordingGitCommandRunner{
		delegate:        execGitCommandRunner{},
		failWorktreeAdd: addErr,
	})

	_, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &target,
	})
	if !errors.Is(err, addErr) {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want %v", err, addErr)
	}
	assertFailedTaskWorktreeCreationRolledBack(t, env, task)
}

func TestPrepareTaskExecutionRootReleasesReservationWhenSetupSettingsResolutionFails(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	settingsErr := errors.New("injected setup settings failure")
	env.service.resolveSetup = func(string) (config.WorktreeSettings, error) {
		return config.WorktreeSettings{}, settingsErr
	}

	_, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &target,
	})
	if !errors.Is(err, settingsErr) {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want %v", err, settingsErr)
	}
	assertFailedTaskWorktreeCreationRolledBack(t, env, task)
}

func TestPrepareTaskExecutionRootAllocatorFailureLeavesNoProvisionalState(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	parent, err := env.service.managedRoots.ensureWorkspaceParent(env.workspaceRoot)
	if err != nil {
		t.Fatalf("ensure managed worktree parent: %v", err)
	}
	existingRoot := filepath.Join(parent, task.ShortID)
	if err := os.Mkdir(existingRoot, 0o755); err != nil {
		t.Fatalf("create exact-root collision: %v", err)
	}
	reservationErr := errors.New("injected allocator entropy failure")
	env.service.managedRoots = newManagedRootAllocator(env.baseDir, errorReader{err: reservationErr})

	prepared, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &target,
	})
	if !errors.Is(err, reservationErr) {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want %v", err, reservationErr)
	}
	if prepared.Root.Managed != nil || prepared.Materialization != nil {
		t.Fatalf("allocator failure preparation = %+v, want no provisional root", prepared)
	}
	assertTaskExecutionTargetUnlocked(t, env, task.ID)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read managed worktree parent: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != task.ShortID {
		t.Fatalf("managed worktree parent entries = %+v, want only pre-existing exact collision", entries)
	}
	exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, task.ShortID)
	if err != nil {
		t.Fatalf("inspect failed Task branch: %v", err)
	}
	if exists {
		t.Fatalf("allocator failure created Task branch %q", task.ShortID)
	}
	records, err := env.store.ListWorktreeRecordsByWorkspaceID(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("list worktree records: %v", err)
	}
	for _, record := range records {
		if record.Managed {
			t.Fatalf("allocator failure left managed worktree record: %+v", record)
		}
	}
}

func TestPrepareTaskExecutionRootRollsBackGitCreationWhenRegistrationFails(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	if _, err := env.store.DB().ExecContext(env.ctx, `
CREATE TRIGGER test_fail_task_worktree_registration
BEFORE INSERT ON worktrees
BEGIN
    SELECT RAISE(FAIL, 'injected task worktree registration failure');
END`); err != nil {
		t.Fatalf("create registration failure trigger: %v", err)
	}

	if _, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &target,
	}); err == nil {
		t.Fatal("PrepareTaskExecutionRoot succeeded, want registration failure")
	}
	assertFailedTaskWorktreeCreationRolledBack(t, env, task)
}

func TestPrepareTaskExecutionRootRollsBackRegistrationWhenBindingFails(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	if _, err := env.store.DB().ExecContext(env.ctx, `
CREATE TRIGGER test_fail_task_worktree_binding
BEFORE UPDATE OF managed_worktree_id ON tasks
WHEN NEW.managed_worktree_id IS NOT NULL
BEGIN
    SELECT RAISE(FAIL, 'injected task worktree binding failure');
END`); err != nil {
		t.Fatalf("create binding failure trigger: %v", err)
	}

	if _, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:        task.ID,
		ManagedTarget: &target,
	}); err == nil {
		t.Fatal("PrepareTaskExecutionRoot succeeded, want binding failure")
	}
	assertFailedTaskWorktreeCreationRolledBack(t, env, task)
}

func TestPrepareTaskExecutionRootRequiresResolvedCommit(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	_, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, GitRevision{})
	if err == nil {
		t.Fatal("PrepareTaskExecutionRoot accepted a missing resolved commit")
	}
	row, queryErr := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if queryErr != nil {
		t.Fatalf("GetTask: %v", queryErr)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree id = %+v, want no provisional candidate", row.ManagedWorktreeID)
	}
}

func TestMaterializeInitialTaskWorktreeRejectsExistingOutsideNamespaceRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	legacyRoot, resolved, gitMetadata := createExistingOutsideManagedWorktree(t, env, "legacy-initial")
	creationBase := resolved.CommitOID
	if err := env.store.UpsertWorktreeRecord(env.ctx, metadata.WorktreeRecord{
		ID: "legacy-initial-record", WorkspaceID: env.binding.WorkspaceID, CanonicalRoot: legacyRoot,
		Managed: true, CreatedBranch: true, GitMetadataJSON: gitMetadata,
		CreationBaseCommitOID: &creationBase,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := env.store.Queries().UpdateTaskManagedWorktree(env.ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                string(task.ID),
		ManagedWorktreeID: sql.NullString{String: "legacy-initial-record", Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
	}); err != nil {
		t.Fatalf("UpdateTaskManagedWorktree: %v", err)
	}

	_, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolved,
	})
	if err == nil {
		t.Fatal("MaterializeInitialTaskWorktree accepted an existing root outside the server namespace")
	}
}

func TestRestoreLockedTaskWorktreeRejectsExplicitRootOverlappingSourceWorkspace(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	worktreeID := taskWorktreeID(materialized.Worktree)
	record, err := env.store.GetWorktreeRecordByID(env.ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	originalRoot := record.CanonicalRoot
	record.CanonicalRoot = filepath.Join(env.workspaceRoot, "nested")
	record.UpdatedAt = time.Now().UTC()
	canonicalRoot, err := config.CanonicalWorkspaceRoot(record.CanonicalRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot overlapping root: %v", err)
	}
	if _, err := env.store.Queries().UpdateWorktreeCanonicalRoot(env.ctx, sqlitegen.UpdateWorktreeCanonicalRootParams{
		CanonicalRootPath: canonicalRoot,
		UpdatedAtUnixMs:   record.UpdatedAt.UnixMilli(),
		ID:                record.ID,
	}); err != nil {
		t.Fatalf("UpdateWorktreeCanonicalRoot overlapping root: %v", err)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	if err == nil {
		t.Fatal("RestoreLockedTaskWorktree accepted an explicit root overlapping the source Workspace")
	}
	if _, err := os.Stat(record.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlapping root was materialized: %v", err)
	}
	registered, err := env.service.git.List(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("git.List: %v", err)
	}
	foundOriginal := false
	for _, worktree := range registered {
		if worktree.Root == originalRoot {
			foundOriginal = true
			break
		}
	}
	if !foundOriginal {
		t.Fatalf("original managed Worktree %q disappeared after rejected restore", originalRoot)
	}
}

func TestRestoreLockedTaskWorktreeRejectsExistingOutsideNamespaceRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	originalRoot := taskWorktreeRoot(materialized.Worktree)
	legacyRoot := filepath.Join(t.TempDir(), "legacy")
	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", originalRoot)
	runGit(t, env.workspaceRoot, "worktree", "add", legacyRoot, task.ShortID)
	legacyRoot, err := config.CanonicalWorkspaceRoot(legacyRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot legacy root: %v", err)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, taskWorktreeID(materialized.Worktree))
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if _, err := env.store.Queries().UpdateWorktreeCanonicalRoot(env.ctx, sqlitegen.UpdateWorktreeCanonicalRootParams{
		CanonicalRootPath: legacyRoot,
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		ID:                record.ID,
	}); err != nil {
		t.Fatalf("UpdateWorktreeCanonicalRoot: %v", err)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	if err == nil {
		t.Fatal("RestoreLockedTaskWorktree accepted an existing root outside the server namespace")
	}
	registered, err := env.service.git.List(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("git.List: %v", err)
	}
	for _, worktree := range registered {
		if worktree.Root == legacyRoot {
			return
		}
	}
	t.Fatalf("legacy root %q disappeared after rejected restore", legacyRoot)
}

func TestRestoreLockedTaskWorktreeRejectsLegacyRootOutsideNamespace(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	worktreeID := taskWorktreeID(materialized.Worktree)
	record, err := env.store.GetWorktreeRecordByID(env.ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	originalRoot := record.CanonicalRoot
	legacyRoot := filepath.Join(t.TempDir(), "legacy")
	canonicalRoot, err := config.CanonicalWorkspaceRoot(legacyRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot legacy root: %v", err)
	}
	if _, err := env.store.Queries().UpdateWorktreeCanonicalRoot(env.ctx, sqlitegen.UpdateWorktreeCanonicalRootParams{
		CanonicalRootPath: canonicalRoot,
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		ID:                record.ID,
	}); err != nil {
		t.Fatalf("UpdateWorktreeCanonicalRoot legacy root: %v", err)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	if err == nil {
		t.Fatal("RestoreLockedTaskWorktree accepted a legacy root outside the server namespace")
	}
	if _, err := os.Stat(legacyRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy root was materialized: %v", err)
	}
	registered, err := env.service.git.List(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("git.List: %v", err)
	}
	for _, worktree := range registered {
		if worktree.Root == legacyRoot {
			t.Fatalf("legacy root %q was registered after rejected restore", legacyRoot)
		}
	}
	foundOriginal := false
	for _, worktree := range registered {
		if worktree.Root == originalRoot {
			foundOriginal = true
			break
		}
	}
	if !foundOriginal {
		t.Fatalf("original managed Worktree %q disappeared after rejected restore", originalRoot)
	}
}

func TestRestoreLockedTaskWorktreeAcceptsHealthyChangedNamedBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	runGit(t, taskWorktreeRoot(materialized.Worktree), "branch", "-m", "operator-renamed")

	restored, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("RestoreLockedTaskWorktree: %v", err)
	}
	if restored.Created ||
		taskWorktreeID(restored.Worktree) != taskWorktreeID(materialized.Worktree) ||
		taskWorktreeRoot(restored.Worktree) != taskWorktreeRoot(materialized.Worktree) ||
		taskWorktreeBranch(restored.Worktree) != "operator-renamed" {
		t.Fatalf("restored worktree = %+v, want healthy renamed root reuse", restored)
	}
}

func TestRestoreLockedTaskWorktreeReusesDetachedHeadWithoutRunningSetup(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	worktreeID := taskWorktreeID(materialized.Worktree)
	worktreeRoot := taskWorktreeRoot(materialized.Worktree)
	runGit(t, worktreeRoot, "checkout", "--detach")
	detachedRevision := resolveTaskWorktreeTestHEAD(t, env, worktreeRoot)
	setupMarker := filepath.Join(t.TempDir(), "setup-ran")
	setupScript := filepath.Join(t.TempDir(), "setup.sh")
	writeExecutableFile(t, setupScript, fmt.Sprintf("#!/bin/sh\ntouch %q\n", setupMarker))
	env.service.setupScript = setupScript

	restored, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID:           task.ID,
		SetupOperationID: newWorktreeSetupOperationIDPointer(),
	})
	if err != nil {
		t.Fatalf("RestoreLockedTaskWorktree: %v", err)
	}
	if restored.Created ||
		taskWorktreeID(restored.Worktree) != worktreeID ||
		taskWorktreeRoot(restored.Worktree) != worktreeRoot ||
		restored.Worktree.Registered == nil ||
		!restored.Worktree.Registered.Git.Detached ||
		restored.Worktree.Registered.Git.BranchRef != nil ||
		restored.Worktree.Registered.Git.BranchName != nil {
		t.Fatalf("restored worktree = %+v, want detached reuse of %q", restored, worktreeID)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	persisted, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
	}
	if persisted.HeadOID != detachedRevision.CommitOID ||
		!persisted.Detached ||
		persisted.Branch != nil {
		t.Fatalf("persisted detached metadata = %+v, want head %q", persisted, detachedRevision.CommitOID)
	}
	if _, err := os.Stat(setupMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore ran setup for reused detached worktree: %v", err)
	}
}

func TestPrepareTaskExecutionRootRejectsDetachedExistingCandidate(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	resolved := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolved)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	worktreeID := taskWorktreeID(materialized.Worktree)
	runGit(t, taskWorktreeRoot(materialized.Worktree), "checkout", "--detach")

	_, err = prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolved)
	var identityErr *ManagedWorktreeIdentityError
	if !errors.As(err, &identityErr) || identityErr.Kind != ManagedWorktreeIdentityErrorDetachedHead {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want detached-head identity error", err)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != worktreeID {
		t.Fatalf("task managed worktree id = %+v, want unchanged %q", row.ManagedWorktreeID, worktreeID)
	}
}

func TestRestoreLockedTaskWorktreeRejectsDifferentRepository(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, taskWorktreeRoot(materialized.Worktree), true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	if err := os.MkdirAll(taskWorktreeRoot(materialized.Worktree), 0o755); err != nil {
		t.Fatalf("MkdirAll replacement repository root: %v", err)
	}
	initGitRepo(t, taskWorktreeRoot(materialized.Worktree))

	_, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseInvalidRoot {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want invalid-root locked target error", err)
	}
}

func TestRestoreLockedTaskWorktreeRejectsNonGitRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, taskWorktreeRoot(materialized.Worktree), true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	if err := os.MkdirAll(taskWorktreeRoot(materialized.Worktree), 0o755); err != nil {
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
	root := taskWorktreeRoot(materialized.Worktree)
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
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, taskWorktreeRoot(materialized.Worktree), true); err != nil {
		t.Fatalf("Remove worktree: %v", err)
	}
	env.service.git = NewGitInspector(&selectedCommandFailingGitRunner{
		base:      execGitCommandRunner{},
		directory: env.workspaceRoot,
		arguments: []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + task.ShortID + "^{object}"},
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
	record, err := env.store.GetWorktreeRecordByID(env.ctx, taskWorktreeID(materialized.Worktree))
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	gitMetadata, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
	}
	if gitMetadata.Branch == nil || gitMetadata.Branch.Name() != task.ShortID {
		t.Fatalf("persisted branch = %+v, want %q (metadata %s)", gitMetadata.Branch, task.ShortID, record.GitMetadataJSON)
	}
	oldRoot := taskWorktreeRoot(materialized.Worktree)
	if err := os.RemoveAll(oldRoot); err != nil {
		t.Fatalf("remove worktree root: %v", err)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID:           task.ID,
		SetupOperationID: newWorktreeSetupOperationIDPointer(),
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
	oldRoot := taskWorktreeRoot(materialized.Worktree)
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
	staleRecord, err := env.store.GetWorktreeRecordByCanonicalRoot(env.ctx, taskWorktreeRoot(materialized.Worktree))
	if err != nil {
		t.Fatalf("GetWorktreeRecordByCanonicalRoot: %v", err)
	}
	if !taskRow.SourceWorkspaceID.Valid || taskRow.SourceWorkspaceID.String != staleRecord.WorkspaceID {
		t.Fatalf("task source workspace = %+v, stale worktree workspace = %q", taskRow.SourceWorkspaceID, staleRecord.WorkspaceID)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseConflict {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want conflict without Task ownership evidence", err)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree id = %+v, want unbound", row.ManagedWorktreeID)
	}
}

func TestRestoreLockedTaskWorktreeRejectsDetachedUnboundExistingRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	worktreeRoot := taskWorktreeRoot(materialized.Worktree)
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
	runGit(t, worktreeRoot, "checkout", "--detach")
	setupMarker := filepath.Join(t.TempDir(), "setup-ran")
	setupScript := filepath.Join(t.TempDir(), "setup.sh")
	writeExecutableFile(t, setupScript, fmt.Sprintf("#!/bin/sh\ntouch %q\n", setupMarker))
	env.service.setupScript = setupScript

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID:           task.ID,
		SetupOperationID: newWorktreeSetupOperationIDPointer(),
	})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseConflict {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want conflict for unclaimed occupied root", err)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree id = %+v, want unbound", row.ManagedWorktreeID)
	}
	identity, err := env.service.git.ValidateManagedWorktreeIdentity(env.ctx, ManagedWorktreeIdentitySpec{
		SourceWorkspaceRoot:  env.workspaceRoot,
		ExpectedWorktreeRoot: worktreeRoot,
	})
	if err != nil {
		t.Fatalf("ValidateManagedWorktreeIdentity: %v", err)
	}
	if branchName, named := identity.NamedBranch(); named {
		t.Fatalf("rejected worktree branch name = %q, want detached", branchName)
	}
	if _, err := os.Stat(setupMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore ran setup for rejected detached repair: %v", err)
	}
}

func TestRestoreLockedTaskWorktreeRecreatesMissingUnboundRootFromRecordedNamedBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	runGit(t, taskWorktreeRoot(materialized.Worktree), "branch", "-m", "operator-renamed")
	if _, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID}); err != nil {
		t.Fatalf("refresh locked worktree metadata: %v", err)
	}
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, taskWorktreeRoot(materialized.Worktree), true); err != nil {
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

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseMissingBranch {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want missing-branch without Task ownership evidence", err)
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, "operator-renamed"); branchErr != nil {
		t.Fatalf("BranchExists: %v", branchErr)
	} else if !exists {
		t.Fatal("restore removed operator-renamed branch")
	}
}

func TestRestoreLockedTaskWorktreeDoesNotInferUnboundBranchFromTaskShortID(t *testing.T) {
	env := newServiceTestEnv(t)
	task, materialized, _ := materializeAndLockTaskWorktree(t, env)
	if err := env.service.git.Remove(env.ctx, env.workspaceRoot, taskWorktreeRoot(materialized.Worktree), true); err != nil {
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
	if err := env.store.DeleteWorktreeRecordByID(env.ctx, taskWorktreeID(materialized.Worktree)); err != nil {
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
	if _, statErr := os.Stat(taskWorktreeRoot(materialized.Worktree)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("restore recreated unbound root %q from task short ID: %v", taskWorktreeRoot(materialized.Worktree), statErr)
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
	record, err := env.store.GetWorktreeRecordByID(env.ctx, taskWorktreeID(materialized.Worktree))
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
	persistedRecord, err := env.store.GetWorktreeRecordByCanonicalRoot(env.ctx, taskWorktreeRoot(materialized.Worktree))
	if err != nil {
		t.Fatalf("GetWorktreeRecordByCanonicalRoot: %v", err)
	}
	if persistedRecord.WorkspaceID != otherWorkspace.WorkspaceID || otherWorkspace.WorkspaceID == env.binding.WorkspaceID {
		t.Fatalf("persisted worktree workspace = %q, other workspace = %q, source workspace = %q", persistedRecord.WorkspaceID, otherWorkspace.WorkspaceID, env.binding.WorkspaceID)
	}
	if info, statErr := os.Stat(taskWorktreeRoot(materialized.Worktree)); statErr != nil || !info.IsDir() {
		t.Fatalf("deterministic root unavailable before restore: info=%v err=%v", info, statErr)
	}

	_, err = env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{TaskID: task.ID})
	var lockedErr *LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != LockedTaskWorktreeCauseConflict {
		t.Fatalf("RestoreLockedTaskWorktree error = %v, want conflict locked target error", err)
	}
}

func TestPrepareTaskExecutionRootCreatesShortIDBranchWithoutControllerLease(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolvedTarget)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if taskWorktreeID(resp.Worktree) == "" {
		t.Fatalf("worktree response = %+v", resp.Worktree)
	}
	if !resp.Created || !resp.CreatedBranch {
		t.Fatalf("created flags = created:%t branch:%t, want true/true", resp.Created, resp.CreatedBranch)
	}
	if !resp.Worktree.Registered.Kent.Managed || !resp.Worktree.Registered.Kent.CreatedBranch {
		t.Fatalf("worktree provenance = %+v, want managed created branch", resp.Worktree)
	}
	if taskWorktreeBranch(resp.Worktree) != task.ShortID {
		t.Fatalf("branch name = %q, want task short id %q", taskWorktreeBranch(resp.Worktree), task.ShortID)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", task.ShortID); !strings.Contains(got, task.ShortID) {
		t.Fatalf("branch list = %q, want task branch %q", got, task.ShortID)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != taskWorktreeID(resp.Worktree) {
		t.Fatalf("task managed worktree id = %+v, want %q", row.ManagedWorktreeID, taskWorktreeID(resp.Worktree))
	}
}

func TestPrepareTaskExecutionRootNoConfiguredScriptReturnsTypedResultWithoutEvents(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()

	materialized, err := prepareManagedTaskExecutionRoot(
		env.ctx,
		env.service,
		task.ID,
		&setupID,
		resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if materialized.SetupResult == nil || materialized.SetupResult.NotRequired == nil ||
		materialized.SetupResult.NotRequired.Reason != serverapi.WorktreeSetupNotRequiredNoConfiguredScript {
		t.Fatalf("setup result = %+v, want no configured script", materialized.SetupResult)
	}
	if err := materialized.SetupResult.Validate(); err != nil {
		t.Fatalf("validate setup result: %v", err)
	}
	assertNoSetupEvent(t, sub)
}

func TestPrepareTaskExecutionRootCreatesFromResolvedCommitAndRecordsImmutableBase(t *testing.T) {
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

	resp, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolvedBase)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if !resp.Created {
		t.Fatalf("materialized response = %+v, want newly created candidate", resp)
	}
	if got := runGit(t, taskWorktreeRoot(resp.Worktree), "rev-parse", "HEAD"); got != resolvedBase.CommitOID {
		t.Fatalf("worktree HEAD = %q, want resolved base %q", got, resolvedBase.CommitOID)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, taskWorktreeID(resp.Worktree))
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if record.CreationBaseCommitOID == nil || *record.CreationBaseCommitOID != resolvedBase.CommitOID {
		t.Fatalf("creation base commit oid = %v, want %q", record.CreationBaseCommitOID, resolvedBase.CommitOID)
	}
}

func TestPrepareTaskExecutionRootRunsSetupAndPublishesProgressBeforeReturning(t *testing.T) {
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
		resp, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, &setupID, resolvedTarget)
		resultCh <- materializationResult{resp: resp, err: err}
	}()

	if got := waitForFileText(t, startedPath); got != "started" {
		t.Fatalf("started marker = %q, want started", got)
	}
	evt, err := sub.Next(env.ctx)
	if err != nil {
		t.Fatalf("setup event: %v", err)
	}
	if evt.Phase != serverapi.WorktreeSetupPhaseStarted || evt.SetupOperationID != setupID ||
		evt.Started == nil || evt.Started.ScriptPath == "" || evt.Started.WorktreeRoot == "" {
		t.Fatalf("started setup event = %+v", evt)
	}
	select {
	case result := <-resultCh:
		t.Fatalf("PrepareTaskExecutionRoot returned before setup release: resp=%+v err=%v", result.resp, result.err)
	default:
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o644); err != nil {
		t.Fatalf("release setup: %v", err)
	}
	var result materializationResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for PrepareTaskExecutionRoot")
	}
	if result.err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", result.err)
	}
	if got := waitForFileText(t, markerPath); got != "marker" {
		t.Fatalf("setup marker = %q, want marker", got)
	}
	payload := waitForSetupPayload(t, payloadPath)
	if payload.SourceWorkspaceRoot != env.workspaceRoot || payload.WorktreeRoot != taskWorktreeRoot(result.resp.Worktree) {
		t.Fatalf("setup payload = %+v, want source %q worktree %q", payload, env.workspaceRoot, taskWorktreeRoot(result.resp.Worktree))
	}
}

func TestPrepareTaskExecutionRootSetupOmitsStaleParentSessionEnvironment(t *testing.T) {
	env := newServiceTestEnv(t)
	t.Setenv(setupEnvironmentKeySessionID, "stale-parent-session")
	t.Setenv(setupEnvironmentKeyWorktreeRoot, "stale-parent-worktree")
	task, _ := createTaskWorktreeTestTask(t, env)
	capture := testsetup.New(t, testsetup.Options{})
	env.service.setupScript = capture.Executable()
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolvedTarget)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
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
	if err := invocation.Verify(testsetup.Payload{
		SourceWorkspaceRoot: env.workspaceRoot,
		BranchName:          task.ShortID,
		WorktreeRoot:        taskWorktreeRoot(resp.Worktree),
		ProjectID:           env.binding.ProjectID,
		WorkspaceID:         env.binding.WorkspaceID,
		WorktreeID:          taskWorktreeID(resp.Worktree),
		CreatedBranch:       resp.CreatedBranch,
	}); err != nil {
		t.Fatalf("workflow setup contract: %v", err)
	}
}

func TestCreateWorktreeSetupReplacesStaleParentReservedEnvironment(t *testing.T) {
	env := newServiceTestEnv(t)
	t.Setenv(setupEnvironmentKeySessionID, "stale-parent-session")
	t.Setenv(setupEnvironmentKeyWorktreeRoot, "stale-parent-worktree")
	capture := testsetup.New(t, testsetup.Options{})
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
	created := worktreeViewFromListEntryForTest(resp.Worktree)
	if err := invocation.Verify(testsetup.Payload{
		SourceWorkspaceRoot: env.workspaceRoot,
		BranchName:          "feature/session-contract",
		WorktreeRoot:        created.CanonicalRoot,
		SessionID:           payload.SessionID,
		ProjectID:           env.binding.ProjectID,
		WorkspaceID:         env.binding.WorkspaceID,
		WorktreeID:          created.WorktreeID,
		CreatedBranch:       created.CreatedBranch,
	}); err != nil {
		t.Fatalf("session setup contract: %v", err)
	}
}

func TestPrepareTaskExecutionRootReturnsExistingManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}

	first, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot first: %v", err)
	}
	second, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot second: %v", err)
	}
	if second.Created || second.CreatedBranch {
		t.Fatalf("second ensure created flags = created:%t branch:%t, want false/false", second.Created, second.CreatedBranch)
	}
	if taskWorktreeID(first.Worktree) != taskWorktreeID(second.Worktree) {
		t.Fatalf("second worktree id = %q, want %q", taskWorktreeID(second.Worktree), taskWorktreeID(first.Worktree))
	}
}

func TestPrepareTaskExecutionRootRetriesIgnoredSetupChangesInPlace(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	probeName := ".setup-recreation-probe"
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, ".gitignore"), []byte(probeName+"\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", ".gitignore")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "ignore setup recreation probe")
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "retry-after-recreation.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf(
		"#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then touch \"$PWD/%s\"; exit 3; fi\nif [ ! -e \"$PWD/%s\" ]; then exit 9; fi\n",
		countPath,
		countPath,
		countPath,
		probeName,
		probeName,
	))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()

	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, &setupID, base)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup attempt count = %q, want 2", got)
	}
	if materialized.SetupResult == nil || materialized.SetupResult.Completed == nil {
		t.Fatalf("setup result = %+v, want completed", materialized.SetupResult)
	}
	if err := materialized.SetupResult.Validate(); err != nil {
		t.Fatalf("validate setup result: %v", err)
	}
	if _, err := os.Stat(filepath.Join(taskWorktreeRoot(materialized.Worktree), probeName)); err != nil {
		t.Fatalf("ignored setup output was not preserved for in-place retry: %v", err)
	}
	assertSetupAttemptEventsOnly(t, sub, 2)
}

func TestPrepareTaskExecutionRootRetriesChangedRootInPlace(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "retry-in-place.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf(
		"#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then printf changed > \"$PWD/setup-change.txt\"; exit 3; fi\nif [ ! -f \"$PWD/setup-change.txt\" ]; then exit 9; fi\n",
		countPath,
		countPath,
		countPath,
	))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()

	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, &setupID, base)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup attempt count = %q, want 2", got)
	}
	if got := waitForFileText(t, filepath.Join(taskWorktreeRoot(materialized.Worktree), "setup-change.txt")); got != "changed" {
		t.Fatalf("in-place setup change = %q, want retained", got)
	}
	assertSetupAttemptEventsOnly(t, sub, 2)
}

func TestPrepareTaskExecutionRootRetainsExistingIgnoredProvisionalRootBeforeSetup(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	probeName := ".existing-clean-probe"
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, ".gitignore"), []byte(probeName+"\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", ".gitignore")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "ignore existing clean probe")
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	first, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("initial PrepareTaskExecutionRoot: %v", err)
	}
	firstRoot := taskWorktreeRoot(first.Worktree)
	if err := os.WriteFile(filepath.Join(firstRoot, probeName), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write existing-root probe: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "existing-clean.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf(
		"#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ ! -e \"$PWD/%s\" ]; then exit 9; fi\n",
		countPath,
		countPath,
		countPath,
		probeName,
	))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()

	second, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, &setupID, base)
	if err != nil {
		t.Fatalf("recovery PrepareTaskExecutionRoot: %v", err)
	}
	if second.Created || second.CreatedBranch {
		t.Fatalf("existing provisional materialization flags = %+v, want retained root", second)
	}
	if taskWorktreeID(second.Worktree) != taskWorktreeID(first.Worktree) || taskWorktreeRoot(second.Worktree) != firstRoot {
		t.Fatalf("retained provisional identity = %+v, want id/root from %+v", second.Worktree, first.Worktree)
	}
	if got := waitForFileText(t, countPath); got != "1" {
		t.Fatalf("setup attempt count = %q, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(firstRoot, probeName)); err != nil {
		t.Fatalf("ignored operator output was not preserved before setup: %v", err)
	}
	assertSetupAttemptEventsOnly(t, sub, 1)
}

func TestPrepareTaskExecutionRootDoesNotRecreateCleanAdvancedProvisionalRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	probeName := ".advanced-root-probe"
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, ".gitignore"), []byte(probeName+"\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", ".gitignore")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "ignore advanced-root probe")
	base := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("initial PrepareTaskExecutionRoot: %v", err)
	}
	firstRoot := taskWorktreeRoot(first.Worktree)
	if err := os.WriteFile(filepath.Join(firstRoot, "operator-commit.txt"), []byte("preserve me\n"), 0o644); err != nil {
		t.Fatalf("write operator commit: %v", err)
	}
	runGit(t, firstRoot, "add", "operator-commit.txt")
	runGit(t, firstRoot, "commit", "-q", "-m", "operator commit")
	if err := os.WriteFile(filepath.Join(firstRoot, probeName), []byte("retained"), 0o644); err != nil {
		t.Fatalf("write advanced-root probe: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "advanced-clean.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf(
		"#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\n[ -e \"$PWD/%s\" ]\n",
		countPath,
		countPath,
		countPath,
		probeName,
	))
	env.service.setupScript = scriptRelpath

	second, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("recovery PrepareTaskExecutionRoot: %v", err)
	}
	if taskWorktreeRoot(second.Worktree) != firstRoot {
		t.Fatalf("recovery root = %q, want retained root %q", taskWorktreeRoot(second.Worktree), firstRoot)
	}
	if got := waitForFileText(t, countPath); got != "1" {
		t.Fatalf("setup attempt count = %q, want 1", got)
	}
	if got := waitForFileText(t, filepath.Join(firstRoot, probeName)); got != "retained" {
		t.Fatalf("advanced-root probe = %q, want retained", got)
	}
}

func TestPrepareTaskExecutionRootFinalSetupFailureRetainsCurrentRootAndBinding(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "fails-twice.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf(
		"#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then printf changed > \"$PWD/setup-change.txt\"; exit 3; fi\nexit 7\n",
		countPath,
		countPath,
		countPath,
	))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()

	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, &setupID, base)
	if err == nil {
		t.Fatal("PrepareTaskExecutionRoot succeeded, want final setup failure")
	}
	var retained *serverapi.WorktreeSetupRetainedError
	if !errors.As(err, &retained) || retained.Worktree.Registered == nil {
		t.Fatalf("setup error = %T %v, want retained worktree", err, err)
	}
	wantScriptPath := filepath.Join(env.workspaceRoot, scriptRelpath)
	if retained.ScriptPath != wantScriptPath {
		t.Fatalf("retained setup script path = %q, want %q", retained.ScriptPath, wantScriptPath)
	}
	var setupErr *setupScriptError
	if !errors.As(err, &setupErr) || setupErr.ExitCode == nil || *setupErr.ExitCode != 7 {
		t.Fatalf("setup error = %T %v, want final exit code 7", err, err)
	}
	if materialized.SetupResult == nil || materialized.SetupResult.Failed == nil ||
		materialized.SetupResult.Failed.Cause.ProcessExit == nil ||
		materialized.SetupResult.Failed.Cause.ProcessExit.ExitCode != 7 {
		t.Fatalf("setup result = %+v, want final process exit 7", materialized.SetupResult)
	}
	if err := materialized.SetupResult.Validate(); err != nil {
		t.Fatalf("validate setup result: %v", err)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup attempt count = %q, want 2", got)
	}
	retainedRoot := retained.Worktree.Registered.Kent.CanonicalRoot
	if got := waitForFileText(t, filepath.Join(retainedRoot, "setup-change.txt")); got != "changed" {
		t.Fatalf("retained in-place setup change = %q, want changed", got)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != retained.Worktree.Registered.Kent.WorktreeID {
		t.Fatalf("retained task binding = %+v, want %q", row.ManagedWorktreeID, retained.Worktree.Registered.Kent.WorktreeID)
	}
	assertSetupAttemptEventsOnly(t, sub, 2)
}

func TestPrepareTaskExecutionRootMissingConfiguredScriptReturnsTypedRetainedError(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	scriptRelpath := filepath.Join("scripts", "missing-task-setup.sh")
	env.service.setupScript = scriptRelpath

	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err == nil {
		t.Fatal("PrepareTaskExecutionRoot succeeded, want missing setup script failure")
	}
	var retained *serverapi.WorktreeSetupRetainedError
	if !errors.As(err, &retained) || retained.Worktree.Registered == nil {
		t.Fatalf("setup error = %T %v, want typed retained worktree", err, err)
	}
	wantScriptPath := filepath.Join(env.workspaceRoot, scriptRelpath)
	if retained.ScriptPath != wantScriptPath {
		t.Fatalf("retained setup script path = %q, want %q", retained.ScriptPath, wantScriptPath)
	}
	if materialized.SetupResult == nil || materialized.SetupResult.Failed == nil {
		t.Fatalf("materialized setup result = %+v, want typed failure", materialized.SetupResult)
	}
}

func TestPrepareTaskExecutionRootWithoutProgressObserverEmitsNoSetupEvents(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base, err := env.service.git.ResolveHEAD(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD: %v", err)
	}
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "manual-move-no-progress.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf(
		"#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then printf changed > \"$PWD/setup-change.txt\"; exit 3; fi\n",
		countPath,
		countPath,
		countPath,
	))
	env.service.setupScript = scriptRelpath
	unrelatedSetupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: unrelatedSetupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()

	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup attempt count = %q, want 2", got)
	}
	if materialized.SetupResult == nil || materialized.SetupResult.Completed == nil {
		t.Fatalf("setup result = %+v, want completed", materialized.SetupResult)
	}
	assertNoSetupEvent(t, sub)
}

func TestPrepareTaskExecutionRootUsesTaskSourceWorkspace(t *testing.T) {
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

	resp, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolvedTarget)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	canonicalBase, err := filepath.EvalSymlinks(env.baseDir)
	if err != nil {
		t.Fatalf("canonical managed worktree base: %v", err)
	}
	expectedParent := filepath.Join(canonicalBase, normalizeWorkspacePathKey(filepath.Base(sourceRoot)))
	if taskWorktreeID(resp.Worktree) == "" || filepath.Dir(taskWorktreeRoot(resp.Worktree)) != expectedParent {
		t.Fatalf("worktree = %+v, want compact parent %q", resp.Worktree, expectedParent)
	}
	if got := runGit(t, sourceRoot, "branch", "--list", task.ShortID); !strings.Contains(got, task.ShortID) {
		t.Fatalf("source branch list = %q, want task branch %q", got, task.ShortID)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", task.ShortID); strings.Contains(got, task.ShortID) {
		t.Fatalf("primary branch list = %q, did not expect task branch %q", got, task.ShortID)
	}
}

func TestTaskSourceWorkspaceRetainsProjectPrimaryOutsideCollectionLimit(t *testing.T) {
	env := newServiceTestEnv(t)
	for index := 0; index < metadata.ProjectWorkspaceCollectionLimit; index++ {
		if _, err := env.store.AttachWorkspaceToProject(env.ctx, env.binding.ProjectID, t.TempDir()); err != nil {
			t.Fatalf("AttachWorkspaceToProject %d: %v", index, err)
		}
	}

	source, err := env.service.taskSourceWorkspace(env.ctx, env.binding.ProjectID, "")
	if err != nil {
		t.Fatalf("taskSourceWorkspace: %v", err)
	}
	if source.WorkspaceID != env.binding.WorkspaceID {
		t.Fatalf("source workspace = %q, want project primary %q", source.WorkspaceID, env.binding.WorkspaceID)
	}
	if source.RootPath != env.workspaceRoot {
		t.Fatalf("source root = %q, want project primary root %q", source.RootPath, env.workspaceRoot)
	}
}

func TestPrepareTaskExecutionRootHandlesRootCollisionAndReportsBranchCollision(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	parent, err := env.service.managedRoots.ensureWorkspaceParent(env.workspaceRoot)
	if err != nil {
		t.Fatalf("ensure compact parent: %v", err)
	}
	baseRoot := filepath.Join(parent, task.ShortID)
	if err := os.Mkdir(baseRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll collision root: %v", err)
	}
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolvedTarget)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot root collision: %v", err)
	}
	if taskWorktreeRoot(resp.Worktree) == baseRoot {
		t.Fatalf("worktree root = %q, want suffixed root because base exists", taskWorktreeRoot(resp.Worktree))
	}
	if filepath.Base(taskWorktreeRoot(resp.Worktree)) == filepath.Base(baseRoot) {
		t.Fatalf("worktree root = %q, want compact suffix after existing collision", taskWorktreeRoot(resp.Worktree))
	}

	otherTask, _ := createTaskWorktreeTestTask(t, env)
	runGit(t, env.workspaceRoot, "branch", otherTask.ShortID)
	_, err = prepareManagedTaskExecutionRoot(env.ctx, env.service, otherTask.ID, nil, resolvedTarget)
	var branchCollision *TaskBranchCollisionError
	if !errors.As(err, &branchCollision) || branchCollision.BranchName != otherTask.ShortID {
		t.Fatalf("PrepareTaskExecutionRoot branch collision error = %v, want task branch collision", err)
	}
}

func TestDeleteWorktreeRecreatesNonTerminalTaskManagedWorktreeOnRestore(t *testing.T) {
	env := newServiceTestEnv(t)
	task, created, _ := materializeAndLockTaskWorktree(t, env)

	_, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   env.session.Meta().SessionID,
		},
		Selector:            taskWorktreeID(created.Worktree),
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if _, err := os.Stat(taskWorktreeRoot(created.Worktree)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted task worktree root stat error = %v, want not exist", err)
	}
	taskAfterDelete, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after deletion: %v", err)
	}
	if !taskAfterDelete.ManagedWorktreeID.Valid || taskAfterDelete.ManagedWorktreeID.String != taskWorktreeID(created.Worktree) {
		t.Fatalf("managed worktree id after deletion = %+v, want retained %q", taskAfterDelete.ManagedWorktreeID, taskWorktreeID(created.Worktree))
	}

	restored, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("RestoreLockedTaskWorktree: %v", err)
	}
	if !restored.Created || taskWorktreeRoot(restored.Worktree) != taskWorktreeRoot(created.Worktree) {
		t.Fatalf("restored worktree = %+v, want recreated worktree at %q", restored, taskWorktreeRoot(created.Worktree))
	}
	taskAfterRestore, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after restore: %v", err)
	}
	if !taskAfterRestore.ManagedWorktreeID.Valid || taskAfterRestore.ManagedWorktreeID.String != taskWorktreeID(created.Worktree) {
		t.Fatalf("managed worktree id after restore = %+v, want retained %q", taskAfterRestore.ManagedWorktreeID, taskWorktreeID(created.Worktree))
	}
}

func TestDeleteWorktreeAllowsTerminalTaskManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	created, err := prepareManagedTaskExecutionRoot(
		env.ctx,
		env.service,
		task.ID,
		nil,
		resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	started, err := workflowStore.StartTask(env.ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want one current node", started.Mutation)
	}
	if _, err := workflowStore.CompleteCurrentNode(env.ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "done",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}

	_, err = env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   env.session.Meta().SessionID,
		},
		Selector:            taskWorktreeID(created.Worktree),
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree terminal task worktree: %v", err)
	}
	if _, err := os.Stat(taskWorktreeRoot(created.Worktree)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected task worktree removed, stat err=%v", err)
	}
}

func TestDeleteTaskWorktreeRemovesManagedWorktreeAndBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	created, err := prepareManagedTaskExecutionRoot(
		env.ctx,
		env.service,
		task.ID,
		nil,
		resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}

	resp, err := env.service.DeleteTaskWorktree(env.ctx, DeleteTaskWorktreeRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("DeleteTaskWorktree: %v", err)
	}
	if !resp.Deleted || resp.WorktreeID != taskWorktreeID(created.Worktree) || !resp.BranchDeleted {
		t.Fatalf("DeleteTaskWorktree response = %+v, want deleted worktree and branch", resp)
	}
	if _, err := os.Stat(taskWorktreeRoot(created.Worktree)); !errors.Is(err, os.ErrNotExist) {
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

func TestDeleteTaskWorktreeRollsBackSessionTargetWhenRemovalFails(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	created, err := prepareManagedTaskExecutionRoot(
		env.ctx,
		env.service,
		task.ID,
		nil,
		resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	worktreeID := taskWorktreeID(created.Worktree)
	worktreeRoot := taskWorktreeRoot(created.Worktree)
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, worktreeID, ".")
	targetBefore := mustResolveServiceTestTarget(t, env)
	runGit(t, env.workspaceRoot, "worktree", "lock", worktreeRoot)
	t.Cleanup(func() {
		if _, err := os.Stat(worktreeRoot); err == nil {
			runGit(t, env.workspaceRoot, "worktree", "unlock", worktreeRoot)
		}
	})

	if _, err := env.service.DeleteTaskWorktree(env.ctx, DeleteTaskWorktreeRequest{TaskID: string(task.ID)}); err == nil {
		t.Fatal("DeleteTaskWorktree succeeded for locked worktree")
	}
	targetAfter := mustResolveServiceTestTarget(t, env)
	if sessionTargetWorktreeID(targetAfter) != sessionTargetWorktreeID(targetBefore) ||
		targetAfter.EffectiveWorkdir != targetBefore.EffectiveWorkdir {
		t.Fatalf("session target changed after failed task worktree removal: before=%+v after=%+v", targetBefore, targetAfter)
	}
	if _, err := os.Stat(worktreeRoot); err != nil {
		t.Fatalf("locked task worktree root changed after failed removal: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, worktreeID); err != nil {
		t.Fatalf("task worktree record changed after failed removal: %v", err)
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

func advanceTaskWorktreeTestTarget(t *testing.T, env *serviceTestEnv) GitRevision {
	t.Helper()
	path := filepath.Join(env.workspaceRoot, "next-target-"+serverapi.NewWorktreeSetupOperationID().String()+".txt")
	if err := os.WriteFile(path, []byte("next\n"), 0o644); err != nil {
		t.Fatalf("write next target: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", filepath.Base(path))
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "next target")
	return resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
}

func assertTaskManagedWorktree(t *testing.T, env *serviceTestEnv, taskID workflow.TaskID, want string) {
	t.Helper()
	row, err := env.store.Queries().GetTask(env.ctx, string(taskID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if want == "" {
		if row.ManagedWorktreeID.Valid {
			t.Fatalf("task managed worktree = %+v, want absent", row.ManagedWorktreeID)
		}
		return
	}
	if !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != want {
		t.Fatalf("task managed worktree = %+v, want %q", row.ManagedWorktreeID, want)
	}
}

func assertTaskExecutionTargetUnlocked(t *testing.T, env *serviceTestEnv, taskID workflow.TaskID) {
	t.Helper()
	row, err := env.store.Queries().GetTask(env.ctx, string(taskID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.ManagedWorktreeID.Valid ||
		row.ExecutionTargetMode.Valid ||
		row.ExecutionTargetRequestedRef.Valid ||
		row.ExecutionTargetResolvedRef.Valid ||
		row.ExecutionTargetCommitOid.Valid ||
		row.ExecutionTargetProvenance.Valid {
		t.Fatalf("task execution target is locked or provisionally bound: %+v", row)
	}
}

func assertFailedTaskWorktreeCreationRolledBack(t *testing.T, env *serviceTestEnv, task workflowstore.TaskRecord) {
	t.Helper()
	assertTaskExecutionTargetUnlocked(t, env, task.ID)
	parent, err := env.service.managedRoots.ensureWorkspaceParent(env.workspaceRoot)
	if err != nil {
		t.Fatalf("ensure managed worktree parent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, task.ShortID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Task worktree reservation remains: %v", err)
	}
	exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, task.ShortID)
	if err != nil {
		t.Fatalf("inspect failed Task branch: %v", err)
	}
	if exists {
		t.Fatalf("failed Task worktree branch %q remains", task.ShortID)
	}
	records, err := env.store.ListWorktreeRecordsByWorkspaceID(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("list worktree records: %v", err)
	}
	for _, record := range records {
		if record.Managed {
			t.Fatalf("failed Task worktree record remains: %+v", record)
		}
	}
}

func materializeAndLockTaskWorktree(t *testing.T, env *serviceTestEnv) (workflowstore.TaskRecord, TaskWorktreeMaterialization, GitRevision) {
	t.Helper()
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, resolvedTarget)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
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
				WorktreeID: taskWorktreeID(materialized.Worktree),
				Root:       taskWorktreeRoot(materialized.Worktree),
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
	resolver := testsetup.QuestionsEnabled("workflow-test")
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
	agentID := workflow.NodeID("node-agent-" + created.ID.String())
	if _, err := store.AddNode(env.ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "implement", Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: "workflow-test"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(env.ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + created.ID.String()), WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(env.ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + created.ID.String()), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work"}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(env.ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + created.ID.String()), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(env.ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + created.ID.String()), Key: "done", TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession}); err != nil {
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

func newWorktreeSetupOperationIDPointer() *serverapi.WorktreeSetupOperationID {
	value := serverapi.NewWorktreeSetupOperationID()
	return &value
}

func assertNoSetupEvent(t *testing.T, sub serverapi.WorktreeSetupSubscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if event, err := sub.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected setup event = %+v, err = %v", event, err)
	}
}

func assertSetupAttemptEventsOnly(t *testing.T, sub serverapi.WorktreeSetupSubscription, count int) {
	t.Helper()
	for attempt := 1; attempt <= count; attempt++ {
		event, err := sub.Next(context.Background())
		if err != nil {
			t.Fatalf("setup attempt %d event: %v", attempt, err)
		}
		if event.Phase != serverapi.WorktreeSetupPhaseStarted || event.Started == nil {
			t.Fatalf("setup attempt %d event = %+v, want started", attempt, event)
		}
	}
	assertNoSetupEvent(t, sub)
}
