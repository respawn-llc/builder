package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestEnsureTaskWorktreeCreatesShortIDBranchWithoutControllerLease(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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

func TestProvisionExecutionTargetWorktreeCreatesTaskBranchFromExactCommit(t *testing.T) {
	env := newServiceTestEnv(t)
	baseCommit := runGit(t, env.workspaceRoot, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatalf("write later commit: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", "later.txt")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "later")
	worktreeRoot, err := env.service.PlanExecutionTargetWorktreeRoot(env.binding.WorkspaceID, "WOR-99")
	if err != nil {
		t.Fatalf("PlanExecutionTargetWorktreeRoot: %v", err)
	}

	provisioned, err := env.service.ProvisionExecutionTargetWorktree(env.ctx, ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         env.binding.WorkspaceID,
		SourceWorkspaceRoot: env.workspaceRoot,
		TaskShortID:         "WOR-99",
		ResolvedCommit:      baseCommit,
		WorktreeRoot:        worktreeRoot,
	})
	if err != nil {
		t.Fatalf("ProvisionExecutionTargetWorktree: %v", err)
	}
	if !provisioned.CreatedBranch || provisioned.BranchName != "WOR-99" || provisioned.WorktreeRoot != worktreeRoot {
		t.Fatalf("provisioned worktree = %+v, want created task branch", provisioned)
	}
	if got := runGit(t, provisioned.WorktreeRoot, "rev-parse", "HEAD"); got != baseCommit {
		t.Fatalf("provisioned worktree commit = %q, want resolved commit %q", got, baseCommit)
	}
}

func TestReprovisionExecutionTargetWorktreeRecreatesMissingRootFromExactTaskBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	commit := runGit(t, env.workspaceRoot, "rev-parse", "HEAD")
	worktreeRoot, err := env.service.PlanExecutionTargetWorktreeRoot(env.binding.WorkspaceID, "WOR-100")
	if err != nil {
		t.Fatalf("PlanExecutionTargetWorktreeRoot: %v", err)
	}
	if _, err := env.service.ProvisionExecutionTargetWorktree(env.ctx, ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         env.binding.WorkspaceID,
		SourceWorkspaceRoot: env.workspaceRoot,
		TaskShortID:         "WOR-100",
		ResolvedCommit:      commit,
		WorktreeRoot:        worktreeRoot,
	}); err != nil {
		t.Fatalf("ProvisionExecutionTargetWorktree: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	exact, err := env.service.InspectExecutionTargetWorktree(env.ctx, InspectExecutionTargetWorktreeRequest{
		SourceWorkspaceRoot: env.workspaceRoot,
		WorktreeRoot:        canonicalWorktreeRoot,
		TaskShortID:         "WOR-100",
		ResolvedCommit:      commit,
	})
	if err != nil {
		t.Fatalf("InspectExecutionTargetWorktree exact: %v", err)
	}
	if exact.LinkedWorktreeOwnership == nil {
		t.Fatalf("exact inspection = %+v, want linked-worktree ownership", exact)
	}
	expectedBranchTip := commit
	movedRoot := filepath.Join(t.TempDir(), "missing-root")
	if err := os.Rename(canonicalWorktreeRoot, movedRoot); err != nil {
		t.Fatalf("rename managed worktree root: %v", err)
	}
	missing, err := env.service.InspectExecutionTargetWorktree(env.ctx, InspectExecutionTargetWorktreeRequest{
		SourceWorkspaceRoot: env.workspaceRoot,
		WorktreeRoot:        canonicalWorktreeRoot,
		TaskShortID:         "WOR-100",
		ResolvedCommit:      commit,
		ExpectedOwnership:   exact.LinkedWorktreeOwnership,
		ExpectedBranchTip:   &expectedBranchTip,
	})
	if err != nil {
		t.Fatalf("InspectExecutionTargetWorktree missing root: %v", err)
	}
	if missing.Kind != ExecutionTargetWorktreeInspectionExactMissingRoot {
		t.Fatalf("missing-root inspection = %+v, want exact missing-root ownership", missing)
	}

	reprovisioned, err := env.service.ReprovisionExecutionTargetWorktree(env.ctx, ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         env.binding.WorkspaceID,
		SourceWorkspaceRoot: env.workspaceRoot,
		TaskShortID:         "WOR-100",
		ResolvedCommit:      commit,
		WorktreeRoot:        canonicalWorktreeRoot,
	})
	if err != nil {
		t.Fatalf("ReprovisionExecutionTargetWorktree: %v", err)
	}
	if reprovisioned.WorktreeRoot != canonicalWorktreeRoot ||
		reprovisioned.BranchName != "WOR-100" ||
		reprovisioned.CreatedBranch ||
		reprovisioned.ExactBranchObservation != commit ||
		reprovisioned.LinkedWorktreeOwnership == nil {
		t.Fatalf("reprovisioned worktree = %+v, want exact existing branch at durable root", reprovisioned)
	}
	if got := runGit(t, canonicalWorktreeRoot, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("reprovisioned worktree commit = %q, want %q", got, commit)
	}
}

