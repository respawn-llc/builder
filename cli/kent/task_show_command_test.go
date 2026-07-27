package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"core/shared/serverapi"
)

func TestTaskShowJSONIncludesCurrentNodesAndRetainedSessionCount(t *testing.T) {
	sessionID := "session-1"
	task := serverapi.WorkflowTaskDetail{
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
			WorkflowID:  "workflow-1",
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
