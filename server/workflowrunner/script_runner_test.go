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

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestExecuteWorkflowScriptUsesJSONStdinAndSeparatedStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX shebang")
	}
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin.json")
	scriptPath := filepath.Join(dir, "complete.sh")
	script := "#!/bin/sh\ncat > " + shellQuote(stdinPath) + "\nprintf 'diagnostic' >&2\nprintf '{\"done\":\"ok\"}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	req := SchedulerStartRunRequest{RunID: "run_script", TaskID: "task_1", PlacementID: "placement_1", NodeID: "node_script", Generation: 1}
	input := workflowstore.RunStartContext{
		Task:         workflowstore.TaskRecord{ID: "task_1", WorkflowID: "workflow_1"},
		Node:         workflowstore.NodeRecord{ID: "node_script", WorkflowID: "workflow_1", Kind: workflow.NodeKindScript, ScriptPath: "complete.sh"},
		InputValues:  map[string]string{"summary": "hello"},
		WorktreeRoot: dir,
	}

	result, err := executeWorkflowScript(context.Background(), req, input)
	if err != nil {
		t.Fatalf("execute script: %v detail=%s", err, scriptFailureDetailJSON(err, result))
	}

	if string(result.Stdout) != `{"done":"ok"}` {
		t.Fatalf("stdout = %q", string(result.Stdout))
	}
	if string(result.Stderr) != "diagnostic" {
		t.Fatalf("stderr = %q", string(result.Stderr))
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

func TestExecuteWorkflowScriptCancelKillsTermIgnoringProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX signal handling")
	}
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "started")
	scriptPath := filepath.Join(dir, "ignore-term.sh")
	script := "#!/bin/sh\ntrap '' TERM\nprintf started > " + shellQuote(markerPath) + "\nwhile true; do sleep 1; done\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	req := SchedulerStartRunRequest{RunID: "run_script", TaskID: "task_1", PlacementID: "placement_1", NodeID: "node_script", Generation: 1}
	input := workflowstore.RunStartContext{
		Task:         workflowstore.TaskRecord{ID: "task_1", WorkflowID: "workflow_1"},
		Node:         workflowstore.NodeRecord{ID: "node_script", WorkflowID: "workflow_1", Kind: workflow.NodeKindScript, ScriptPath: "ignore-term.sh"},
		InputValues:  map[string]string{},
		WorktreeRoot: dir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type scriptResult struct {
		result workflowScriptResult
		err    error
	}
	done := make(chan scriptResult, 1)
	go func() {
		result, err := executeWorkflowScript(ctx, req, input)
		done <- scriptResult{result: result, err: err}
	}()
	waitForFile(t, markerPath, 5*time.Second)

	cancel()

	select {
	case got := <-done:
		var scriptErr workflowScriptError
		if !errors.As(got.err, &scriptErr) || scriptErr.Reason != ReasonRuntimeCanceled {
			t.Fatalf("execute error = %#v, want runtime cancellation", got.err)
		}
		if !got.result.Canceled {
			t.Fatalf("result canceled = false, want true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("script cancellation did not kill TERM-ignoring process")
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