func TestInspectExecutionTargetWorktreeRejectsMissingRootWithChangedBranchTip(t *testing.T) {
	env := newServiceTestEnv(t)
	commit := runGit(t, env.workspaceRoot, "rev-parse", "HEAD")
	worktreeRoot, err := env.service.PlanExecutionTargetWorktreeRoot(env.binding.WorkspaceID, "WOR-102")
	if err != nil {
		t.Fatalf("PlanExecutionTargetWorktreeRoot: %v", err)
	}
	if _, err := env.service.ProvisionExecutionTargetWorktree(env.ctx, ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         env.binding.WorkspaceID,
		SourceWorkspaceRoot: env.workspaceRoot,
		TaskShortID:         "WOR-102",
		ResolvedCommit:      commit,
		WorktreeRoot:        worktreeRoot,
	}); err != nil {
		t.Fatalf("ProvisionExecutionTargetWorktree: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	exact, err := env.service.InspectExecutionTargetWorktree(env.ctx, InspectExecutionTargetWorktreeRequest{
		SourceWorkspaceRoot: env.workspaceRoot,
		WorktreeRoot:        canonicalWorktreeRoot,
		TaskShortID:         "WOR-102",
		ResolvedCommit:      commit,
	})
	if err != nil {
		t.Fatalf("InspectExecutionTargetWorktree exact: %v", err)
	}
	if exact.LinkedWorktreeOwnership == nil {
		t.Fatal("exact inspection is missing ownership")
	}
	if err := os.Rename(canonicalWorktreeRoot, filepath.Join(t.TempDir(), "missing-root")); err != nil {
		t.Fatalf("rename managed worktree root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.workspaceRoot, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatalf("write later commit: %v", err)
	}
	runGit(t, env.workspaceRoot, "add", "later.txt")
	runGit(t, env.workspaceRoot, "commit", "-q", "-m", "later")
	runGit(t, env.workspaceRoot, "update-ref", "refs/heads/WOR-102", "HEAD")
	expectedBranchTip := commit
	inspection, err := env.service.InspectExecutionTargetWorktree(env.ctx, InspectExecutionTargetWorktreeRequest{
		SourceWorkspaceRoot: env.workspaceRoot,
		WorktreeRoot:        canonicalWorktreeRoot,
		TaskShortID:         "WOR-102",
		ResolvedCommit:      commit,
		ExpectedOwnership:   exact.LinkedWorktreeOwnership,
		ExpectedBranchTip:   &expectedBranchTip,
	})
	if err != nil {
		t.Fatalf("InspectExecutionTargetWorktree after branch move: %v", err)
	}
	if inspection.Kind != ExecutionTargetWorktreeInspectionAmbiguous {
		t.Fatalf("inspection after branch move = %+v, want ambiguous", inspection)
	}
}

