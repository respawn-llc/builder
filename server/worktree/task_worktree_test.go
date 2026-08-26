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
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/internal/testharness/workflowfixture"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/workflowcontract"
	"core/shared/worktreecontract"
)

func materializeInitialTaskWorktree(
	ctx context.Context,
	service *Service,
	req InitialTaskWorktreeMaterializationRequest,
) (TaskWorktreeMaterialization, error) {
	return prepareManagedTaskExecutionRoot(ctx, service, req.TaskID, req.SetupOperationID, req.ResolvedTarget)
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
		SetupRequirement: worktreecontract.SetupRequirementRequired,
	})
	if prepared.Materialization == nil {
		return TaskWorktreeMaterialization{}, err
	}
	return *prepared.Materialization, err
}

func newWorktreeSetupOperationIDPointer() *serverapi.WorktreeSetupOperationID {
	value := serverapi.NewWorktreeSetupOperationID()
	return &value
}

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

func TestMaterializeInitialTaskWorktreeRequiresResolvedCommit(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	_, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
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
	if _, err := env.store.Queries().BindInitialTaskManagedWorktree(env.ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: "legacy-initial-record", Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		TaskID:            string(task.ID),
	}); err != nil {
		t.Fatalf("BindInitialTaskManagedWorktree: %v", err)
	}

	_, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
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

func TestRestoreLockedTaskWorktreeReusesDetachedHeadWithoutErasingBranchAuthority(t *testing.T) {
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
		persisted.Branch != nil ||
		persisted.RecordedBranch == nil ||
		persisted.RecordedBranch.Name() != task.ShortID {
		t.Fatalf("persisted metadata = %+v, want detached head %q with recorded branch %q", persisted, detachedRevision.CommitOID, task.ShortID)
	}
	if _, err := os.Stat(setupMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore ran setup for reused detached worktree: %v", err)
	}
	if err := env.service.AssertInitialTaskBranch(env.ctx, task.ID, task.ShortID); err != nil {
		t.Fatalf("AssertInitialTaskBranch after detached reuse: %v", err)
	}
	otherBranch := "feature/other"
	err = env.service.AssertInitialTaskBranch(env.ctx, task.ID, otherBranch)
	var mismatch *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &mismatch) ||
		mismatch.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch ||
		mismatch.ExistingBranchName == nil ||
		*mismatch.ExistingBranchName != task.ShortID {
		t.Fatalf("AssertInitialTaskBranch mismatch after detached reuse = %+v, want existing branch %q", err, task.ShortID)
	}

	repeated, err := env.service.RestoreLockedTaskWorktree(env.ctx, LockedTaskWorktreeRestoreRequest{
		TaskID:           task.ID,
		SetupOperationID: newWorktreeSetupOperationIDPointer(),
		BranchName:       &task.ShortID,
	})
	if err != nil {
		t.Fatalf("RestoreLockedTaskWorktree repeated: %v", err)
	}
	if repeated.Created ||
		taskWorktreeID(repeated.Worktree) != worktreeID ||
		taskWorktreeRoot(repeated.Worktree) != worktreeRoot ||
		repeated.Worktree.Registered == nil ||
		!repeated.Worktree.Registered.Git.Detached {
		t.Fatalf("repeated restored worktree = %+v, want detached reuse of %q", repeated, worktreeID)
	}
	if _, err := os.Stat(setupMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repeated restore ran setup for reused detached worktree: %v", err)
	}
}

func TestPrepareTaskExecutionRootNoneTargetRetainsModifiedRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	firstTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	first, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:           task.ID,
		ManagedTarget:    &firstTarget,
		SetupRequirement: worktreecontract.SetupRequirementRequired,
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
	replacement, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:           task.ID,
		SetupRequirement: worktreecontract.SetupRequirementRequired,
	})
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot replacement: %v", err)
	}
	if replacement.Root.Managed != nil {
		t.Fatalf("replacement root = %+v, want source workspace", replacement.Root)
	}
	if replacement.RetainedPreviousWorktree == nil ||
		replacement.RetainedPreviousWorktree.Worktree.Registered == nil ||
		replacement.RetainedPreviousWorktree.Worktree.Registered.Kent.WorktreeID != first.Root.Managed.WorktreeID {
		t.Fatalf("retained previous worktree = %+v, want %q", replacement.RetainedPreviousWorktree, first.Root.Managed.WorktreeID)
	}
	if got := waitForFileText(t, changedPath); got != "keep me" {
		t.Fatalf("retained worktree change = %q, want preserved", got)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("task managed worktree = %+v, want unbound", row.ManagedWorktreeID)
	}
}

