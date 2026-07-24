package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
)

func TestPreparedScriptWorkflowRunCompensationRetiresFailedCommit(t *testing.T) {
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	ref := sessionruntime.WorkflowExecutionRef{
		TaskID:     "task-script-compensation",
		RunID:      "run-script-compensation",
		Generation: 1,
	}
	preparedExecution, err := authority.PrepareScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &ref,
		Command:  sessionruntime.ScriptCommand{Path: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("PrepareScriptExecution: %v", err)
	}
	prepared := &preparedScriptWorkflowRun{prepared: preparedExecution}

	commitErr := prepared.Commit()
	if commitErr == nil {
		t.Fatal("committing a directory as an executable unexpectedly succeeded")
	}
	if err := prepared.Compensate(context.Background()); err != nil {
		t.Fatalf("Compensate failed script commit: %v", err)
	}
	snapshot, err := authority.CurrentTaskExecutionSnapshot(ref.TaskID)
	if err != nil {
		t.Fatalf("CurrentTaskExecutionSnapshot: %v", err)
	}
	if len(snapshot.Executions) != 0 {
		t.Fatalf("compensated failed script remains live: %+v", snapshot.Executions)
	}
	if _, waitErr := preparedExecution.Handle().Wait(context.Background()); !errors.Is(waitErr, commitErr) {
		t.Fatalf("execution result error = %v, want original commit failure %v", waitErr, commitErr)
	}
}

func TestExecuteWorkflowScriptUsesJSONStdinAndSeparatedStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX shebang")
	}
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin.json")
	workdirPath := filepath.Join(dir, "workdir.txt")
	runIDPath := filepath.Join(dir, "run-id.txt")
	scriptPath := filepath.Join(dir, "complete.sh")
	script := "#!/bin/sh\npwd > " + shellQuote(workdirPath) + "\nprintf '%s' \"$KENT_WORKFLOW_RUN_ID\" > " + shellQuote(runIDPath) + "\ncat > " + shellQuote(stdinPath) + "\nprintf 'diagnostic' >&2\nprintf '{\"done\":\"ok\"}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	req := SchedulerStartRunRequest{RunID: "run_script", TaskID: "task_1", PlacementID: "placement_1", NodeID: "node_script", Generation: 1}
	input := workflowstore.RunStartContext{
		Task:        workflowstore.TaskRecord{ID: "task_1", WorkflowID: "workflow_1"},
		Node:        workflowstore.NodeRecord{ID: "node_script", WorkflowID: "workflow_1", Kind: workflow.NodeKindScript, ScriptPath: "complete.sh"},
		InputValues: map[string]string{"summary": "hello"},
		ExecutionRoot: &workflowstore.ExecutionRoot{
			SourceWorkspaceID:   "workspace_1",
			SourceWorkspaceRoot: dir,
		},
	}

	scriptRequest, _, err := workflowScriptExecutionRequest(req, input)
	if err != nil {
		t.Fatalf("build script execution request: %v", err)
	}
	if scriptRequest.Workflow == nil || *scriptRequest.Workflow != (sessionruntime.WorkflowExecutionRef{TaskID: req.TaskID, RunID: req.RunID, Generation: req.Generation}) {
		t.Fatalf("workflow execution ref = %#v", scriptRequest.Workflow)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	handle, err := authority.StartScriptExecution(context.Background(), scriptRequest)
	if err != nil {
		t.Fatalf("start script execution: %v", err)
	}
	execution, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait script execution: %v", err)
	}
	if execution.Script == nil {
		t.Fatal("script execution result is missing")
	}

	if string(execution.Script.Stdout) != `{"done":"ok"}` {
		t.Fatalf("stdout = %q", string(execution.Script.Stdout))
	}
	if string(execution.Script.Stderr) != "diagnostic" {
		t.Fatalf("stderr = %q", string(execution.Script.Stderr))
	}
	workdir, err := os.ReadFile(workdirPath)
	if err != nil {
		t.Fatalf("read script workdir: %v", err)
	}
	wantWorkdir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve expected workdir: %v", err)
	}
	if strings.TrimSpace(string(workdir)) != wantWorkdir {
		t.Fatalf("script workdir = %q, want %q", strings.TrimSpace(string(workdir)), wantWorkdir)
	}
	runID, err := os.ReadFile(runIDPath)
	if err != nil {
		t.Fatalf("read script run id: %v", err)
	}
	if string(runID) != string(req.RunID) {
		t.Fatalf("script run id = %q, want %q", string(runID), req.RunID)
	}
	stdinBytes, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	var stdin map[string]any
	if err := json.Unmarshal(stdinBytes, &stdin); err != nil {
		t.Fatalf("decode stdin: %v", err)
	}
	if stdin["summary"] != "hello" {
		t.Fatalf("summary stdin = %#v", stdin["summary"])
	}
	kent, ok := stdin["_kent"].(map[string]any)
	if !ok {
		t.Fatalf("_kent stdin = %#v", stdin["_kent"])
	}
	if kent["run_id"] != "run_script" || kent["placement_id"] != "placement_1" {
		t.Fatalf("_kent = %#v", kent)
	}
}