func TestInspectExecutionTargetWorktreeClassifiesExactProvisioningEvidence(t *testing.T) {
	env := newServiceTestEnv(t)
	commit := runGit(t, env.workspaceRoot, "rev-parse", "HEAD")
	worktreeRoot, err := env.service.PlanExecutionTargetWorktreeRoot(env.binding.WorkspaceID, "WOR-101")
	if err != nil {
		t.Fatalf("PlanExecutionTargetWorktreeRoot: %v", err)
	}
	request := InspectExecutionTargetWorktreeRequest{
		SourceWorkspaceRoot: env.workspaceRoot,
		WorktreeRoot:        worktreeRoot,
		TaskShortID:         "WOR-101",
		ResolvedCommit:      commit,
	}
	inspection, err := env.service.InspectExecutionTargetWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("InspectExecutionTargetWorktree before provision: %v", err)
	}
	if inspection.Kind != ExecutionTargetWorktreeInspectionNoSideEffects {
		t.Fatalf("inspection before provision = %+v, want no side effects", inspection)
	}
	if _, err := env.service.ProvisionExecutionTargetWorktree(env.ctx, ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         env.binding.WorkspaceID,
		SourceWorkspaceRoot: env.workspaceRoot,
		TaskShortID:         "WOR-101",
		ResolvedCommit:      commit,
		WorktreeRoot:        worktreeRoot,
	}); err != nil {
		t.Fatalf("ProvisionExecutionTargetWorktree: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	inspection, err = env.service.InspectExecutionTargetWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("InspectExecutionTargetWorktree after provision: %v", err)
	}
	if inspection.Kind != ExecutionTargetWorktreeInspectionExact ||
		inspection.BranchName != "WOR-101" ||
		inspection.ExactBranchObservation != commit ||
		inspection.LinkedWorktreeOwnership == nil ||
		inspection.LinkedWorktreeOwnership.CommonDir == "" ||
		inspection.LinkedWorktreeOwnership.AdminEntry == "" ||
		inspection.LinkedWorktreeOwnership.GitDir != filepath.Join(canonicalWorktreeRoot, ".git") ||
		inspection.LinkedWorktreeOwnership.HeadRef != "refs/heads/WOR-101" {
		t.Fatalf("inspection after provision = %+v ownership = %+v, want exact task branch ownership evidence", inspection, inspection.LinkedWorktreeOwnership)
	}
	inspection, err = env.service.InspectExecutionTargetWorktree(env.ctx, InspectExecutionTargetWorktreeRequest{
		SourceWorkspaceRoot: env.workspaceRoot,
		WorktreeRoot:        filepath.Join(t.TempDir(), "other-root"),
		TaskShortID:         "WOR-101",
		ResolvedCommit:      commit,
	})
	if err != nil {
		t.Fatalf("InspectExecutionTargetWorktree ambiguous branch: %v", err)
	}
	if inspection.Kind != ExecutionTargetWorktreeInspectionAmbiguous {
		t.Fatalf("inspection with surviving branch = %+v, want ambiguous", inspection)
	}
}

func TestRunExecutionTargetSetupRunsFromProvisionedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	scriptRelpath := filepath.Join("scripts", "target-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), "#!/bin/sh\nprintf setup > setup.marker\n")
	env.service.setupScript = scriptRelpath
	worktreeRoot, err := env.service.PlanExecutionTargetWorktreeRoot(env.binding.WorkspaceID, "WOR-100")
	if err != nil {
		t.Fatalf("PlanExecutionTargetWorktreeRoot: %v", err)
	}
	provisioned, err := env.service.ProvisionExecutionTargetWorktree(env.ctx, ProvisionExecutionTargetWorktreeRequest{
		WorkspaceID:         env.binding.WorkspaceID,
		SourceWorkspaceRoot: env.workspaceRoot,
		TaskShortID:         "WOR-100",
		ResolvedCommit:      runGit(t, env.workspaceRoot, "rev-parse", "HEAD"),
		WorktreeRoot:        worktreeRoot,
	})
	if err != nil {
		t.Fatalf("ProvisionExecutionTargetWorktree: %v", err)
	}
	if err := env.service.RunExecutionTargetSetup(env.ctx, RunExecutionTargetSetupRequest{
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SourceWorkspaceRoot: env.workspaceRoot,
		WorktreeRoot:        provisioned.WorktreeRoot,
		BranchName:          provisioned.BranchName,
		ProjectID:           env.binding.ProjectID,
		WorkspaceID:         env.binding.WorkspaceID,
		WorktreeID:          "worktree-target-setup",
		CreatedBranch:       provisioned.CreatedBranch,
	}); err != nil {
		t.Fatalf("RunExecutionTargetSetup: %v", err)
	}
	if got := waitForFileText(t, filepath.Join(provisioned.WorktreeRoot, "setup.marker")); got != "setup" {
		t.Fatalf("setup marker = %q, want setup", got)
	}
}

