package prompts

import (
	"strings"
	"testing"
)

func TestWorkflowTaskInstructionDataSelectsDependencyAwareness(t *testing.T) {
	taskShortID := "AWARE-1847"
	count := int64(3)
	data := newWorkflowTaskInstructionsTemplateData(WorkflowNodeContextArgs{
		TaskShortId:                    taskShortID,
		TaskUnsatisfiedDependencyCount: count,
	}, "complete")
	if !data.ShowTaskDependenciesReminder {
		t.Fatal("nonzero unsatisfied dependency count omitted the reminder")
	}
	if data.TaskDependenciesLabel != taskDependenciesLabel(count) {
		t.Fatalf("dependency label = %q, want count-derived label", data.TaskDependenciesLabel)
	}
	wantCommand := strings.Join([]string{LaunchCommand(), "task", "show", taskShortID}, " ")
	if data.TaskShowCommand != wantCommand {
		t.Fatalf("Task show command = %q, want %q", data.TaskShowCommand, wantCommand)
	}

	zero := newWorkflowTaskInstructionsTemplateData(WorkflowNodeContextArgs{
		TaskShortId: taskShortID,
	}, "complete")
	if zero.ShowTaskDependenciesReminder {
		t.Fatal("zero unsatisfied dependencies selected the reminder")
	}
}
