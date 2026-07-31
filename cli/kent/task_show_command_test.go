package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"core/internal/testharness/testsetup"

	"core/shared/serverapi"
)

func TestTaskShowJSONIncludesCurrentNodesAndRetainedSessionCount(t *testing.T) {
	sessionID := "session-1"
	task := serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{WorkflowID: testsetup.WorkflowID(t, "task-show")},
		Workflow: serverapi.WorkflowTaskWorkflowSummary{WorkflowID: testsetup.WorkflowID(t, "task-show")},
		CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{
			NodeID:    "node-1",
			SessionID: &sessionID,
		}},
		RetainedSessionCount: 3,
	}

	encoded, err := json.Marshal(taskShowOutputFromDetail(task))
	if err != nil {
		t.Fatalf("marshal task show output: %v", err)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("decode task show output: %v", err)
	}
	if _, exists := output["current_nodes"]; !exists {
		t.Fatalf("task show JSON keys = %v, want current_nodes", output)
	}
	if _, exists := output["retained_session_count"]; !exists {
		t.Fatalf("task show JSON keys = %v, want retained_session_count", output)
	}
	var currentNodes []serverapi.WorkflowTaskCurrentNode
	if err := json.Unmarshal(output["current_nodes"], &currentNodes); err != nil {
		t.Fatalf("decode current_nodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].NodeID != "node-1" {
		t.Fatalf("current_nodes = %+v, want node-1", currentNodes)
	}
	var retainedSessionCount int
	if err := json.Unmarshal(output["retained_session_count"], &retainedSessionCount); err != nil {
		t.Fatalf("decode retained_session_count: %v", err)
	}
	if retainedSessionCount != 3 {
		t.Fatalf("retained_session_count = %d, want 3", retainedSessionCount)
	}
}

func TestTaskShowHumanOutputReportsRetainedSessionsWithoutDuplicatingCurrentNodeIdentity(t *testing.T) {
	sessionID := "session-1"
	task := serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{
			ShortID:         "KNT-1",
			Title:           "Task",
			ProjectID:       "project-1",
			CreatedAtUnixMs: 1,
		},
		Project: serverapi.ProjectBoardProject{DisplayName: "Project"},
		Workflow: serverapi.WorkflowTaskWorkflowSummary{
			WorkflowID:  testsetup.WorkflowID(t, "task-show"),
			DisplayName: "Workflow",
		},
		CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{
			NodeID:    "node-1",
			SessionID: &sessionID,
		}},
		LiveSessionIDs:       []string{sessionID},
		RetainedSessionCount: 3,
		Status: serverapi.WorkflowTaskStatus{
			Kind:        serverapi.WorkflowTaskStatusKindActive,
			NativeState: serverapi.WorkflowTaskNativeStateActive,
		},
	}

	var output bytes.Buffer
	if err := writeTaskDetail(&output, task); err != nil {
		t.Fatalf("write task detail: %v", err)
	}
	if !bytes.Contains(output.Bytes(), []byte("Current session: session-1\n")) {
		t.Fatalf("task show human output = %q, want current Session", output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte("Retained sessions: 3\n")) {
		t.Fatalf("task show human output = %q, want retained Session count", output.String())
	}
	if bytes.Contains(output.Bytes(), []byte("Current node:")) ||
		bytes.Contains(output.Bytes(), []byte("node-1")) {
		t.Fatalf("task show human output = %q, must not duplicate Current Node identity", output.String())
	}
}

func TestTaskShowHumanOutputUsesSharedDependencySections(t *testing.T) {
	unsatisfied := serverapi.WorkflowTaskDependencyUnsatisfied
	task := serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{
			ShortID:         "KNT-2",
			Title:           "Task",
			ProjectID:       "project-1",
			CreatedAtUnixMs: 1,
		},
		Project:  serverapi.ProjectBoardProject{DisplayName: "Project"},
		Workflow: serverapi.WorkflowTaskWorkflowSummary{WorkflowID: testsetup.WorkflowID(t, "task-show-dependencies"), DisplayName: "Workflow"},
		Status:   taskDependencyTestStatus(serverapi.WorkflowTaskStatusKindBacklog),
		Dependencies: serverapi.WorkflowTaskDependencies{
			BlockerCount:            1,
			UnsatisfiedBlockerCount: 1,
			Directions: []serverapi.WorkflowTaskDependencyDirectionProjection{{
				Direction:        serverapi.WorkflowTaskDependencyDirectionBlockedBy,
				TotalCount:       1,
				UnsatisfiedCount: intTestPointer(1),
				Items: []serverapi.WorkflowTaskDependencyItem{{
					TaskID:       "task-1",
					ShortID:      "KNT-1",
					Title:        "Foundation",
					WorkflowID:   "workflow-1",
					Status:       taskDependencyTestStatus(serverapi.WorkflowTaskStatusKindActive),
					Satisfaction: &unsatisfied,
				}},
			}},
		},
	}

	var output bytes.Buffer
	if err := writeTaskDetail(&output, task); err != nil {
		t.Fatalf("write task detail: %v", err)
	}
	if !bytes.HasSuffix(output.Bytes(), []byte("Blocked by:\nKNT-1: Foundation (active)\n")) {
		t.Fatalf("task show output=%q", output.String())
	}
}

func TestTaskShowJSONIncludesAggregateDependenciesOnlyWhenNonzero(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "task-show-aggregate-dependencies")
	task := serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{WorkflowID: workflowID},
		Workflow: serverapi.WorkflowTaskWorkflowSummary{WorkflowID: workflowID},
		Dependencies: serverapi.WorkflowTaskDependencies{
			BlockerCount:             2,
			UnsatisfiedBlockerCount:  1,
			DirectlyBlockedTaskCount: 3,
			Directions: []serverapi.WorkflowTaskDependencyDirectionProjection{{
				Direction: serverapi.WorkflowTaskDependencyDirectionBlocks,
				Items: []serverapi.WorkflowTaskDependencyItem{{
					TaskID: "must-not-leak",
				}},
			}},
		},
	}

	data, err := json.Marshal(taskShowOutputFromDetail(task))
	if err != nil {
		t.Fatalf("marshal task show output: %v", err)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("decode task show output: %v", err)
	}
	var summary map[string]int
	if err := json.Unmarshal(output["dependencies"], &summary); err != nil {
		t.Fatalf("decode dependencies: %v", err)
	}
	if len(summary) != 3 || summary["blocker_count"] != 2 || summary["unsatisfied_blocker_count"] != 1 || summary["blocked_task_count"] != 3 {
		t.Fatalf("dependency summary=%v JSON=%s", summary, data)
	}
	if bytes.Contains(data, []byte("directions")) || bytes.Contains(data, []byte("must-not-leak")) {
		t.Fatalf("task show JSON leaked dependency items: %s", data)
	}

	emptyData, err := json.Marshal(taskShowOutputFromDetail(serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{WorkflowID: workflowID},
		Workflow: serverapi.WorkflowTaskWorkflowSummary{WorkflowID: workflowID},
	}))
	if err != nil {
		t.Fatalf("marshal empty task show output: %v", err)
	}
	if bytes.Contains(emptyData, []byte(`"dependencies"`)) {
		t.Fatalf("empty task show JSON includes dependencies: %s", emptyData)
	}
}