func TestPrepareTaskExecutionRootSettingsFailureSurfacesOnceWithoutSetup(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	target := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	settingsErr := errors.New("injected setup settings failure")
	resolveCalls := 0
	env.service.resolveSetup = func(string) (config.WorktreeSettings, error) {
		resolveCalls++
		return config.WorktreeSettings{}, settingsErr
	}

	_, err := env.service.PrepareTaskExecutionRoot(env.ctx, TaskExecutionRootPreparationRequest{
		TaskID:           task.ID,
		ManagedTarget:    &target,
		SetupRequirement: worktreecontract.SetupRequirementRequired,
	})
	if !errors.Is(err, settingsErr) {
		t.Fatalf("PrepareTaskExecutionRoot error = %v, want %v", err, settingsErr)
	}
	if resolveCalls != 1 {
		t.Fatalf("setup settings resolutions = %d, want one", resolveCalls)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("settings failure attached managed worktree = %+v", row.ManagedWorktreeID)
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

func TestMaterializeInitialTaskWorktreeCreatesShortIDBranchWithoutControllerLease(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
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

func TestMaterializeInitialTaskWorktreeCreatesPendingCustomBranchAtShortIDRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	const branchName = "feature/MBL-742"
	if err := workflowStore.ReplacePendingInitialManagedBranchName(env.ctx, task.ID, branchName); err != nil {
		t.Fatalf("ReplacePendingInitialManagedBranchName: %v", err)
	}

	resp, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	if got := taskWorktreeBranch(resp.Worktree); got != branchName {
		t.Fatalf("materialized branch = %q, want %q", got, branchName)
	}
	if got := filepath.Base(taskWorktreeRoot(resp.Worktree)); got != task.ShortID {
		t.Fatalf("automatic root basename = %q, want Task Short ID %q", got, task.ShortID)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.PendingInitialManagedBranchName.Valid {
		t.Fatalf("bound pending initial managed branch = %+v, want absent", row.PendingInitialManagedBranchName)
	}
}

func TestMaterializeInitialTaskWorktreeUsesLeasedPendingBranchSnapshot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	const (
		snapshotBranch = "feature/snapshot"
		laterBranch    = "feature/later"
	)
	if err := workflowStore.ReplacePendingInitialManagedBranchName(env.ctx, task.ID, snapshotBranch); err != nil {
		t.Fatalf("replace snapshot branch: %v", err)
	}
	inspectionStarted := make(chan struct{})
	releaseInspection := make(chan struct{})
	var pauseOnce sync.Once
	runner := &taskWorktreeGitCommandInterceptor{
		base: execGitCommandRunner{},
		beforeRun: func(ctx context.Context, _ string, args []string) error {
			if !slices.Equal(args, []string{"check-ref-format", "refs/heads/" + snapshotBranch}) {
				return nil
			}
			pauseOnce.Do(func() { close(inspectionStarted) })
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseInspection:
				return nil
			}
		},
	}
	env.service.git = NewGitInspector(runner)

	type result struct {
		materialized TaskWorktreeMaterialization
		err          error
	}
	resultCh := make(chan result, 1)
	go func() {
		materialized, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
			TaskID:         task.ID,
			ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
		})
		resultCh <- result{materialized: materialized, err: err}
	}()

	select {
	case <-inspectionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pending branch snapshot inspection")
	}
	if err := workflowStore.ReplacePendingInitialManagedBranchName(env.ctx, task.ID, laterBranch); err != nil {
		t.Fatalf("replace later branch: %v", err)
	}
	close(releaseInspection)

	var got result
	select {
	case got = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for materialization")
	}
	if got.err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", got.err)
	}
	if branch := taskWorktreeBranch(got.materialized.Worktree); branch != snapshotBranch {
		t.Fatalf("materialized branch = %q, want cutoff snapshot %q", branch, snapshotBranch)
	}
	if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, laterBranch); err != nil {
		t.Fatalf("BranchExists later branch: %v", err)
	} else if exists {
		t.Fatalf("post-snapshot branch %q was created", laterBranch)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.PendingInitialManagedBranchName.Valid {
		t.Fatalf("post-snapshot pending branch survived bind: %+v", row.PendingInitialManagedBranchName)
	}
}

