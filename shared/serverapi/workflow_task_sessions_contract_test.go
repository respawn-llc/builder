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

func TestWorkflowTaskSessionRequestRejectsBlankTaskID(t *testing.T) {
	if err := (WorkflowTaskOffsetPageRequest{TaskID: " "}).Validate(); err == nil {
		t.Fatal("Task Session request accepted a blank Task ID")
	}
}
