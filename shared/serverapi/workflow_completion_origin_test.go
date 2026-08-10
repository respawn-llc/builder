package serverapi

import (
	"testing"

	"github.com/google/uuid"
)

func TestWorkflowTaskCompleteRequestRequiresExactAgentStepOrigin(t *testing.T) {
	validOrigin := &RuntimeStepOrigin{
		RunID:  uuid.NewString(),
		StepID: uuid.NewString(),
	}
	tests := []struct {
		name    string
		request WorkflowTaskCompleteRequest
		wantErr bool
	}{
		{
			name: "agent exact origin",
			request: WorkflowTaskCompleteRequest{
				ActorKind:      WorkflowTaskCompleteActorAgent,
				AgentSessionID: uuid.NewString(),
				TransitionID:   "done",
				Origin:         validOrigin,
			},
		},
		{
			name: "agent missing origin",
			request: WorkflowTaskCompleteRequest{
				ActorKind:      WorkflowTaskCompleteActorAgent,
				AgentSessionID: uuid.NewString(),
				TransitionID:   "done",
			},
			wantErr: true,
		},
		{
			name: "agent partial origin",
			request: WorkflowTaskCompleteRequest{
				ActorKind:      WorkflowTaskCompleteActorAgent,
				AgentSessionID: uuid.NewString(),
				TransitionID:   "done",
				Origin:         &RuntimeStepOrigin{RunID: uuid.NewString()},
			},
			wantErr: true,
		},
		{
			name: "agent invalid origin",
			request: WorkflowTaskCompleteRequest{
				ActorKind:      WorkflowTaskCompleteActorAgent,
				AgentSessionID: uuid.NewString(),
				TransitionID:   "done",
				Origin: &RuntimeStepOrigin{
					RunID:  "not-a-run-id",
					StepID: uuid.NewString(),
				},
			},
			wantErr: true,
		},
		{
			name: "human forced completion rejects live origin",
			request: WorkflowTaskCompleteRequest{
				ActorKind:    WorkflowTaskCompleteActorUser,
				Force:        true,
				TaskID:       "task-id",
				TransitionID: "done",
				Origin:       validOrigin,
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}
