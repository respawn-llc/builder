package main

import (
	"fmt"
	"io"
	"strings"

	"core/shared/serverapi"
)

func writeTaskStartResult(stdout io.Writer, task serverapi.WorkflowTaskDetail, resp serverapi.WorkflowTaskStartResponse) {
	run, _ := workflowTaskRunByID(task, resp.RunID)
	sessionID := strings.TrimSpace(run.SessionID)
	placement, _ := workflowTaskPlacementByID(task, resp.PlacementID)
	nodeKey := placementDisplayKey(placement, run.NodeID)
	fmt.Fprintf(stdout, "Started task %s in session %s using workflow %q (%s).\n", taskDisplayID(task), sessionID, task.Workflow.DisplayName, task.Workflow.WorkflowID)
	fmt.Fprintf(stdout, "First node: %s\n", nodeKey)
}

func writeTaskResumeResult(stdout io.Writer, task serverapi.WorkflowTaskDetail, resp serverapi.WorkflowTaskResumeResponse) {
	fmt.Fprintf(stdout, "Resumed task %s.\n", taskDisplayID(task))
	for _, run := range resp.Runs {
		placement, _ := workflowTaskPlacementByID(task, run.PlacementID)
		nodeKey := placementDisplayKey(placement, run.NodeID)
		fmt.Fprintf(stdout, "Resumed node %s in session %s.\n", nodeKey, strings.TrimSpace(run.SessionID))
	}
}

func writeTaskTransitionResult(stdout io.Writer, action string, task serverapi.WorkflowTaskDetail, transitionID string, runIDs []string) {
	transition, _ := workflowTaskTransitionByID(task, transitionID)
	fmt.Fprintf(stdout, "%s %s from `%s` to `%s`.\n", action, taskDisplayID(task), transitionStartKey(transition, transitionID), transitionEndKey(transition, transitionID))
	if nodeKey, sessionID, ok := transitionStartedRun(task, transition, runIDs); ok {
		fmt.Fprintf(stdout, "Because of this, started node %s in session %s.\n", nodeKey, sessionID)
	}
}

func workflowTaskTransitionByID(task serverapi.WorkflowTaskDetail, transitionID string) (serverapi.WorkflowTaskTransition, bool) {
	trimmedTransitionID := strings.TrimSpace(transitionID)
	for _, transition := range task.Transitions {
		if strings.TrimSpace(transition.ID) == trimmedTransitionID {
			return transition, true
		}
	}
	return serverapi.WorkflowTaskTransition{}, false
}

func workflowTaskRunByID(task serverapi.WorkflowTaskDetail, runID string) (serverapi.WorkflowRun, bool) {
	trimmedRunID := strings.TrimSpace(runID)
	for _, run := range task.Runs {
		if strings.TrimSpace(run.ID) == trimmedRunID {
			return run, true
		}
	}
	return serverapi.WorkflowRun{}, false
}

func workflowTaskPlacementByID(task serverapi.WorkflowTaskDetail, placementID string) (serverapi.WorkflowPlacement, bool) {
	trimmedPlacementID := strings.TrimSpace(placementID)
	for _, placement := range task.Placements {
		if strings.TrimSpace(placement.ID) == trimmedPlacementID {
			return placement, true
		}
	}
	return serverapi.WorkflowPlacement{}, false
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

func placementDisplayKey(placement serverapi.WorkflowPlacement, fallbackNodeID string) string {
	if nodeKey := strings.TrimSpace(placement.NodeKey); nodeKey != "" {
		return nodeKey
	}
	if nodeID := strings.TrimSpace(placement.NodeID); nodeID != "" {
		return nodeID
	}
	return strings.TrimSpace(fallbackNodeID)
}

func transitionStartKey(transition serverapi.WorkflowTaskTransition, fallback string) string {
	if sourceKey := strings.TrimSpace(transition.SourceNodeKey); sourceKey != "" {
		return sourceKey
	}
	if sourceID := strings.TrimSpace(transition.SourceNodeID); sourceID != "" {
		return sourceID
	}
	if transitionID := strings.TrimSpace(transition.TransitionID); transitionID != "" {
		return transitionID
	}
	return strings.TrimSpace(fallback)
}

func transitionEndKey(transition serverapi.WorkflowTaskTransition, fallback string) string {
	if edge, ok := transitionSelectedEdge(transition); ok {
		if edgeKey := strings.TrimSpace(edge.EdgeKey); edgeKey != "" {
			return edgeKey
		}
		if targetKey := strings.TrimSpace(edge.TargetNodeKey); targetKey != "" {
			return targetKey
		}
		if targetID := strings.TrimSpace(edge.TargetNodeID); targetID != "" {
			return targetID
		}
	}
	if transitionID := strings.TrimSpace(transition.TransitionID); transitionID != "" {
		return transitionID
	}
	return strings.TrimSpace(fallback)
}

func transitionStartedRun(task serverapi.WorkflowTaskDetail, transition serverapi.WorkflowTaskTransition, runIDs []string) (string, string, bool) {
	for _, runID := range runIDs {
		run, ok := workflowTaskRunByID(task, runID)
		if !ok || strings.TrimSpace(run.SessionID) == "" {
			continue
		}
		placement, _ := workflowTaskPlacementByID(task, run.PlacementID)
		nodeKey := placementDisplayKey(placement, run.NodeID)
		if strings.TrimSpace(nodeKey) == "" {
			nodeKey = transitionEndNodeKey(transition)
		}
		return nodeKey, strings.TrimSpace(run.SessionID), true
	}
	return "", "", false
}

func transitionEndNodeKey(transition serverapi.WorkflowTaskTransition) string {
	if edge, ok := transitionSelectedEdge(transition); ok {
		if targetKey := strings.TrimSpace(edge.TargetNodeKey); targetKey != "" {
			return targetKey
		}
		return strings.TrimSpace(edge.TargetNodeID)
	}
	return ""
}

func transitionSelectedEdge(transition serverapi.WorkflowTaskTransition) (serverapi.WorkflowTransitionEdge, bool) {
	for _, edge := range transition.Edges {
		if strings.TrimSpace(edge.State) == "applied" {
			return edge, true
		}
	}
	if len(transition.Edges) > 0 {
		return transition.Edges[0], true
	}
	return serverapi.WorkflowTransitionEdge{}, false
}
