package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"core/prompts"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

func TestTaskCreateAcceptsSourceWorkspace(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Source Workflow")
	createOut, _ := runWorkflowRootCommandOK(t, "task", "create", "--title", "Sourced", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID, "--source-workspace", binding.WorkspaceID)
	shortID := taskDetailHeadingShortID(t, createOut)
	resp, err := remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after create: %v", err)
	}
	if resp.Task.Summary.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("created task source workspace = %q, want %q", resp.Task.Summary.SourceWorkspaceID, binding.WorkspaceID)
	}
}

func TestTaskEditUpdatesFields(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Edit Workflow")
	createOut, _ := runWorkflowRootCommandOK(t, "task", "create", "--title", "Original", "--body", "Original body", "--workflow", workflowID, "--project", binding.ProjectID)
	shortID := taskDetailHeadingShortID(t, createOut)

	editOut, _ := runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--title", "Retitled", shortID)
	if editOut != "Edited task "+shortID+".\n" {
		t.Fatalf("task edit output = %q, want confirmation line", editOut)
	}
	resp, err := remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after title edit: %v", err)
	}
	if resp.Task.Summary.Title != "Retitled" || resp.Task.Body != "Original body" {
		t.Fatalf("after title edit title=%q body=%q, want retitled with unchanged body", resp.Task.Summary.Title, resp.Task.Body)
	}

	runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--body", "Edited body", shortID)
	resp, err = remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after body edit: %v", err)
	}
	if resp.Task.Summary.Title != "Retitled" || resp.Task.Body != "Edited body" {
		t.Fatalf("after body edit title=%q body=%q, want unchanged title with edited body", resp.Task.Summary.Title, resp.Task.Body)
	}

	runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--source-workspace", binding.WorkspaceID, shortID)
	resp, err = remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after source workspace edit: %v", err)
	}
	if resp.Task.Summary.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("after source workspace edit source=%q, want %q", resp.Task.Summary.SourceWorkspaceID, binding.WorkspaceID)
	}

	jsonOut, _ := runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--json", "--title", "JSON title", shortID)
	var updateResp serverapi.WorkflowTaskUpdateResponse
	if err := json.Unmarshal([]byte(jsonOut), &updateResp); err != nil {
		t.Fatalf("task edit --json output = %q, want JSON: %v", jsonOut, err)
	}
	if updateResp.Task.Title != "JSON title" || updateResp.Task.ShortID != shortID {
		t.Fatalf("task edit --json task = %+v, want updated summary", updateResp.Task)
	}
}

func TestTaskEditValidation(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Edit Validation Workflow")
	createOut, _ := runWorkflowRootCommandOK(t, "task", "create", "--title", "Original", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	shortID := taskDetailHeadingShortID(t, createOut)

	if _, stderr, code := runWorkflowRootCommand("task", "edit", "--project", binding.ProjectID); code != 2 || !strings.Contains(stderr, "requires <short-id-or-task-id>") {
		t.Fatalf("task edit without id code=%d stderr=%q, want positional requirement", code, stderr)
	}
	if _, stderr, code := runWorkflowRootCommand("task", "edit", "--project", binding.ProjectID, shortID); code != 2 || !strings.Contains(stderr, "at least one of") {
		t.Fatalf("task edit without fields code=%d stderr=%q, want field requirement", code, stderr)
	}
	if _, stderr, code := runWorkflowRootCommand("task", "edit", "--project", binding.ProjectID, "--body", "x", "--body-file", "/tmp/x", shortID); code != 2 || !strings.Contains(stderr, "--body cannot be combined with --body-file") {
		t.Fatalf("task edit body conflict code=%d stderr=%q, want mutual exclusion error", code, stderr)
	}
}

func TestTaskHumanOnlyActionsAreDeniedInsideKentSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		t.Fatal("human-only task command opened workflow remote")
		return config.App{}, nil, nil
	}
	defer func() {
		workflowCommandRemoteOpener = previous
	}()

	for _, args := range [][]string{
		{"task", "start", "TASK-1"},
		{"task", "cancel", "TASK-1"},
		{"task", "resume", "TASK-1"},
		{"task", "approve", "transition-1"},
		{"task", "move", "TASK-1", "node-1"},
		{"task", "comment", "delete", "comment-1"},
	} {
		stdout, stderr, code := runWorkflowRootCommand(args...)
		if code != 1 {
			t.Fatalf("%v exit = %d stderr=%q", args, code, stderr)
		}
		if stdout != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout)
		}
		if stderr != prompts.WorkflowHumanOnlyTaskActionDeniedPrompt+"\n" {
			t.Fatalf("%v stderr = %q, want denied prompt", args, stderr)
		}
	}
}

