package serverapi

import (
	"encoding/json"
	"testing"
)

func TestWorkflowTaskCompleteRequestDistinguishesOmittedAndEmptyHumanProvenance(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		wantErr bool
	}{
		{
			name:    "omitted provenance",
			payload: `{"actor_kind":"user","task_id":"task-1","force":true}`,
		},
		{
			name:    "null provenance",
			payload: `{"actor_kind":"user","task_id":"task-1","run_id":null,"step_id":null,"force":true}`,
		},
		{
			name:    "empty run provenance",
			payload: `{"actor_kind":"user","task_id":"task-1","run_id":"","force":true}`,
			wantErr: true,
		},
		{
			name:    "empty step provenance",
			payload: `{"actor_kind":"user","task_id":"task-1","step_id":"","force":true}`,
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request WorkflowTaskCompleteRequest
			err := json.Unmarshal([]byte(test.payload), &request)
			if err == nil {
				err = request.Validate()
			}
			if test.wantErr && err == nil {
				t.Fatal("explicitly empty completion provenance was accepted")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("omitted completion provenance: %v", err)
			}
		})
	}
}

func TestWorkflowTaskCompleteRequestDecodesTypedAgentProvenance(t *testing.T) {
	var request WorkflowTaskCompleteRequest
	if err := json.Unmarshal([]byte(`{
		"actor_kind":"agent",
		"agent_session_id":"33333333-3333-4333-8333-333333333333",
		"run_id":"11111111-1111-4111-8111-111111111111",
		"step_id":"22222222-2222-4222-8222-222222222222"
	}`), &request); err != nil {
		t.Fatalf("decode agent completion provenance: %v", err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate agent completion provenance: %v", err)
	}
	if request.RunID == nil || request.RunID.String() != "11111111-1111-4111-8111-111111111111" ||
		request.StepID == nil || request.StepID.String() != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("decoded agent completion provenance = run %v, step %v", request.RunID, request.StepID)
	}
}
