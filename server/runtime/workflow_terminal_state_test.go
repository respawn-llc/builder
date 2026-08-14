package runtime

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

func TestWorkflowSessionStateRequiresValidTaskAndWorkflowIdentity(t *testing.T) {
	validWorkflowID := runtimeids.NewWorkflowID()
	for _, testCase := range []struct {
		name       string
		taskID     workflow.TaskID
		workflowID runtimeids.WorkflowID
		wantState  bool
	}{
		{name: "missing task", workflowID: validWorkflowID},
		{name: "blank task", taskID: " \t", workflowID: validWorkflowID},
		{name: "missing workflow", taskID: "task-1"},
		{name: "valid identity", taskID: "task-1", workflowID: validWorkflowID, wantState: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			execution := &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID: runtimeids.NewExecutionScopeID(),
				Instructions: workflowruntime.TaskInstructions{
					CurrentNode: workflow.CurrentNodeReference{TaskID: testCase.taskID},
					WorkflowID:  testCase.workflowID,
				},
			}
			engine := &Engine{
				currentNodeExecution: newCurrentNodeExecutionState(),
			}
			engine.currentNodeExecution.config = execution

			state, err := engine.WorkflowSessionState()
			if testCase.wantState && err != nil {
				t.Fatalf("WorkflowSessionState() error = %v", err)
			}
			if !testCase.wantState && (testCase.taskID == "" || testCase.workflowID.IsZero()) && err == nil {
				t.Fatal("WorkflowSessionState() error = nil for invalid active identity")
			}
			if (state != nil) != testCase.wantState {
				t.Fatalf("WorkflowSessionState() = %+v, want state present = %v", state, testCase.wantState)
			}
		})
	}
}