func TestTaskSafeActionsRemainAvailableInsideKentSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	_, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, remote.cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Safe Task Workflow").ID
	if workflowID == "" {
		t.Fatal("workflow create did not return a workflow id")
	}
	if _, nodeErr, code := runWorkflowRootCommand("workflow", "node", "add", workflowID, "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow node add exit=%d stderr=%q", code, nodeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "implement", "--context", "new_session", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow start edge add exit=%d stderr=%q", code, edgeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "implement", "--transition", "done", "--edge-key", "done", "--to", "done", "--context", "new_session"); code != 0 {
		t.Fatalf("workflow done edge add exit=%d stderr=%q", code, edgeErr)
	}
	if _, linkErr, code := runWorkflowRootCommand("workflow", "link", binding.ProjectID, workflowID, "--default"); code != 0 {
		t.Fatalf("workflow link exit=%d stderr=%q", code, linkErr)
	}

	taskOut, taskErr, code := runWorkflowRootCommand("task", "create", "--title", "Safe Task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID, "--source-url", "https://github.com/respawn-llc/kent/issues/123")
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	if !strings.Contains(taskOut, "Imported from: https://github.com/respawn-llc/kent/issues/123\n") {
		t.Fatalf("task create output = %q, want source URL", taskOut)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	if _, listErr, code := runWorkflowRootCommand("task", "list", "--project", binding.ProjectID); code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, listErr)
	}
	if _, showErr, code := runWorkflowRootCommand("task", "show", "--project", binding.ProjectID, shortID); code != 0 {
		t.Fatalf("task show exit=%d stderr=%q", code, showErr)
	}
	commentOut, commentErr, code := runWorkflowRootCommand("task", "comment", "add", "--project", binding.ProjectID, "--author", "user", "--author-id", "octocat", "--body", "note", shortID)
	if code != 0 {
		t.Fatalf("task comment add exit=%d stderr=%q", code, commentErr)
	}
	commentID := labeledOutputValue(t, commentOut, "comment_id")
	if commentID == "" {
		t.Fatalf("task comment add output = %q", commentOut)
	}
	commentListOut, commentListErr, code := runWorkflowRootCommand("task", "comment", "list", "--project", binding.ProjectID, shortID)
	if code != 0 {
		t.Fatalf("task comment list exit=%d stderr=%q", code, commentListErr)
	}
	if !strings.Contains(commentListOut, "octocat at ") {
		t.Fatalf("task comment list output = %q, want author id", commentListOut)
	}
	if _, replaceErr, code := runWorkflowRootCommand("task", "comment", "replace", "--body", "edited", commentID); code != 0 {
		t.Fatalf("task comment replace exit=%d stderr=%q", code, replaceErr)
	}
}

func TestTaskMutationOutputRenderers(t *testing.T) {
	task := serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "BLD-1", Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Workflow"},
		Placements: []serverapi.WorkflowPlacement{
			{ID: "placement-1", NodeID: "node-1", NodeKey: "implement"},
			{ID: "placement-2", NodeID: "node-2", NodeKey: "review"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-1", PlacementID: "placement-1", NodeID: "node-1", SessionID: "session-1"},
			{ID: "run-2", PlacementID: "placement-2", NodeID: "node-2", SessionID: "session-2"},
		},
		Transitions: []serverapi.WorkflowTaskTransition{
			{
				ID:            "transition-1",
				SourceNodeKey: "implement",
				TransitionID:  "done",
				Edges: []serverapi.WorkflowTransitionEdge{
					{EdgeKey: "done", TargetNodeKey: "review", State: "applied"},
				},
			},
		},
	}

	var start bytes.Buffer
	writeTaskStartResult(&start, task, serverapi.WorkflowTaskStartApplied{RunID: "run-1", PlacementID: "placement-1", TransitionID: "transition-start"})
	if got, want := start.String(), "Started task BLD-1 in session session-1 using workflow \"Workflow\" (workflow-1).\nFirst node: implement\n"; got != want {
		t.Fatalf("start output = %q, want %q", got, want)
	}

	var approve bytes.Buffer
	writeTaskTransitionResult(&approve, "Approved transition of", task, "transition-1", []string{"run-2"})
	if got, want := approve.String(), "Approved transition of BLD-1 from `implement` to `done`.\nBecause of this, started node review in session session-2.\n"; got != want {
		t.Fatalf("approve output = %q, want %q", got, want)
	}

	var move bytes.Buffer
	writeTaskTransitionResult(&move, "Moved task", task, "transition-1", nil)
	if got, want := move.String(), "Moved task BLD-1 from `implement` to `done`.\n"; got != want {
		t.Fatalf("move output = %q, want %q", got, want)
	}
}

