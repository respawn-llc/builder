package serverapi

import (
	"encoding/json"
	"testing"
)

func TestWorkflowTaskSessionStatusJSONContract(t *testing.T) {
	for _, status := range []WorkflowTaskSessionStatus{
		WorkflowTaskSessionStatusRunning,
		WorkflowTaskSessionStatusQuestion,
		WorkflowTaskSessionStatusIdle,
	} {
		encoded, err := json.Marshal(status)
		if err != nil {
			t.Fatalf("marshal %q: %v", status, err)
		}
		if string(encoded) != `"`+string(status)+`"` {
			t.Fatalf("marshal %q = %s", status, encoded)
		}
	}
}

func TestWorkflowTaskSessionItemJSONContract(t *testing.T) {
	sessionName := "Implementation"
	nodeName := "Implement"
	item := WorkflowTaskSessionItem{
		SessionID:   "session-1",
		SessionName: &sessionName,
		NodeName:    &nodeName,
		AgentRole:   "coder",
		Status:      WorkflowTaskSessionStatusRunning,
	}

	encoded, shape := marshalWorkflowJSON[map[string]any](t, item)
	if shape["session_id"] != "session-1" ||
		shape["session_name"] != sessionName ||
		shape["node_name"] != nodeName ||
		shape["agent_role"] != "coder" ||
		shape["status"] != "running" {
		t.Fatalf("item JSON = %s", encoded)
	}
}

func TestWorkflowTaskSessionListResponseJSONContract(t *testing.T) {
	nextOffset := 25
	response := WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskSessionItem]{
			Items: []WorkflowTaskSessionItem{{
				SessionID: "session-1",
				AgentRole: "default",
				Status:    WorkflowTaskSessionStatusIdle,
			}},
			NextOffset: &nextOffset,
		},
	}

	encoded, shape := marshalWorkflowJSON[map[string]any](t, response)
	if shape["task_id"] != "task-1" ||
		len(shape["items"].([]any)) != 1 ||
		shape["next_offset"] != float64(nextOffset) {
		t.Fatalf("response JSON = %s", encoded)
	}
}

func TestWorkflowTaskSessionListResponseValidatesCompleteResponse(t *testing.T) {
	sessionName := "Implementation"
	nodeName := "Implement"
	response := WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskSessionItem]{
			Items: []WorkflowTaskSessionItem{{
				SessionID:   "session-1",
				SessionName: &sessionName,
				NodeName:    &nodeName,
				AgentRole:   "coder",
				Status:      WorkflowTaskSessionStatusQuestion,
			}},
		},
	}

	if err := response.Validate(); err != nil {
		t.Fatalf("valid Task Session response rejected: %v", err)
	}
}

func TestWorkflowTaskSessionListResponseRejectsInvalidStatus(t *testing.T) {
	response := WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskSessionItem]{
			Items: []WorkflowTaskSessionItem{{
				SessionID: "session-1",
				AgentRole: "coder",
				Status:    WorkflowTaskSessionStatus("approval"),
			}},
		},
	}

	if err := response.Validate(); err == nil {
		t.Fatal("Task Session response accepted an invalid status")
	}
}

func TestWorkflowTaskSessionListResponseRejectsMalformedFields(t *testing.T) {
	sessionName := "Implementation"
	nodeName := "Implement"
	valid := WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskSessionItem]{
			Items: []WorkflowTaskSessionItem{{
				SessionID:   "session-1",
				SessionName: &sessionName,
				NodeName:    &nodeName,
				AgentRole:   "coder",
				Status:      WorkflowTaskSessionStatusIdle,
			}},
		},
	}

	for name, mutate := range map[string]func(*WorkflowTaskSessionListResponse){
		"blank Task ID": func(response *WorkflowTaskSessionListResponse) {
			response.TaskID = " "
		},
		"null items": func(response *WorkflowTaskSessionListResponse) {
			response.Items = nil
		},
		"blank Session ID": func(response *WorkflowTaskSessionListResponse) {
			response.Items[0].SessionID = " "
		},
		"blank Session name": func(response *WorkflowTaskSessionListResponse) {
			blank := " "
			response.Items[0].SessionName = &blank
		},
		"blank Node name": func(response *WorkflowTaskSessionListResponse) {
			blank := " "
			response.Items[0].NodeName = &blank
		},
		"blank Agent role": func(response *WorkflowTaskSessionListResponse) {
			response.Items[0].AgentRole = " "
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := valid
			response.Items = append([]WorkflowTaskSessionItem(nil), valid.Items...)
			mutate(&response)
			if err := response.Validate(); err == nil {
				t.Fatalf("malformed response accepted: %+v", response)
			}
		})
	}
}

func TestWorkflowTaskSessionListResponseValidatesRequestedTask(t *testing.T) {
	response := WorkflowTaskSessionListResponse{
		TaskID: "task-other",
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskSessionItem]{
			Items: []WorkflowTaskSessionItem{},
		},
	}
	if err := response.ValidateForTask("task-requested"); err == nil {
		t.Fatal("Task Session response accepted a different top-level Task ID")
	}
}
