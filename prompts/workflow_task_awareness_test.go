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

func TestWorkflowTaskInstructionsRenderDependencyAwarenessForEveryDeliveryKind(t *testing.T) {
	taskShortID := "AWARE-2849"
	count := int64(3)
	command := strings.Join([]string{LaunchCommand(), "task", "show", taskShortID}, " ")
	for _, kind := range []WorkflowTaskPromptKind{
		WorkflowTaskPromptInitialAssignment,
		WorkflowTaskPromptReassignment,
		WorkflowTaskPromptCompactionReminder,
	} {
		withDependencies, err := RenderWorkflowTaskInstructions(kind, WorkflowNodeContextArgs{
			TaskShortId:                    taskShortID,
			TaskUnsatisfiedDependencyCount: count,
		}, "complete")
		if err != nil {
			t.Fatalf("render prompt kind %d with dependencies: %v", kind, err)
		}
		if !strings.Contains(withDependencies, taskDependenciesLabel(count)) {
			t.Fatalf("prompt kind %d omitted the unsatisfied dependency count", kind)
		}
		if got := strings.Count(withDependencies, "`"+command+"`"); got != 2 {
			t.Fatalf("prompt kind %d Task show command occurrences = %d, want base guidance plus dependency reminder", kind, got)
		}

		withoutDependencies, err := RenderWorkflowTaskInstructions(kind, WorkflowNodeContextArgs{
			TaskShortId: taskShortID,
		}, "complete")
		if err != nil {
			t.Fatalf("render prompt kind %d without dependencies: %v", kind, err)
		}
		if strings.Contains(withoutDependencies, taskDependenciesLabel(count)) {
			t.Fatalf("prompt kind %d rendered a dependency count for zero unsatisfied dependencies", kind)
		}
		if got := strings.Count(withoutDependencies, "`"+command+"`"); got != 1 {
			t.Fatalf("prompt kind %d Task show command occurrences = %d, want only base guidance", kind, got)
		}
	}
}