func TestTaskStartExecutionTargetOverrideAndSelectionRequiredOutput(t *testing.T) {
	remote := &taskStartPollingRemote{
		projectID:   "project-1",
		taskID:      "task-1",
		shortID:     "BLD-1",
		workflowID:  "workflow-1",
		workflow:    "Workflow",
		placementID: "placement-1",
		runID:       "run-1",
		sessionID:   "session-1",
		nodeID:      "node-1",
		nodeKey:     "implement",
		startResponse: &serverapi.WorkflowTaskStartResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired,
			SelectionRequired: &serverapi.WorkflowExecutionTargetSelectionRequirement{
				Reason: serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: "."}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "start", "--project", remote.projectID, remote.shortID)
	if code != 1 || stdout != "" {
		t.Fatalf("selection-required exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{
		"Execution target selection is required: workflow policy requires selection.\n",
		"  --execution-target none\n",
		"  --execution-target head\n",
		"  --execution-target default-branch\n",
		"  --execution-target ref:<revision>\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("selection-required stderr = %q, want %q", stderr, want)
		}
	}

	remote.taskIDDetailCalls = 0
	stdout, stderr, code = runWorkflowRootCommand("task", "start", "--project", remote.projectID, "--json", remote.shortID)
	if code != 1 || stderr != "" {
		t.Fatalf("JSON selection-required exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var response serverapi.WorkflowTaskStartResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("decode JSON selection-required output %q: %v", stdout, err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired ||
		response.SelectionRequired == nil ||
		response.SelectionRequired.Reason != serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection {
		t.Fatalf("JSON selection-required response = %+v", response)
	}

	configuredRef := "release/v1"
	remote.startResponse = &serverapi.WorkflowTaskStartResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired,
		SelectionRequired: &serverapi.WorkflowExecutionTargetSelectionRequirement{
			Reason: serverapi.WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable,
			ConfiguredTarget: &serverapi.WorkflowExecutionTargetConfiguredTarget{
				Mode:         serverapi.WorkflowExecutionTargetModeCustomRef,
				RequestedRef: &configuredRef,
			},
			UnavailableCause: serverapi.WorkflowExecutionTargetUnavailableCauseInvalidRevision,
		},
	}
	_, stderr, code = runWorkflowRootCommand("task", "start", "--project", remote.projectID, remote.shortID)
	if code != 1 ||
		!strings.Contains(stderr, "ref:"+configuredRef) ||
		!strings.Contains(stderr, string(serverapi.WorkflowExecutionTargetUnavailableCauseInvalidRevision)) {
		t.Fatalf("configured-target-unavailable exit=%d stderr=%q", code, stderr)
	}

	remote.startResponse = nil
	remote.taskIDDetailCalls = 0
	stdout, stderr, code = runWorkflowRootCommand("task", "start", "--project", remote.projectID, "--execution-target", "ref:release/v1", remote.shortID)
	if code != 0 || stderr != "" {
		t.Fatalf("explicit target exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if remote.startRequest.ExecutionTarget == nil ||
		remote.startRequest.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeCustomRef ||
		remote.startRequest.ExecutionTarget.CustomRef == nil ||
		*remote.startRequest.ExecutionTarget.CustomRef != "release/v1" {
		t.Fatalf("start request execution target = %+v", remote.startRequest.ExecutionTarget)
	}
}

func TestTaskApproveAndMoveExecutionTargetOverrideAndSelectionRequiredOutput(t *testing.T) {
	for _, tc := range []struct {
		name            string
		args            []string
		setResponse     func(*setupProgressLifecycleRemote, serverapi.WorkflowExecutionTargetActionOutcome)
		executionTarget func(*setupProgressLifecycleRemote) *serverapi.WorkflowExecutionTargetSelection
	}{
		{
			name: "approve",
			args: []string{"task", "approve", "transition-1"},
			setResponse: func(remote *setupProgressLifecycleRemote, outcome serverapi.WorkflowExecutionTargetActionOutcome) {
				remote.approveResponse = &serverapi.WorkflowTaskApproveResponse{
					Outcome: outcome,
					SelectionRequired: &serverapi.WorkflowExecutionTargetSelectionRequirement{
						Reason: serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
					},
				}
			},
			executionTarget: func(remote *setupProgressLifecycleRemote) *serverapi.WorkflowExecutionTargetSelection {
				return remote.approveRequest.ExecutionTarget
			},
		},
		{
			name: "move",
			args: []string{"task", "move", "--project", "project-1", "BLD-1", "node-1"},
			setResponse: func(remote *setupProgressLifecycleRemote, outcome serverapi.WorkflowExecutionTargetActionOutcome) {
				remote.moveResponse = &serverapi.WorkflowTaskMoveResponse{
					Outcome: outcome,
					SelectionRequired: &serverapi.WorkflowExecutionTargetSelectionRequirement{
						Reason: serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
					},
				}
			},
			executionTarget: func(remote *setupProgressLifecycleRemote) *serverapi.WorkflowExecutionTargetSelection {
				return remote.moveRequest.ExecutionTarget
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := newSetupProgressLifecycleRemote()
			tc.setResponse(remote, serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired)
			restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: "."}, remote)
			defer restore()

			stdout, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 1 || stdout != "" {
				t.Fatalf("selection-required exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			for _, want := range []string{
				"Execution target selection is required: workflow policy requires selection.\n",
				"  --execution-target none\n",
				"  --execution-target head\n",
				"  --execution-target default-branch\n",
				"  --execution-target ref:<revision>\n",
			} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("selection-required stderr = %q, want %q", stderr, want)
				}
			}

			remote = newSetupProgressLifecycleRemote()
			restore()
			restore = replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: "."}, remote)
			args := append([]string{"task", tc.name, "--execution-target", "ref:release/v1"}, tc.args[2:]...)
			stdout, stderr, code = runWorkflowRootCommand(args...)
			if code != 0 {
				t.Fatalf("explicit target exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			selection := tc.executionTarget(remote)
			if selection == nil ||
				selection.Mode != serverapi.WorkflowExecutionTargetModeCustomRef ||
				selection.CustomRef == nil ||
				*selection.CustomRef != "release/v1" {
				t.Fatalf("request execution target = %+v", selection)
			}
		})
	}
}

