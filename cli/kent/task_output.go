package main

import (
	"fmt"
	"io"
	"strings"

	"core/shared/serverapi"
)

func writeTaskStartResult(stdout io.Writer, task serverapi.WorkflowTaskDetail, applied serverapi.WorkflowTaskStartApplied) {
	if len(task.CurrentSessionIDs) == 1 {
		fmt.Fprintf(stdout, "Started task %s in session %s using workflow %q (%s).\n", taskDisplayID(task), task.CurrentSessionIDs[0], task.Workflow.DisplayName, task.Workflow.WorkflowID)
		return
	}
	for _, script := range task.CurrentScripts {
		if script.RunID == applied.RunID {
			fmt.Fprintf(stdout, "Started task %s with script %s using workflow %q (%s).\n", taskDisplayID(task), script.Path, task.Workflow.DisplayName, task.Workflow.WorkflowID)
			return
		}
	}
	fmt.Fprintf(stdout, "Started task %s using workflow %q (%s).\n", taskDisplayID(task), task.Workflow.DisplayName, task.Workflow.WorkflowID)
}

func writeTaskResumeResult(stdout io.Writer, task serverapi.WorkflowTaskDetail, resp serverapi.WorkflowTaskResumeResponse) {
	fmt.Fprintf(stdout, "Resumed task %s.\n", taskDisplayID(task))
	for _, run := range resp.Runs {
		sessionID := strings.TrimSpace(run.SessionID)
		if sessionID == "" {
			fmt.Fprintf(stdout, "Resumed node %s.\n", run.NodeID)
			continue
		}
		fmt.Fprintf(stdout, "Resumed node %s in session %s.\n", run.NodeID, sessionID)
	}
}

func writeTaskTransitionResult(stdout io.Writer, action string, task serverapi.WorkflowTaskDetail, transitionID string, runIDs []string) {
	fmt.Fprintf(stdout, "%s %s with transition %s.\n", action, taskDisplayID(task), transitionID)
	for _, runID := range runIDs {
		fmt.Fprintf(stdout, "Started run: %s\n", runID)
	}
}

func taskDisplayID(task serverapi.WorkflowTaskDetail) string {
	return taskSummaryDisplayID(task.Summary)
}

func taskSummaryDisplayID(summary serverapi.WorkflowTaskSummary) string {
	if shortID := strings.TrimSpace(summary.ShortID); shortID != "" {
		return shortID
	}
	return strings.TrimSpace(summary.ID)
}
