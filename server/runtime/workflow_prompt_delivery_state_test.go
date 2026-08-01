package runtime

import (
	"testing"

	"core/server/workflowruntime"
)

func TestWorkflowPromptDeliveryStateUsesStartCauseOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		delivery     workflowruntime.TaskPromptDelivery
		first        workflowTaskPromptTrigger
		continuation workflowTaskPromptTrigger
	}{
		{
			name:         "assignment",
			delivery:     workflowruntime.TaskPromptDeliveryAssignment,
			first:        workflowTaskPromptTriggerAssignmentDelivery,
			continuation: workflowTaskPromptTriggerTaskDelivery,
		},
		{
			name:         "resume",
			delivery:     workflowruntime.TaskPromptDeliveryResume,
			first:        workflowTaskPromptTriggerResumeDelivery,
			continuation: workflowTaskPromptTriggerTaskDelivery,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := newWorkflowPromptDeliveryState(&workflowruntime.CurrentNodeExecutionConfig{
				TaskPromptDelivery: test.delivery,
			})
			var first workflowTaskPromptTrigger
			if err := state.apply(workflowTaskPromptTriggerTaskDelivery, func(trigger workflowTaskPromptTrigger) error {
				first = trigger
				return nil
			}); err != nil {
				t.Fatalf("apply first prompt delivery: %v", err)
			}
			if first != test.first {
				t.Fatalf("first prompt delivery trigger = %v, want %v", first, test.first)
			}
			if continuation := state.trigger(workflowTaskPromptTriggerTaskDelivery); continuation != test.continuation {
				t.Fatalf("continuation trigger = %v, want %v", continuation, test.continuation)
			}
		})
	}
}

func TestWorkflowPromptDeliveryStateUsesStartCauseDuringInitialCompaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		delivery workflowruntime.TaskPromptDelivery
		want     workflowTaskPromptTrigger
	}{
		{
			name:     "assignment",
			delivery: workflowruntime.TaskPromptDeliveryAssignment,
			want:     workflowTaskPromptTriggerAssignmentDelivery,
		},
		{
			name:     "resume",
			delivery: workflowruntime.TaskPromptDeliveryResume,
			want:     workflowTaskPromptTriggerCompaction,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := newWorkflowPromptDeliveryState(&workflowruntime.CurrentNodeExecutionConfig{
				TaskPromptDelivery: test.delivery,
			})
			if trigger := state.trigger(workflowTaskPromptTriggerCompaction); trigger != test.want {
				t.Fatalf("initial compaction trigger = %v, want %v", trigger, test.want)
			}
		})
	}
}