func TestTaskApproveAndMoveRejectMalformedExecutionTargetBeforeOpeningRemote(t *testing.T) {
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		t.Fatal("malformed execution target opened workflow remote")
		return config.App{}, nil, nil
	}
	defer func() {
		workflowCommandRemoteOpener = previous
	}()

	for _, args := range [][]string{
		{"task", "approve", "transition-1", "--execution-target", "branch"},
		{"task", "move", "BLD-1", "node-1", "--execution-target", "ref:"},
	} {
		stdout, stderr, code := runWorkflowRootCommand(args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "execution target") {
			t.Fatalf("%v exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}

func TestWorkflowExecutionTargetErrorsExposeTypedFacts(t *testing.T) {
	for _, test := range []struct {
		err   error
		facts []string
	}{
		{
			err: &serverapi.WorkflowExecutionTargetResolutionError{
				Code:         serverapi.WorkflowExecutionTargetResolutionErrorInvalidRevision,
				RequestedRef: "missing-ref",
			},
			facts: []string{"missing-ref", string(serverapi.WorkflowExecutionTargetResolutionErrorInvalidRevision)},
		},
		{
			err: &serverapi.WorkflowLockedExecutionTargetError{
				Cause: serverapi.WorkflowLockedExecutionTargetCauseMissingBranch,
			},
			facts: []string{string(serverapi.WorkflowLockedExecutionTargetCauseMissingBranch)},
		},
	} {
		var output bytes.Buffer
		if !writeWorkflowExecutionTargetError(&output, test.err) {
			t.Fatalf("typed error %T was not handled", test.err)
		}
		for _, fact := range test.facts {
			if !strings.Contains(output.String(), fact) {
				t.Fatalf("typed error output %q omitted %q", output.String(), fact)
			}
		}
	}
}

func TestTaskStartSessionPollingTimeoutReportsStartedTask(t *testing.T) {
	remote := &taskSessionPollingRemote{task: serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "BLD-1", Title: "Task"},
		Runs:    []serverapi.WorkflowRun{{ID: "run-1"}},
	}}
	_, err := waitForWorkflowTaskRunSession(context.Background(), remote, "task-1", "run-1", 10*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatalf("waitForWorkflowTaskRunSession succeeded, want timeout")
	}
	if got := err.Error(); !strings.Contains(got, "started task BLD-1 with run run-1") || !strings.Contains(got, "session id was not assigned within") {
		t.Fatalf("timeout error = %q, want started task context and timeout detail", got)
	}
}

func TestTaskStartSessionPollingDoesNotWaitForScriptRunSession(t *testing.T) {
	remote := &taskSessionPollingRemote{task: serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "BLD-1", Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Workflow"},
		Placements: []serverapi.WorkflowPlacement{
			{ID: "placement-1", TaskID: "task-1", NodeID: "node-script", NodeKey: "script"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-1", TaskID: "task-1", PlacementID: "placement-1", NodeID: "node-script", NodeKind: "script"},
		},
	}}
	detail, err := waitForWorkflowTaskRunSession(context.Background(), remote, "task-1", "run-1", time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("waitForWorkflowTaskRunSession: %v", err)
	}
	var stdout bytes.Buffer
	writeTaskStartResult(&stdout, detail, serverapi.WorkflowTaskStartApplied{RunID: "run-1", PlacementID: "placement-1", TransitionID: "transition-start"})
	if got, want := stdout.String(), "Started task BLD-1 using workflow \"Workflow\" (workflow-1).\nFirst node: script\n"; got != want {
		t.Fatalf("start output = %q, want %q", got, want)
	}
}

func TestTaskStartCommandPollsForSessionAndPrintsReadableOutput(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &taskStartPollingRemote{
		projectID:   "project-1",
		taskID:      "task-1",
		shortID:     "BLD-1",
		workflowID:  "workflow-1",
		workflow:    "Workflow",
		placementID: "placement-1",
		runID:       "run-1",
		sessionID:   "session-1",
		nodeID:      "node-1",
		nodeKey:     "implement",
	}
	restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreRemote()

	stdout, stderr, code := runWorkflowRootCommand("task", "start", "--project", "project-1", "BLD-1")
	if code != 0 {
		t.Fatalf("task start exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	want := "Started task BLD-1 in session session-1 using workflow \"Workflow\" (workflow-1).\nFirst node: implement\n"
	if stdout != want {
		t.Fatalf("task start stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("task start stderr = %q, want empty", stderr)
	}
	if remote.taskIDDetailCalls < 2 {
		t.Fatalf("task detail calls = %d, want polling before session assignment", remote.taskIDDetailCalls)
	}
}

func TestTaskLifecycleCommandsConsumeWorktreeSetupProgressWithoutMutationDeadline(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "start", args: []string{"task", "start", "--project", "project-1", "BLD-1"}, want: "Started task BLD-1"},
		{name: "approve", args: []string{"task", "approve", "transition-1"}, want: "Approved transition of BLD-1"},
		{name: "move", args: []string{"task", "move", "--project", "project-1", "BLD-1", "node-1"}, want: "Moved task BLD-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := newSetupProgressLifecycleRemote()
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			stdout, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 0 {
				t.Fatalf("command exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.HasPrefix(stdout, tc.want) {
				t.Fatalf("stdout = %q, want lifecycle result prefix %q", stdout, tc.want)
			}
			assertSetupProgressOutputHasPaths(t, stderr, remote.scriptPath, remote.worktreeRoot)
			if !remote.mutationCalled {
				t.Fatal("workflow mutation was not called")
			}
		})
	}
}

func TestTaskLifecycleCommandsWarnAndContinueWhenSetupProgressUnavailable(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "start", args: []string{"task", "start", "--project", "project-1", "BLD-1"}, want: "Started task BLD-1"},
		{name: "approve", args: []string{"task", "approve", "transition-1"}, want: "Approved transition of BLD-1"},
		{name: "move", args: []string{"task", "move", "--project", "project-1", "BLD-1", "node-1"}, want: "Moved task BLD-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := newSetupProgressLifecycleRemote()
			remote.subscribeErr = errors.New("setup subscription unavailable")
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			stdout, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 0 {
				t.Fatalf("command exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.HasPrefix(stdout, tc.want) {
				t.Fatalf("stdout = %q, want lifecycle result prefix %q", stdout, tc.want)
			}
			if !strings.Contains(stderr, "warning: worktree setup progress subscription unavailable") {
				t.Fatalf("stderr = %q, want setup subscription warning", stderr)
			}
			if !remote.mutationCalled {
				t.Fatal("workflow mutation was not called")
			}
		})
	}
}