func TestMaterializeInitialTaskWorktreeAllowsRemoteTrackingRefCreatedAfterFinalInspection(t *testing.T) {
	env := newServiceTestEnv(t)
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	const branchName = "feature/remote-race"
	if err := workflowStore.ReplacePendingInitialManagedBranchName(env.ctx, task.ID, branchName); err != nil {
		t.Fatalf("ReplacePendingInitialManagedBranchName: %v", err)
	}
	runGit(t, env.workspaceRoot, "remote", "add", "origin", "https://example.invalid/origin.git")
	var mutateOnce sync.Once
	env.service.git = NewGitInspector(&taskWorktreeGitCommandInterceptor{
		base: execGitCommandRunner{},
		beforeOutput: func(ctx context.Context, dir string, args []string) error {
			if len(args) < 4 || !slices.Equal(args[:4], []string{"worktree", "add", "-b", branchName}) {
				return nil
			}
			var err error
			mutateOnce.Do(func() {
				_, err = execGitCommandRunner{}.Output(
					ctx,
					dir,
					"update-ref",
					"refs/remotes/origin/"+branchName,
					"HEAD",
				)
			})
			return err
		},
	})

	materialized, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
	}
	if got := taskWorktreeBranch(materialized.Worktree); got != branchName {
		t.Fatalf("materialized branch = %q, want %q", got, branchName)
	}
	for _, ref := range []string{"refs/heads/" + branchName, "refs/remotes/origin/" + branchName} {
		exists, err := env.service.git.RefExists(env.ctx, env.workspaceRoot, ref)
		if err != nil {
			t.Fatalf("RefExists(%q): %v", ref, err)
		}
		if !exists {
			t.Fatalf("expected coexisting ref %q", ref)
		}
	}
}

func TestMaterializeInitialTaskWorktreeRejectsLocalBranchCreatedAfterFinalInspection(t *testing.T) {
	env := newServiceTestEnv(t)
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	const branchName = "feature/local-race"
	if err := workflowStore.ReplacePendingInitialManagedBranchName(env.ctx, task.ID, branchName); err != nil {
		t.Fatalf("ReplacePendingInitialManagedBranchName: %v", err)
	}
	var mutateOnce sync.Once
	env.service.git = NewGitInspector(&taskWorktreeGitCommandInterceptor{
		base: execGitCommandRunner{},
		beforeOutput: func(ctx context.Context, dir string, args []string) error {
			if len(args) < 4 || !slices.Equal(args[:4], []string{"worktree", "add", "-b", branchName}) {
				return nil
			}
			var err error
			mutateOnce.Do(func() {
				_, err = execGitCommandRunner{}.Output(ctx, dir, "branch", branchName)
			})
			return err
		},
	})

	_, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	localRef := "refs/heads/" + branchName
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision ||
		branchErr.BranchName != branchName ||
		branchErr.Ref == nil ||
		*branchErr.Ref != localRef {
		t.Fatalf("MaterializeInitialTaskWorktree error = %T %+v, want local collision for %q", err, err, localRef)
	}
	row, queryErr := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if queryErr != nil {
		t.Fatalf("GetTask: %v", queryErr)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("failed local race bound managed Worktree %+v", row.ManagedWorktreeID)
	}
	if !row.PendingInitialManagedBranchName.Valid || row.PendingInitialManagedBranchName.String != branchName {
		t.Fatalf("failed local race pending branch = %+v, want retained %q", row.PendingInitialManagedBranchName, branchName)
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, branchName); branchErr != nil {
		t.Fatalf("BranchExists: %v", branchErr)
	} else if !exists {
		t.Fatalf("injected local branch %q disappeared", branchName)
	}
}

