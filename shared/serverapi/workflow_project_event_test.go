package serverapi

import "testing"

func TestWorkflowProjectEventValidatesTypedResourceActionAndScope(t *testing.T) {
	tests := []struct {
		name  string
		event WorkflowProjectEvent
		valid bool
	}{
		{
			name: "project task event",
			event: WorkflowProjectEvent{
				ProjectID:        eventID("project-1"),
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceTask,
				Action:           WorkflowProjectEventActionStarted,
				PrimaryEntityID:  "task-1",
				RelatedIDs:       []string{"run-1"},
				OccurredAtUnixMs: 1,
			},
			valid: true,
		},
		{
			name: "global workflow event",
			event: WorkflowProjectEvent{
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceWorkflow,
				Action:           WorkflowProjectEventActionUpdated,
				PrimaryEntityID:  "workflow-1",
				OccurredAtUnixMs: 1,
			},
			valid: true,
		},
		{
			name: "project label event",
			event: WorkflowProjectEvent{
				ProjectID:        eventID("project-1"),
				Resource:         WorkflowProjectEventResourceLabel,
				Action:           WorkflowProjectEventActionRenamed,
				PrimaryEntityID:  "0198c486-0f74-4de8-80cb-02e698e99bb0",
				OccurredAtUnixMs: 1,
			},
			valid: true,
		},
		{
			name: "action forbidden for resource",
			event: WorkflowProjectEvent{
				ProjectID:        eventID("project-1"),
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceTask,
				Action:           WorkflowProjectEventActionLinked,
				PrimaryEntityID:  "task-1",
				OccurredAtUnixMs: 1,
			},
		},
		{
			name: "task requires project scope",
			event: WorkflowProjectEvent{
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceTask,
				Action:           WorkflowProjectEventActionUpdated,
				PrimaryEntityID:  "task-1",
				OccurredAtUnixMs: 1,
			},
		},
		{
			name: "label forbids workflow scope",
			event: WorkflowProjectEvent{
				ProjectID:        eventID("project-1"),
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceLabel,
				Action:           WorkflowProjectEventActionCreated,
				PrimaryEntityID:  "0198c486-0f74-4de8-80cb-02e698e99bb0",
				OccurredAtUnixMs: 1,
			},
		},
		{
			name: "primary entity rejects surrounding whitespace",
			event: WorkflowProjectEvent{
				ProjectID:        eventID("project-1"),
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceTask,
				Action:           WorkflowProjectEventActionUpdated,
				PrimaryEntityID:  " task-1 ",
				OccurredAtUnixMs: 1,
			},
		},
		{
			name: "primary entity is required",
			event: WorkflowProjectEvent{
				ProjectID:        eventID("project-1"),
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceTask,
				Action:           WorkflowProjectEventActionUpdated,
				OccurredAtUnixMs: 1,
			},
		},
		{
			name: "related entities cannot repeat primary",
			event: WorkflowProjectEvent{
				ProjectID:        eventID("project-1"),
				WorkflowID:       eventID("workflow-1"),
				Resource:         WorkflowProjectEventResourceTask,
				Action:           WorkflowProjectEventActionStarted,
				PrimaryEntityID:  "task-1",
				RelatedIDs:       []string{"task-1"},
				OccurredAtUnixMs: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.event.Validate()
			if tt.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tt.valid && err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func eventID(value string) *string {
	return &value
}