func TestTaskLifecycleCommandsWarnAndContinueWhenSetupProgressStreamFailsAfterMutation(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "start", args: []string{"task", "start", "--project", "project-1", "BLD-1"}, want: "Started task BLD-1"},
		{name: "approve", args: []string{"task", "approve", "transition-1"}, want: "Approved transition of BLD-1"},
		{name: "move", args: []string{"task", "move", "--project", "project-1", "BLD-1", "node-1"}, want: "Moved task BLD-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := newSetupProgressLifecycleRemote()
			remote.streamErrAfterEvent = errors.New("setup stream disconnected")
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			stdout, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 0 {
				t.Fatalf("command exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.HasPrefix(stdout, tc.want) {
				t.Fatalf("stdout = %q, want lifecycle result prefix %q", stdout, tc.want)
			}
			assertSetupProgressOutputHasPaths(t, stderr, remote.scriptPath, remote.worktreeRoot)
			if !strings.Contains(stderr, "warning: worktree setup progress stream ended unexpectedly") {
				t.Fatalf("stderr = %q, want setup stream warning", stderr)
			}
		})
	}
}

func replaceTaskStartSessionPolling(t *testing.T, timeout time.Duration, interval time.Duration) func() {
	t.Helper()
	originalTimeout := taskStartSessionPollTimeout
	originalInterval := taskStartSessionPollInterval
	taskStartSessionPollTimeout = timeout
	taskStartSessionPollInterval = interval
	return func() {
		taskStartSessionPollTimeout = originalTimeout
		taskStartSessionPollInterval = originalInterval
	}
}