func TestMaterializeInitialTaskWorktreePreservesExternallyRacedWorktreeAfterAddFailure(t *testing.T) {
	env := newServiceTestEnv(t)
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	const branchName = "feature/partial-add-failure"
	if err := workflowStore.ReplacePendingInitialManagedBranchName(env.ctx, task.ID, branchName); err != nil {
		t.Fatalf("ReplacePendingInitialManagedBranchName: %v", err)
	}
	addErr := errors.New("post-checkout hook failed")
	var raceOnce sync.Once
	env.service.git = NewGitInspector(&taskWorktreeGitCommandInterceptor{
		base: execGitCommandRunner{},
		beforeOutput: func(ctx context.Context, dir string, args []string) error {
			if len(args) < 5 || !slices.Equal(args[:4], []string{"worktree", "add", "-b", branchName}) {
				return nil
			}
			var raceErr error
			raceOnce.Do(func() {
				_, raceErr = execGitCommandRunner{}.Output(ctx, dir, args...)
			})
			if raceErr != nil {
				return fmt.Errorf("create externally raced worktree: %w", raceErr)
			}
			return addErr
		},
	})

	_, err := env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if !errors.Is(err, addErr) {
		t.Fatalf("MaterializeInitialTaskWorktree error = %T %v, want original add failure", err, err)
	}
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision ||
		branchErr.BranchName != branchName {
		t.Fatalf("MaterializeInitialTaskWorktree error = %+v, want joined local collision", err)
	}
	if exists, branchErr := env.service.git.BranchExists(env.ctx, env.workspaceRoot, branchName); branchErr != nil {
		t.Fatalf("BranchExists after ambiguous failed add: %v", branchErr)
	} else if !exists {
		t.Fatalf("unowned branch %q was deleted", branchName)
	}
	worktrees, listErr := env.service.git.List(env.ctx, env.workspaceRoot)
	if listErr != nil {
		t.Fatalf("List after ambiguous failed add: %v", listErr)
	}
	if len(worktrees) != 2 {
		t.Fatalf("worktrees after ambiguous failed add = %+v, want source and preserved raced Worktree", worktrees)
	}
	row, queryErr := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if queryErr != nil {
		t.Fatalf("GetTask: %v", queryErr)
	}
	if row.ManagedWorktreeID.Valid {
		t.Fatalf("failed partial add bound managed Worktree %+v", row.ManagedWorktreeID)
	}
	if !row.PendingInitialManagedBranchName.Valid || row.PendingInitialManagedBranchName.String != branchName {
		t.Fatalf("failed partial add pending branch = %+v, want retained %q", row.PendingInitialManagedBranchName, branchName)
	}

	_, err = env.service.MaterializeInitialTaskWorktree(env.ctx, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	branchErr = nil
	if !errors.As(err, &branchErr) || branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision {
		t.Fatalf("MaterializeInitialTaskWorktree retry error = %+v, want preserved local collision", err)
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

	resp, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedBase,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
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
		resp, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
			TaskID:           task.ID,
			SetupOperationID: &setupID,
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
	if evt.Phase != serverapi.WorktreeSetupPhaseStarted || evt.SetupOperationID != setupID ||
		evt.Started == nil || evt.Started.ScriptPath == "" || evt.Started.WorktreeRoot == "" {
		t.Fatalf("started setup event = %+v", evt)
	}
	select {
	case result := <-resultCh:
		t.Fatalf("MaterializeInitialTaskWorktree returned before setup release: resp=%+v err=%v", result.resp, result.err)
	default:
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
	if payload.SourceWorkspaceRoot != env.workspaceRoot || payload.WorktreeRoot != taskWorktreeRoot(result.resp.Worktree) {
		t.Fatalf("setup payload = %+v, want source %q worktree %q", payload, env.workspaceRoot, taskWorktreeRoot(result.resp.Worktree))
	}
}

func TestMaterializeInitialTaskWorktreeSetupOmitsStaleParentSessionEnvironment(t *testing.T) {
	env := newServiceTestEnv(t)
	t.Setenv(setupEnvironmentKeySessionID, "stale-parent-session")
	t.Setenv(setupEnvironmentKeyWorktreeRoot, "stale-parent-worktree")
	task, _ := createTaskWorktreeTestTask(t, env)
	capture := testsetup.New(t, testsetup.Options{})
	env.service.setupScript = capture.Executable()
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)

	resp, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
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

func TestPrepareTaskExecutionRootRecreatesCleanRootBeforeRetry(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "retry-clean-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then exit 3; fi\n", countPath, countPath, countPath))
	env.service.setupScript = scriptRelpath
	runner := &recordingGitCommandRunner{delegate: execGitCommandRunner{}}
	env.service.git = NewGitInspector(runner)

	_, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup attempt count = %q, want 2", got)
	}
	adds, removes := 0, 0
	for _, call := range runner.calls {
		if len(call) >= 2 && call[0] == "worktree" {
			switch call[1] {
			case "add":
				adds++
			case "remove":
				removes++
			}
		}
	}
	if adds != 2 || removes != 1 {
		t.Fatalf("Git recreation calls = %d add, %d remove; want 2 add, 1 remove", adds, removes)
	}
}

