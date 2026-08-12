package serverapi

import "testing"

func TestWorkflowTaskSessionResponseContract(t *testing.T) {
	sessionName := "Implementation"
	nodeName := "Implement"
	nextOffset := 25
	for _, test := range []struct {
		status WorkflowTaskSessionStatus
		wire   string
	}{
		{status: WorkflowTaskSessionStatusRunning, wire: "running"},
		{status: WorkflowTaskSessionStatusQuestion, wire: "question"},
		{status: WorkflowTaskSessionStatusIdle, wire: "idle"},
	} {
		t.Run(test.wire, func(t *testing.T) {
			response := WorkflowTaskSessionListResponse{
				TaskID: "task-1",
				WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskSessionItem]{
					Items: []WorkflowTaskSessionItem{{
						SessionID:   "session-1",
						SessionName: &sessionName,
						NodeName:    &nodeName,
						AgentRole:   "coder",
						Status:      test.status,
					}},
					NextOffset: &nextOffset,
				},
			}
			if err := response.ValidateForTask("task-1"); err != nil {
				t.Fatalf("valid response rejected: %v", err)
			}
			encoded, shape := marshalWorkflowJSON[map[string]any](t, response)
			items := shape["items"].([]any)
			item := items[0].(map[string]any)
			if len(items) != 1 || shape["task_id"] != "task-1" || shape["next_offset"] != float64(nextOffset) ||
				item["session_id"] != "session-1" || item["session_name"] != sessionName ||
				item["node_name"] != nodeName || item["agent_role"] != "coder" || item["status"] != test.wire {
				t.Fatalf("response JSON = %s", encoded)
			}
		})
	}
}

func TestWorkflowTaskSessionResponseRejectsMalformedData(t *testing.T) {
	if err := (WorkflowTaskOffsetPageRequest{TaskID: " "}).Validate(); err == nil {
		t.Fatal("Task Session request accepted a blank Task ID")
	}
	valid := WorkflowTaskSessionListResponse{
		TaskID: "task-1",
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskSessionItem]{
			Items: []WorkflowTaskSessionItem{{
				SessionID: "session-1",
				AgentRole: "coder",
				Status:    WorkflowTaskSessionStatusIdle,
			}},
		},
	}
	for name, mutate := range map[string]func(*WorkflowTaskSessionListResponse){
		"blank Task ID":  func(response *WorkflowTaskSessionListResponse) { response.TaskID = " " },
		"null items":     func(response *WorkflowTaskSessionListResponse) { response.Items = nil },
		"blank Session":  func(response *WorkflowTaskSessionListResponse) { response.Items[0].SessionID = " " },
		"blank role":     func(response *WorkflowTaskSessionListResponse) { response.Items[0].AgentRole = " " },
		"invalid status": func(response *WorkflowTaskSessionListResponse) { response.Items[0].Status = "approval" },
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
	if err := valid.ValidateForTask("task-other"); err == nil {
		t.Fatal("Task Session response accepted a different top-level Task ID")
	}
}
