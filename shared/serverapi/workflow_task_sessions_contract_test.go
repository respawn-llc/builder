package serverapi

import "testing"

func TestWorkflowTaskSessionResponseContract(t *testing.T) {
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
						SessionID: "session-1",
						AgentRole: "coder",
						Status:    test.status,
					}},
				},
			}
			if err := response.ValidateForTask("task-1"); err != nil {
				t.Fatalf("valid response rejected: %v", err)
			}
			if string(test.status) != test.wire {
				t.Fatalf("status = %q, want wire value %q", test.status, test.wire)
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