type taskSessionPollingRemote struct {
	apicontract.WorkflowService
	task serverapi.WorkflowTaskDetail
}

func (r *taskSessionPollingRemote) Close() error { return nil }

func (r *taskSessionPollingRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskSessionPollingRemote) GetWorkflowTask(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{Task: r.task}, nil
}

type taskStartPollingRemote struct {
	apicontract.WorkflowService
	projectID         string
	taskID            string
	shortID           string
	workflowID        string
	workflow          string
	placementID       string
	runID             string
	sessionID         string
	nodeID            string
	nodeKey           string
	taskIDDetailCalls int
	startRequest      serverapi.WorkflowTaskStartRequest
	startResponse     *serverapi.WorkflowTaskStartResponse
}

func (r *taskStartPollingRemote) Close() error { return nil }

func (r *taskStartPollingRemote) SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return noopWorktreeSetupSubscription{}, nil
}

func (r *taskStartPollingRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{Binding: &serverapi.ProjectBinding{ProjectID: r.projectID}}, nil
}

func (r *taskStartPollingRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	if req.ProjectID == r.projectID && req.ShortID == r.shortID {
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail("")}, nil
	}
	if req.TaskID == r.taskID {
		r.taskIDDetailCalls++
		if r.taskIDDetailCalls == 1 {
			return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail("")}, nil
		}
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail(r.sessionID)}, nil
	}
	return serverapi.WorkflowTaskGetResponse{}, sql.ErrNoRows
}

