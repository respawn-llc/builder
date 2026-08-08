package main

import (
	"fmt"
	"io"
	"strings"

	"core/shared/serverapi"
)

func writeTaskStartResult(stdout io.Writer, task serverapi.WorkflowTaskDetail, _ serverapi.WorkflowTaskStartApplied) {
	if len(task.LiveSessions) == 1 {
		fmt.Fprintf(stdout, "Started task %s in session %s using workflow %q (%s).\n", taskDisplayID(task), task.LiveSessions[0].SessionID, task.Workflow.DisplayName, task.Workflow.WorkflowID)
		return
	}
	if len(task.CurrentScripts) == 1 {
		fmt.Fprintf(stdout, "Started task %s with script %s using workflow %q (%s).\n", taskDisplayID(task), task.CurrentScripts[0].Path, task.Workflow.DisplayName, task.Workflow.WorkflowID)
		return
	}
	fmt.Fprintf(stdout, "Started task %s using workflow %q (%s).\n", taskDisplayID(task), task.Workflow.DisplayName, task.Workflow.WorkflowID)
}

func writeTaskResumeResult(stdout io.Writer, task serverapi.WorkflowTaskDetail, resp serverapi.WorkflowTaskResumeApplied) {
	fmt.Fprintf(stdout, "Resumed task %s.\n", taskDisplayID(task))
	for _, currentNode := range resp.CurrentNodes {
		if currentNode.SessionID == nil || strings.TrimSpace(*currentNode.SessionID) == "" {
			fmt.Fprintf(stdout, "Resumed node %s.\n", currentNode.NodeID)
			continue
		}
		fmt.Fprintf(stdout, "Resumed node %s in session %s.\n", currentNode.NodeID, *currentNode.SessionID)
	}
}

func writeTaskLifecycleResult(stdout io.Writer, action string, task serverapi.WorkflowTaskDetail) {
	fmt.Fprintf(stdout, "%s %s.\n", action, taskDisplayID(task))
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
