package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/worktreesetup"
	"core/server/workflow"
	"core/server/workflowstore"
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

func TestEnsureTaskWorktreeCreatesShortIDBranchWithoutControllerLease(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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
	default:
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
	if payload.SourceWorkspaceRoot != env.workspaceRoot || payload.WorktreeRoot != taskWorktreeRoot(result.resp.Worktree) {
		t.Fatalf("setup payload = %+v, want source %q worktree %q", payload, env.workspaceRoot, taskWorktreeRoot(result.resp.Worktree))
	}
}

func TestEnsureTaskWorktreeSetupOmitsStaleParentSessionEnvironment(t *testing.T) {
	env := newServiceTestEnv(t)
	t.Setenv(setupEnvironmentKeySessionID, "stale-parent-session")
	t.Setenv(setupEnvironmentKeyWorktreeRoot, "stale-parent-worktree")
	task, _ := createTaskWorktreeTestTask(t, env)
	capture := worktreesetup.New(t, worktreesetup.Options{})
	env.service.setupScript = capture.Executable()

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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
	created := worktreeViewFromListEntryForTest(resp.Worktree)
	if err := invocation.Verify(worktreesetup.Payload{
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
	if taskWorktreeID(first.Worktree) != taskWorktreeID(second.Worktree) {
		t.Fatalf("second worktree id = %q, want %q", taskWorktreeID(second.Worktree), taskWorktreeID(first.Worktree))
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
	if taskWorktreeID(resp.Worktree) == "" || !strings.Contains(taskWorktreeRoot(resp.Worktree), source.WorkspaceID) {
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
	if taskWorktreeRoot(resp.Worktree) == baseRoot {
		t.Fatalf("worktree root = %q, want suffixed root because base exists", taskWorktreeRoot(resp.Worktree))
	}
	if !strings.HasSuffix(taskWorktreeRoot(resp.Worktree), filepath.Base(baseRoot)+"-2") {
		t.Fatalf("worktree root = %q, want -2 suffix from existing collision behavior", taskWorktreeRoot(resp.Worktree))
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
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
		Selector:            taskWorktreeID(created.Worktree),
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
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
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
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
	created, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: task.ID})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
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