func (r *taskStartPollingRemote) StartWorkflowTask(_ context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	r.startRequest = req
	if r.startResponse != nil {
		return *r.startResponse, nil
	}
	return serverapi.WorkflowTaskStartResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskStartApplied{TransitionID: "transition-1", PlacementID: r.placementID, RunID: r.runID},
	}, nil
}

func (r *taskStartPollingRemote) taskDetail(sessionID string) serverapi.WorkflowTaskDetail {
	return serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: r.taskID, ShortID: r.shortID, WorkflowID: r.workflowID, ProjectID: r.projectID, Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: r.workflowID, DisplayName: r.workflow},
		Placements: []serverapi.WorkflowPlacement{
			{ID: r.placementID, TaskID: r.taskID, NodeID: r.nodeID, NodeKey: r.nodeKey},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: r.runID, TaskID: r.taskID, PlacementID: r.placementID, NodeID: r.nodeID, SessionID: sessionID},
		},
	}
}

type setupProgressLifecycleRemote struct {
	apicontract.WorkflowService
	projectID           string
	taskID              string
	shortID             string
	workflowID          string
	workflow            string
	placementID         string
	runID               string
	sessionID           string
	nodeID              string
	nodeKey             string
	transitionID        string
	scriptPath          string
	worktreeRoot        string
	events              chan serverapi.WorktreeSetupEvent
	eventConsumed       chan struct{}
	subscribedID        serverapi.WorktreeSetupOperationID
	mutationCalled      bool
	subscribeErr        error
	streamErrAfterEvent error
	approveRequest      serverapi.WorkflowTaskApproveRequest
	approveResponse     *serverapi.WorkflowTaskApproveResponse
	moveRequest         serverapi.WorkflowTaskMoveRequest
	moveResponse        *serverapi.WorkflowTaskMoveResponse
}

func newSetupProgressLifecycleRemote() *setupProgressLifecycleRemote {
	return &setupProgressLifecycleRemote{
		projectID:     "project-1",
		taskID:        "task-1",
		shortID:       "BLD-1",
		workflowID:    "workflow-1",
		workflow:      "Workflow",
		placementID:   "placement-1",
		runID:         "run-1",
		sessionID:     "session-1",
		nodeID:        "node-1",
		nodeKey:       "implement",
		transitionID:  "transition-1",
		scriptPath:    "/tmp/kent-setup.sh",
		worktreeRoot:  "/tmp/kent-worktree",
		events:        make(chan serverapi.WorktreeSetupEvent, 1),
		eventConsumed: make(chan struct{}, 1),
	}
}

func (r *setupProgressLifecycleRemote) Close() error { return nil }

func (r *setupProgressLifecycleRemote) SubscribeWorktreeSetup(_ context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if r.subscribeErr != nil {
		return nil, r.subscribeErr
	}
	r.subscribedID = req.SetupOperationID
	return &setupProgressSubscription{events: r.events, consumed: r.eventConsumed, errAfterEvent: r.streamErrAfterEvent}, nil
}

func (r *setupProgressLifecycleRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{Binding: &serverapi.ProjectBinding{ProjectID: r.projectID}}, nil
}

func (r *setupProgressLifecycleRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	if req.ProjectID == r.projectID && req.ShortID == r.shortID {
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail("")}, nil
	}
	if req.TaskID == r.taskID {
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail(r.sessionID)}, nil
	}
	return serverapi.WorkflowTaskGetResponse{}, sql.ErrNoRows
}

func (r *setupProgressLifecycleRemote) StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	if err := r.validateMutationContextAndSetupID(ctx, req.SetupOperationID); err != nil {
		return serverapi.WorkflowTaskStartResponse{}, err
	}
	return serverapi.WorkflowTaskStartResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskStartApplied{TransitionID: r.transitionID, PlacementID: r.placementID, RunID: r.runID},
	}, nil
}

func (r *setupProgressLifecycleRemote) ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error) {
	if err := r.validateMutationContextAndSetupID(ctx, req.SetupOperationID); err != nil {
		return serverapi.WorkflowTaskApproveResponse{}, err
	}
	r.approveRequest = req
	if r.approveResponse != nil {
		return *r.approveResponse, nil
	}
	return serverapi.WorkflowTaskApproveResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskApproveApplied{TaskID: r.taskID, TransitionID: r.transitionID, State: "applied", RunIDs: []string{r.runID}},
	}, nil
}