func TestRepositoryMutationLockSerializesLinkedWorkspaceBindings(t *testing.T) {
	env := newServiceTestEnv(t)
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", "lock-linked", linkedRoot, "HEAD")
	t.Cleanup(func() {
		_ = env.service.git.Remove(context.Background(), env.workspaceRoot, linkedRoot, true)
	})
	linkedBinding, err := env.store.RegisterWorkspaceBinding(env.ctx, linkedRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding linked worktree: %v", err)
	}
	if linkedBinding.ProjectID == env.binding.ProjectID {
		t.Fatalf("linked workspace project = %q, want a distinct project from %q", linkedBinding.ProjectID, env.binding.ProjectID)
	}

	releaseFirst, err := env.service.AcquireRepositoryMutationLock(env.ctx, env.workspaceRoot)
	if err != nil {
		t.Fatalf("AcquireRepositoryMutationLock main workspace: %v", err)
	}
	firstReleased := false
	t.Cleanup(func() {
		if !firstReleased {
			releaseFirst()
		}
	})
	type acquisition struct {
		release func()
		err     error
	}
	acquired := make(chan acquisition, 1)
	go func() {
		release, err := env.service.AcquireRepositoryMutationLock(context.Background(), linkedRoot)
		acquired <- acquisition{release: release, err: err}
	}()
	select {
	case result := <-acquired:
		if result.release != nil {
			result.release()
		}
		t.Fatalf("linked workspace acquired repository mutation lock before release: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseFirst()
	firstReleased = true
	select {
	case result := <-acquired:
		if result.err != nil {
			t.Fatalf("AcquireRepositoryMutationLock linked workspace: %v", result.err)
		}
		result.release()
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for linked workspace repository mutation lock")
	}
}

func TestEnsureTaskWorktreeRunsSetupAndPublishesProgressBeforeReturning(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	startedPath := filepath.Join(t.TempDir(), "started")
	releasePath := filepath.Join(t.TempDir(), "release")
	markerPath := filepath.Join(t.TempDir(), "marker")
	payloadPath := filepath.Join(t.TempDir(), "payload.json")
	scriptRelpath := filepath.Join("scripts", "task-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nprintf started > %q\ncat > %q\nwhile [ ! -f %q ]; do sleep 0.02; done\nprintf marker > %q\n", startedPath, payloadPath, releasePath, markerPath))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	type ensureResult struct {
		resp EnsureTaskWorktreeResponse
		err  error
	}
	resultCh := make(chan ensureResult, 1)
	go func() {
		resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID, SetupOperationID: setupID})
		resultCh <- ensureResult{resp: resp, err: err}
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
		t.Fatalf("EnsureTaskWorktree returned before setup release: resp=%+v err=%v", result.resp, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o644); err != nil {
		t.Fatalf("release setup: %v", err)
	}
	var result ensureResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for EnsureTaskWorktree")
	}
	if result.err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", result.err)
	}
	if got := waitForFileText(t, markerPath); got != "marker" {
		t.Fatalf("setup marker = %q, want marker", got)
	}
	payload := waitForSetupPayload(t, payloadPath)
	if payload.SourceWorkspaceRoot != env.workspaceRoot || payload.WorktreeRoot != result.resp.Worktree.CanonicalRoot {
		t.Fatalf("setup payload = %+v, want source %q worktree %q", payload, env.workspaceRoot, result.resp.Worktree.CanonicalRoot)
	}
}