func TestPrepareTaskExecutionRootRetriesIgnoredOrEmptyChangedRootInPlace(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "retry-in-place.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then printf 'setup-change.txt\\n' >> \"$(git rev-parse --git-path info/exclude)\"; printf changed > \"$PWD/setup-change.txt\"; exit 3; fi\nif [ ! -f \"$PWD/setup-change.txt\" ]; then exit 9; fi\n", countPath, countPath, countPath))
	env.service.setupScript = scriptRelpath

	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	if err != nil {
		t.Fatalf("PrepareTaskExecutionRoot: %v", err)
	}
	if got := waitForFileText(t, filepath.Join(taskWorktreeRoot(materialized.Worktree), "setup-change.txt")); got != "changed" {
		t.Fatalf("in-place setup change = %q, want retained", got)
	}
	if err := errors.Join(os.Remove(filepath.Join(taskWorktreeRoot(materialized.Worktree), "setup-change.txt")), os.Mkdir(filepath.Join(taskWorktreeRoot(materialized.Worktree), "empty-change"), 0o755)); err != nil {
		t.Fatalf("prepare empty-directory change: %v", err)
	}
	if unchanged, err := env.service.git.probeRecreationUnchanged(env.ctx, taskWorktreeRoot(materialized.Worktree)); err != nil || unchanged {
		t.Fatalf("empty-directory probe = %t, %v; want changed", unchanged, err)
	}
}