func TestWorkflowScriptEnvUsesSourceExecutionRootWithoutWorktreeVariable(t *testing.T) {
	sourceRoot := t.TempDir()
	input := workflowstore.RunStartContext{
		Task: workflowstore.TaskRecord{ID: "task_1", WorkflowID: "workflow_1"},
		Node: workflowstore.NodeRecord{ID: "node_script", WorkflowID: "workflow_1", Kind: workflow.NodeKindScript},
		ExecutionRoot: &workflowstore.ExecutionRoot{
			SourceWorkspaceID:   "workspace_1",
			SourceWorkspaceRoot: sourceRoot,
		},
	}
	env, err := workflowScriptEnv(SchedulerStartRunRequest{RunID: "run_script", PlacementID: "placement_1"}, input)
	if err != nil {
		t.Fatalf("workflowScriptEnv: %v", err)
	}
	if value, ok := environmentValue(env, "KENT_EXECUTION_ROOT"); !ok || value != sourceRoot {
		t.Fatalf("KENT_EXECUTION_ROOT = %q, present=%t; want %q", value, ok, sourceRoot)
	}
	if value, ok := environmentValue(env, "KENT_WORKTREE_ROOT"); ok {
		t.Fatalf("KENT_WORKTREE_ROOT = %q, want variable omitted for source execution root", value)
	}
}

func TestWorkflowScriptEnvIncludesManagedExecutionAndWorktreeRoots(t *testing.T) {
	sourceRoot := t.TempDir()
	worktreeRoot := t.TempDir()
	input := workflowstore.RunStartContext{
		Task: workflowstore.TaskRecord{ID: "task_1", WorkflowID: "workflow_1"},
		Node: workflowstore.NodeRecord{ID: "node_script", WorkflowID: "workflow_1", Kind: workflow.NodeKindScript},
		ExecutionRoot: &workflowstore.ExecutionRoot{
			SourceWorkspaceID:   "workspace_1",
			SourceWorkspaceRoot: sourceRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: "worktree_1",
				Root:       worktreeRoot,
			},
		},
	}
	env, err := workflowScriptEnv(SchedulerStartRunRequest{RunID: "run_script", PlacementID: "placement_1"}, input)
	if err != nil {
		t.Fatalf("workflowScriptEnv: %v", err)
	}
	if value, ok := environmentValue(env, "KENT_EXECUTION_ROOT"); !ok || value != worktreeRoot {
		t.Fatalf("KENT_EXECUTION_ROOT = %q, present=%t; want %q", value, ok, worktreeRoot)
	}
	if value, ok := environmentValue(env, "KENT_WORKTREE_ROOT"); !ok || value != worktreeRoot {
		t.Fatalf("KENT_WORKTREE_ROOT = %q, present=%t; want %q", value, ok, worktreeRoot)
	}
}

func TestWorkflowScriptExecutionCancellationKillsTermIgnoringProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX signal handling")
	}
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "process-identity.json")
	scriptPath := filepath.Join(dir, "ignore-term.sh")
	script := "#!/bin/sh\ntrap '' TERM\nidentity_path=" + shellQuote(identityPath) + "\nidentity_tmp=\"${identity_path}.$$\"\nprintf '{\"process_group_id\":%s}\\n' \"$$\" > \"$identity_tmp\"\nmv \"$identity_tmp\" \"$identity_path\"\nwhile true; do sleep 1; done\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	req := SchedulerStartRunRequest{RunID: "run_script", TaskID: "task_1", PlacementID: "placement_1", NodeID: "node_script", Generation: 1}
	input := workflowstore.RunStartContext{
		Task:        workflowstore.TaskRecord{ID: "task_1", WorkflowID: "workflow_1"},
		Node:        workflowstore.NodeRecord{ID: "node_script", WorkflowID: "workflow_1", Kind: workflow.NodeKindScript, ScriptPath: "ignore-term.sh"},
		InputValues: map[string]string{},
		ExecutionRoot: &workflowstore.ExecutionRoot{
			SourceWorkspaceID:   "workspace_1",
			SourceWorkspaceRoot: dir,
		},
	}
	scriptRequest, _, err := workflowScriptExecutionRequest(req, input)
	if err != nil {
		t.Fatalf("build script execution request: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	handle, err := authority.StartScriptExecution(context.Background(), scriptRequest)
	if err != nil {
		t.Fatalf("start script execution: %v", err)
	}
	identity := waitForWorkflowScriptProcessIdentity(t, identityPath, 5*time.Second)
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := handle.Stop(stopCtx); err != nil {
		t.Fatalf("stop script execution: %v", err)
	}
	execution, err := handle.Wait(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("script execution error = %v, want context cancellation", err)
	}
	if execution.Script == nil || !execution.Script.Canceled {
		t.Fatalf("script execution result = %#v, want canceled script result", execution.Script)
	}
	assertScriptProcessGroupGone(t, identity.ProcessGroupID)
}

type workflowScriptProcessIdentity struct {
	ProcessGroupID int `json:"process_group_id"`
}

func waitForWorkflowScriptProcessIdentity(t *testing.T, path string, timeout time.Duration) workflowScriptProcessIdentity {
	t.Helper()
	var identity workflowScriptProcessIdentity
	testsetup.RequireUntil(t, time.Now().Add(timeout), 10*time.Millisecond, func() bool {
		body, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(body, &identity); err != nil {
				t.Fatalf("decode process identity %s: %v", path, err)
			}
			if identity.ProcessGroupID <= 0 {
				t.Fatalf("process identity = %+v, want positive process group id", identity)
			}
			return true
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read process identity %s: %v", path, err)
		}
		return false
	}, "timed out waiting for %s", path)
	return identity
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func environmentValue(environment []string, name string) (string, bool) {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}
