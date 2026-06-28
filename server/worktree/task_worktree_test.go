package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestEnsureTaskWorktreeCreatesShortIDBranchWithoutControllerLease(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
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

func TestEnsureTaskWorktreeReturnsExistingManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)

	first, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("EnsureTaskWorktree first: %v", err)
	}
	second, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
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

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
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

	resp, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
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
	_, err = env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(otherTask.ID)})
	var branchCollision *TaskBranchCollisionError
	if !errors.As(err, &branchCollision) || branchCollision.BranchName != otherTask.ShortID {
		t.Fatalf("EnsureTaskWorktree branch collision error = %v, want task branch collision", err)
	}
}

func TestDeleteWorktreeBlocksNonTerminalTaskManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	task, _ := createTaskWorktreeTestTask(t, env)
	created, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
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
	created, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
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
	created, err := env.service.EnsureTaskWorktree(env.ctx, EnsureTaskWorktreeRequest{TaskID: string(task.ID)})
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
		if node.Kind == kind {
			return node.ID
		}
	}
	t.Fatalf("node kind %q not found", kind)
	return ""
}