func TestPrepareTaskExecutionRootFinalSetupFailureRetainsCurrentRootAndBinding(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	base := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "fails-twice.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf(
		"#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then printf changed > \"$PWD/setup-change.txt\"; exit 3; fi\nexit 7\n",
		countPath,
		countPath,
		countPath,
	))
	env.service.setupScript = scriptRelpath

	materialized, err := prepareManagedTaskExecutionRoot(env.ctx, env.service, task.ID, nil, base)
	var retained *serverapi.WorktreeSetupRetainedError
	if !errors.As(err, &retained) || retained.Worktree.Registered == nil {
		t.Fatalf("PrepareTaskExecutionRoot error = %T %v, want retained setup failure", err, err)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup attempt count = %q, want 2", got)
	}
	if materialized.SetupResult == nil || materialized.SetupResult.Failed == nil ||
		materialized.SetupResult.Failed.Cause.ProcessExit == nil ||
		materialized.SetupResult.Failed.Cause.ProcessExit.ExitCode != 7 {
		t.Fatalf("setup result = %+v, want final process exit 7", materialized.SetupResult)
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != retained.Worktree.Registered.Kent.WorktreeID {
		t.Fatalf("retained task binding = %+v, want %q", row.ManagedWorktreeID, retained.Worktree.Registered.Kent.WorktreeID)
	}
	if row.ExecutionTargetMode.Valid {
		t.Fatalf("failed setup locked execution target = %+v, want task remain unlocked", row.ExecutionTargetMode)
	}
	if row.PendingInitialManagedBranchName.Valid {
		t.Fatalf("failed setup retained pending initial managed branch %+v", row.PendingInitialManagedBranchName)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, row.ManagedWorktreeID.String)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if _, err := os.Stat(record.CanonicalRoot); err != nil {
		t.Fatalf("failed setup worktree root unavailable: %v", err)
	}
	persisted, err := worktreeGitMetadataFromRecord(record)
	if err != nil {
		t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
	}
	if persisted.Branch == nil || persisted.Branch.Name() != task.ShortID {
		t.Fatalf("failed setup persisted branch = %+v, want %q", persisted.Branch, task.ShortID)
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

	resp, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
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

func TestMaterializeInitialTaskWorktreeHandlesRootCollisionAndReportsBranchCollision(t *testing.T) {
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

	resp, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolvedTarget,
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree root collision: %v", err)
	}
	if taskWorktreeRoot(resp.Worktree) == baseRoot {
		t.Fatalf("worktree root = %q, want suffixed root because base exists", taskWorktreeRoot(resp.Worktree))
	}
	if filepath.Base(taskWorktreeRoot(resp.Worktree)) == filepath.Base(baseRoot) {
		t.Fatalf("worktree root = %q, want compact suffix after existing collision", taskWorktreeRoot(resp.Worktree))
	}

	otherTask, _ := createTaskWorktreeTestTask(t, env)
	runGit(t, env.workspaceRoot, "branch", otherTask.ShortID)
	_, err = materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
		TaskID:         otherTask.ID,
		ResolvedTarget: resolvedTarget,
	})
	var branchCollision *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchCollision) ||
		branchCollision.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision ||
		branchCollision.BranchName != otherTask.ShortID ||
		branchCollision.Ref == nil ||
		*branchCollision.Ref != "refs/heads/"+otherTask.ShortID {
		t.Fatalf("MaterializeInitialTaskWorktree branch collision error = %v, want task branch collision", err)
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
	created, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
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
	created, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
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
	created, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
		TaskID:         task.ID,
		ResolvedTarget: resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot),
	})
	if err != nil {
		t.Fatalf("MaterializeInitialTaskWorktree: %v", err)
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

func materializeAndLockTaskWorktree(t *testing.T, env *serviceTestEnv) (workflowstore.TaskRecord, TaskWorktreeMaterialization, GitRevision) {
	t.Helper()
	task, workflowStore := createTaskWorktreeTestTask(t, env)
	resolvedTarget := resolveTaskWorktreeTestHEAD(t, env, env.workspaceRoot)
	materialized, err := materializeInitialTaskWorktree(env.ctx, env.service, InitialTaskWorktreeMaterializationRequest{
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
			Mode:         workflowcontract.ExecutionTargetModeHead,
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

type taskWorktreeGitCommandInterceptor struct {
	base         gitCommandRunner
	beforeRun    func(context.Context, string, []string) error
	beforeOutput func(context.Context, string, []string) error
}

func (r *taskWorktreeGitCommandInterceptor) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if r.beforeOutput != nil {
		if err := r.beforeOutput(ctx, dir, slices.Clone(args)); err != nil {
			return nil, err
		}
	}
	output, err := r.base.Output(ctx, dir, args...)
	return output, err
}

func (r *taskWorktreeGitCommandInterceptor) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	if r.beforeRun != nil {
		if err := r.beforeRun(ctx, dir, slices.Clone(args)); err != nil {
			return nil, -1, err
		}
	}
	return r.base.Run(ctx, dir, args...)
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
	agentID := workflow.NodeID(runtimeids.NewGraphEntityID())
	startGroupID := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	doneGroupID := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	workflowfixture.SaveStoreGraph(t, env.ctx, store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		startID := taskWorktreeNodeIDByKind(t, definition, workflow.NodeKindStart)
		doneID := taskWorktreeNodeIDByKind(t, definition, workflow.NodeKindTerminal)
		request.Nodes = append(request.Nodes, workflowstore.NodeRecord{
			ID: agentID, WorkflowID: created.ID, Key: "implement",
			Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: "workflow-test",
		})
		request.TransitionGroups = append(request.TransitionGroups,
			workflowstore.TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
			workflowstore.TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
		)
		request.Edges = append(request.Edges,
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work"},
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: doneID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession},
		)
	})
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