func TestEnsureTaskWorktreeReturnsExistingManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	first, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree first: %v", err)
	}
	second, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree second: %v", err)
	}
	if second.Created || second.CreatedBranch {
		t.Fatalf("second ensure created flags = created:%t branch:%t, want false/false", second.Created, second.CreatedBranch)
	}
	if first.Worktree.WorktreeID != second.Worktree.WorktreeID {
		t.Fatalf("second worktree id = %q, want %q", second.Worktree.WorktreeID, first.Worktree.WorktreeID)
	}
}

func TestEnsureTaskWorktreeFailureRetryTrustsExistingWorktreeAndRecreatesRemovedRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	countPath := filepath.Join(t.TempDir(), "count")
	scriptRelpath := filepath.Join("scripts", "retry-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\ncount=0\nif [ -f %q ]; then count=$(cat %q); fi\ncount=$((count + 1))\nprintf '%%s' \"$count\" > %q\nif [ \"$count\" = \"1\" ]; then exit 3; fi\n", countPath, countPath, countPath))
	env.service.setupScript = scriptRelpath

	_, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID()})
	if err == nil {
		t.Fatal("first EnsureTaskWorktree succeeded, want setup failure")
	}
	row, err := env.store.Queries().GetTask(env.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.ManagedWorktreeID.Valid || strings.TrimSpace(row.ManagedWorktreeID.String) == "" {
		t.Fatalf("managed worktree not attached after setup failure: %+v", row.ManagedWorktreeID)
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

	second, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID()})
	if err != nil {
		t.Fatalf("second EnsureTaskWorktree should trust existing root: %v", err)
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
	third, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID()})
	if err != nil {
		t.Fatalf("third EnsureTaskWorktree should recreate removed root: %v", err)
	}
	if !third.Created {
		t.Fatalf("third ensure did not recreate worktree: %+v", third)
	}
	if got := waitForFileText(t, countPath); got != "2" {
		t.Fatalf("setup run count after recreate = %q, want 2", got)
	}
}

func TestEnsureTaskWorktreeUsesTaskSourceWorkspace(t *testing.T) {
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

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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

func TestEnsureTaskWorktreeHandlesRootCollisionAndReportsBranchCollision(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	baseRoot, err := defaultWorktreeRoot(env.baseDir, env.binding.WorkspaceID, task.ShortID)
	if err != nil {
		t.Fatalf("defaultWorktreeRoot: %v", err)
	}
	if err := os.MkdirAll(baseRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll collision root: %v", err)
	}

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree root collision: %v", err)
	}
	if resp.Worktree.CanonicalRoot == baseRoot {
		t.Fatalf("worktree root = %q, want suffixed root because base exists", resp.Worktree.CanonicalRoot)
	}
	if !strings.HasSuffix(resp.Worktree.CanonicalRoot, filepath.Base(baseRoot)+"-2") {
		t.Fatalf("worktree root = %q, want -2 suffix from existing collision behavior", resp.Worktree.CanonicalRoot)
	}

	otherTask, _ := createTaskWorktreeTestTask(t, env)
	runGit(t, env.workspaceRoot, "branch", otherTask.ShortID)
	_, err = env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: otherTask.ID})
	var branchCollision *TaskBranchCollisionError
	if !errors.As(err, &branchCollision) || branchCollision.BranchName != otherTask.ShortID {
		t.Fatalf("EnsureTaskWorktree branch collision error = %v, want task branch collision", err)
	}
}

func TestDeleteWorktreeBlocksNonTerminalTaskManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	created, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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
	created, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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
	created, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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
