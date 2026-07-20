package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/server/workflow"
	"core/shared/serverapi"
)

func TestTaskCommandsUseWorkflowAPI(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Task Workflow API")

	taskOut, _ := runRootCommandOK(t, "task", "create", "--title", "Task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	shortID := taskDetailHeadingShortID(t, taskOut)
	if !strings.Contains(taskOut, shortID+": Task\n") || !strings.Contains(taskOut, "Body:\n```md\nBody\n```\n") || !strings.Contains(taskOut, "Status: backlog\n") {
		t.Fatalf("task create output = %q, want task show output", taskOut)
	}
	taskResp, err := remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after create: %v", err)
	}
	taskID := taskResp.Task.Summary.ID

	taskListJSONOut, _ := runRootCommandOK(t, "task", "list", "--project", binding.ProjectID, "--json")
	var taskList taskListOutput
	if err := json.Unmarshal([]byte(taskListJSONOut), &taskList); err != nil {
		t.Fatalf("task list JSON output = %q: %v", taskListJSONOut, err)
	}
	if taskList.ProjectID != binding.ProjectID ||
		taskList.WorkflowID != nil ||
		len(taskList.Tasks) != 1 ||
		taskList.Tasks[0].TaskID != taskID ||
		taskList.Tasks[0].ShortID != shortID ||
		taskList.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindBacklog ||
		taskList.Tasks[0].ColumnKeys != nil {
		t.Fatalf("task list projection = %+v, want project-wide task without workflow-relative columns", taskList)
	}

	taskShowOut, _ := runRootCommandOK(t, "task", "show", "--project", binding.ProjectID, shortID)
	if !strings.Contains(taskShowOut, shortID+": Task\n") || !strings.Contains(taskShowOut, "Body:\n```md\nBody\n```\n") || !strings.Contains(taskShowOut, "Status: backlog\n") {
		t.Fatalf("task show output = %q, want summary block", taskShowOut)
	}
	taskShowJSONOut, _ := runRootCommandOK(t, "task", "show", "--project", binding.ProjectID, "--json", shortID)
	var taskShowJSON taskShowOutput
	if err := json.Unmarshal([]byte(taskShowJSONOut), &taskShowJSON); err != nil {
		t.Fatalf("task show --json output = %q, want JSON: %v", taskShowJSONOut, err)
	}
	if taskShowJSON.Summary.ID != taskID || taskShowJSON.Summary.ShortID != shortID || taskShowJSON.Body != "Body" {
		t.Fatalf("task show --json output = %+v, want minimal task detail summary", taskShowJSON)
	}
	var taskShowJSONFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(taskShowJSONOut), &taskShowJSONFields); err != nil {
		t.Fatalf("task show --json output = %q, want JSON object: %v", taskShowJSONOut, err)
	}
	for _, omitted := range []string{"attention", "placements", "runs", "transitions", "comments"} {
		if _, ok := taskShowJSONFields[omitted]; ok {
			t.Fatalf("task show --json output = %q, did not expect unbounded %q array", taskShowJSONOut, omitted)
		}
	}
	taskShowOut, _ = runRootCommandOK(t, "task", "show", "--project", "missing-project", taskID)
	if !strings.Contains(taskShowOut, shortID+": Task\n") {
		t.Fatalf("task show by full id output = %q, want task short id", taskShowOut)
	}

	commentOut, _ := runRootCommandOK(t, "task", "comment", "add", "--project", binding.ProjectID, "--body", "note", shortID)
	commentID := labeledOutputValue(t, commentOut, "comment_id")
	if commentID == "" {
		t.Fatalf("comment output = %q, want comment id", commentOut)
	}
	runRootCommandOK(t, "task", "comment", "replace", "--body", "edited", commentID)
	commentListOut, _ := runRootCommandOK(t, "task", "comment", "list", "--project", binding.ProjectID, shortID)
	if !strings.Contains(commentListOut, "Comments (1):\nUser at ") || !strings.Contains(commentListOut, "edited") {
		t.Fatalf("comment list output = %q, want rendered comment", commentListOut)
	}
	runRootCommandOK(t, "task", "comment", "delete", commentID)

	startResp, err := remote.StartWorkflowTask(context.Background(), serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask for resume command: %v", err)
	}
	if startResp.Applied == nil {
		t.Fatalf("StartWorkflowTask response = %+v, want applied payload", startResp)
	}
	runID := startResp.Applied.RunID
	claimed, err := remote.store.ClaimRun(context.Background(), workflow.RunID(runID), 0)
	if err != nil {
		t.Fatalf("ClaimRun for resume command: %v", err)
	}
	resumeSessionID := createWorkflowCommandTestSession(t, cfg, binding, remote.metadataStore)
	if err := remote.store.AttachRunSession(context.Background(), workflow.RunID(runID), claimed.Generation, resumeSessionID); err != nil {
		t.Fatalf("AttachRunSession for resume command: %v", err)
	}
	if err := remote.store.InterruptRunGeneration(context.Background(), workflow.RunID(runID), claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration for resume command: %v", err)
	}
	resumeOut, _ := runRootCommandOK(t, "task", "resume", "--project", binding.ProjectID, shortID)
	if !strings.Contains(resumeOut, shortID) || !strings.Contains(resumeOut, resumeSessionID) || !strings.Contains(resumeOut, string(claimed.NodeID)) {
		t.Fatalf("resume output = %q, want task/node/session referenced", resumeOut)
	}

	cancelOut, _ := runRootCommandOK(t, "task", "cancel", "--project", binding.ProjectID, "--reason", "test", shortID)
	if cancelOut != "Canceled task "+shortID+".\n" {
		t.Fatalf("cancel output = %q, want readable cancel message", cancelOut)
	}

	if _, resumeErr, resumeCode := runRootCommand("task", "resume"); resumeCode != 2 || !strings.Contains(resumeErr, "requires <short-id-or-task-id>") {
		t.Fatalf("task resume validation code=%d stderr=%q, want task requirement", resumeCode, resumeErr)
	}
	if _, approveErr, approveCode := runRootCommand("task", "approve"); approveCode != 2 || !strings.Contains(approveErr, "requires <transition-id>") {
		t.Fatalf("task approve validation code=%d stderr=%q, want transition id requirement", approveCode, approveErr)
	}
	if _, moveErr, moveCode := runRootCommand("task", "move"); moveCode != 2 || !strings.Contains(moveErr, "requires <short-id-or-task-id> <target-node-id>") {
		t.Fatalf("task move validation code=%d stderr=%q, want task and target requirement", moveCode, moveErr)
	}
}