func (r *setupProgressLifecycleRemote) MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	if err := r.validateMutationContextAndSetupID(ctx, req.SetupOperationID); err != nil {
		return serverapi.WorkflowTaskMoveResponse{}, err
	}
	r.moveRequest = req
	if r.moveResponse != nil {
		return *r.moveResponse, nil
	}
	return serverapi.WorkflowTaskMoveResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskMoveApplied{TransitionID: r.transitionID, State: "applied", RunIDs: []string{r.runID}},
	}, nil
}

func (r *setupProgressLifecycleRemote) validateMutationContextAndSetupID(ctx context.Context, setupOperationID serverapi.WorktreeSetupOperationID) error {
	r.mutationCalled = true
	if _, ok := ctx.Deadline(); ok {
		return errors.New("workflow lifecycle mutation context has a deadline")
	}
	if err := setupOperationID.Validate(); err != nil {
		return err
	}
	if r.subscribedID.Validate() != nil {
		return nil
	}
	if setupOperationID != r.subscribedID {
		return errors.New("workflow lifecycle mutation used a different setup operation id than the subscription")
	}
	r.events <- serverapi.WorktreeSetupEvent{
		SetupOperationID:    setupOperationID,
		SourceWorkspaceRoot: "/tmp/source",
		WorktreeRoot:        r.worktreeRoot,
		ScriptPath:          r.scriptPath,
		Phase:               serverapi.WorktreeSetupPhaseStarted,
	}
	select {
	case <-r.eventConsumed:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("setup progress event was not consumed while mutation was in flight")
	}
}

func (r *setupProgressLifecycleRemote) taskDetail(sessionID string) serverapi.WorkflowTaskDetail {
	return serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: r.taskID, ShortID: r.shortID, WorkflowID: r.workflowID, ProjectID: r.projectID, Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: r.workflowID, DisplayName: r.workflow},
		Placements: []serverapi.WorkflowPlacement{
			{ID: r.placementID, TaskID: r.taskID, NodeID: r.nodeID, NodeKey: r.nodeKey},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: r.runID, TaskID: r.taskID, PlacementID: r.placementID, NodeID: r.nodeID, SessionID: sessionID},
		},
		Transitions: []serverapi.WorkflowTaskTransition{
			{
				ID:            r.transitionID,
				SourceNodeKey: r.nodeKey,
				TransitionID:  "done",
				Edges: []serverapi.WorkflowTransitionEdge{
					{EdgeKey: "done", TargetNodeKey: "done", State: "applied"},
				},
			},
		},
	}
}

type setupProgressSubscription struct {
	events        <-chan serverapi.WorktreeSetupEvent
	consumed      chan<- struct{}
	errAfterEvent error
	delivered     bool
}

func (s *setupProgressSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	if s.delivered && s.errAfterEvent != nil {
		return serverapi.WorktreeSetupEvent{}, s.errAfterEvent
	}
	select {
	case event := <-s.events:
		s.delivered = true
		select {
		case s.consumed <- struct{}{}:
		default:
		}
		return event, nil
	case <-ctx.Done():
		return serverapi.WorktreeSetupEvent{}, ctx.Err()
	}
}

func (s *setupProgressSubscription) Close() error { return nil }

type noopWorktreeSetupSubscription struct{}

func (noopWorktreeSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	<-ctx.Done()
	return serverapi.WorktreeSetupEvent{}, ctx.Err()
}

func (noopWorktreeSetupSubscription) Close() error { return nil }

func assertSetupProgressOutputHasPaths(t *testing.T, output string, scriptPath string, worktreeRoot string) {
	t.Helper()
	fields := strings.Fields(output)
	if len(fields) < 8 {
		t.Fatalf("setup progress output fields = %v", fields)
	}
	scriptFound := false
	worktreeFound := false
	for _, field := range fields {
		if field == scriptPath {
			scriptFound = true
		}
		if strings.TrimSuffix(field, ".") == worktreeRoot {
			worktreeFound = true
		}
	}
	if !scriptFound || !worktreeFound {
		t.Fatalf("setup progress output = %q, want script %q and worktree %q", output, scriptPath, worktreeRoot)
	}
}
