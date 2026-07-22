package serverapi

import (
	"errors"
	"testing"
)

func TestWorkflowAttentionItemValidateEnforcesDiscriminatedVariants(t *testing.T) {
	tests := []struct {
		name string
		item WorkflowAttentionItem
		want bool
	}{
		{name: "question without recovered prompt metadata", item: validWorkflowAttentionQuestion(), want: true},
		{name: "approval", item: validWorkflowAttentionApproval(), want: true},
		{name: "interrupted run", item: validWorkflowAttentionInterruptedRun(), want: true},
		{name: "removed validation blocker", item: WorkflowAttentionItem{Kind: "validation_blocker"}, want: false},
		{name: "unknown kind", item: WorkflowAttentionItem{Kind: "unknown"}, want: false},
		{name: "blank task identity", item: WorkflowAttentionItem{Kind: "question", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), AskID: workflowAttentionString("ask-1")}, want: false},
		{name: "blank workflow identity", item: WorkflowAttentionItem{Kind: "question", TaskID: "task-1", RunID: workflowAttentionString("run-1"), AskID: workflowAttentionString("ask-1")}, want: false},
		{name: "question without run", item: WorkflowAttentionItem{Kind: "question", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), AskID: workflowAttentionString("ask-1")}, want: false},
		{name: "question without ask", item: WorkflowAttentionItem{Kind: "question", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1")}, want: false},
		{name: "question with transition", item: WorkflowAttentionItem{Kind: "question", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), AskID: workflowAttentionString("ask-1"), TaskTransitionID: workflowAttentionString("transition-1")}, want: false},
		{name: "question with approval snapshot", item: WorkflowAttentionItem{Kind: "question", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), AskID: workflowAttentionString("ask-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}}, want: false},
		{name: "question with interruption payload", item: WorkflowAttentionItem{Kind: "question", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), AskID: workflowAttentionString("ask-1"), DetailJSON: workflowAttentionString("{}")}, want: false},
		{name: "approval without transition", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}}, want: false},
		{name: "approval without snapshot", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1")}, want: false},
		{name: "approval with ask", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}, AskID: workflowAttentionString("ask-1")}, want: false},
		{name: "approval with run", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}, RunID: workflowAttentionString("run-1")}, want: false},
		{name: "approval with session", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}, SessionID: workflowAttentionString("session-1")}, want: false},
		{name: "approval with suggestions", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}, Suggestions: []string{}}, want: false},
		{name: "approval with recommendation", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}, RecommendedOptionIndex: workflowAttentionInt(1)}, want: false},
		{name: "approval with question metadata", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}, Question: &WorkflowAttentionQuestionPrompt{}}, want: false},
		{name: "approval with interruption payload", item: WorkflowAttentionItem{Kind: "approval", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), TaskTransitionID: workflowAttentionString("transition-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}, DetailJSON: workflowAttentionString("{}")}, want: false},
		{name: "interrupted run without run", item: WorkflowAttentionItem{Kind: "interrupted_run", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID()}, want: false},
		{name: "interrupted run with ask", item: WorkflowAttentionItem{Kind: "interrupted_run", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), AskID: workflowAttentionString("ask-1")}, want: false},
		{name: "interrupted run with suggestions", item: WorkflowAttentionItem{Kind: "interrupted_run", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), Suggestions: []string{}}, want: false},
		{name: "interrupted run with recommendation", item: WorkflowAttentionItem{Kind: "interrupted_run", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), RecommendedOptionIndex: workflowAttentionInt(1)}, want: false},
		{name: "interrupted run with question metadata", item: WorkflowAttentionItem{Kind: "interrupted_run", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), Question: &WorkflowAttentionQuestionPrompt{}}, want: false},
		{name: "interrupted run with transition", item: WorkflowAttentionItem{Kind: "interrupted_run", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), TaskTransitionID: workflowAttentionString("transition-1")}, want: false},
		{name: "interrupted run with approval snapshot", item: WorkflowAttentionItem{Kind: "interrupted_run", TaskID: "task-1", WorkflowID: workflowAttentionWorkflowID(), RunID: workflowAttentionString("run-1"), ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.item.Validate()
			if (err == nil) != tt.want {
				t.Fatalf("Validate() error = %v, want valid=%t for %+v", err, tt.want, tt.item)
			}
		})
	}
}

func TestWorkflowAttentionResponseValidationPrefixesItemErrorsAndBindsTaskResponses(t *testing.T) {
	global := WorkflowAttentionListResponse{Items: []WorkflowAttentionItem{
		validWorkflowAttentionQuestion(),
		{Kind: "approval", TaskID: "task-2", WorkflowID: workflowAttentionWorkflowID()},
	}}
	requireWorkflowAttentionIndexedError(t, global.Validate(), "items[1].task_transition_id")

	taskResponse := WorkflowTaskAttentionListResponse{Items: []WorkflowAttentionItem{validWorkflowAttentionQuestion()}}
	if err := taskResponse.Validate(); err != nil {
		t.Fatalf("task attention response rejected a valid item: %v", err)
	}
	if err := taskResponse.ValidateForTask("task-2"); err == nil {
		t.Fatal("task attention response accepted an item for another task")
	}
}

func TestWorkflowTaskActivityResponseValidationEnforcesNestedAttentionCoherence(t *testing.T) {
	valid := WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{
		Type:      "run_interrupted",
		TaskID:    "task-1",
		Attention: workflowAttentionInterruptedRunForTask("task-1"),
	}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("coherent interrupted-run activity response rejected: %v", err)
	}
	if err := valid.ValidateForTask("task-1"); err != nil {
		t.Fatalf("coherent interrupted-run attention rejected: %v", err)
	}

	tests := []struct {
		name     string
		response WorkflowTaskActivityListResponse
		taskID   string
	}{
		{
			name:     "outer task mismatch",
			taskID:   "task-1",
			response: WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{Type: "run_interrupted", TaskID: "task-2"}}},
		},
		{
			name:   "nested attention task mismatch",
			taskID: "task-1",
			response: WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{
				Type:      "run_interrupted",
				TaskID:    "task-1",
				Attention: workflowAttentionInterruptedRunForTask("task-2"),
			}}},
		},
		{
			name:   "nested attention on non interrupted activity",
			taskID: "task-1",
			response: WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{
				Type:      "comment",
				TaskID:    "task-1",
				Attention: workflowAttentionInterruptedRunForTask("task-1"),
			}}},
		},
		{
			name:   "non interrupted attention variant",
			taskID: "task-1",
			response: WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{
				Type:   "run_interrupted",
				TaskID: "task-1",
				Attention: &WorkflowAttentionItem{
					Kind:       "question",
					TaskID:     "task-1",
					WorkflowID: workflowAttentionWorkflowID(),
					RunID:      workflowAttentionString("run-1"),
					AskID:      workflowAttentionString("ask-1"),
				},
			}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.response.ValidateForTask(tt.taskID); err == nil {
				t.Fatalf("activity response accepted incoherent nested attention: %+v", tt.response)
			}
		})
	}
}

func validWorkflowAttentionQuestion() WorkflowAttentionItem {
	return WorkflowAttentionItem{
		Kind:       "question",
		TaskID:     "task-1",
		WorkflowID: workflowAttentionWorkflowID(),
		RunID:      workflowAttentionString("run-1"),
		AskID:      workflowAttentionString("ask-1"),
	}
}

func validWorkflowAttentionApproval() WorkflowAttentionItem {
	return WorkflowAttentionItem{
		Kind:             "approval",
		TaskID:           "task-1",
		WorkflowID:       workflowAttentionWorkflowID(),
		TaskTransitionID: workflowAttentionString("transition-1"),
		ApprovalSnapshot: &WorkflowAttentionApprovalSnapshot{},
	}
}

func validWorkflowAttentionInterruptedRun() WorkflowAttentionItem {
	return *workflowAttentionInterruptedRunForTask("task-1")
}

func workflowAttentionInterruptedRunForTask(taskID string) *WorkflowAttentionItem {
	return &WorkflowAttentionItem{
		Kind:       "interrupted_run",
		TaskID:     taskID,
		WorkflowID: workflowAttentionWorkflowID(),
		RunID:      workflowAttentionString("run-1"),
	}
}

func workflowAttentionWorkflowID() *string {
	workflowID := "workflow-1"
	return &workflowID
}

func workflowAttentionString(value string) *string {
	return &value
}

func workflowAttentionInt(value int) *int {
	return &value
}

func requireWorkflowAttentionIndexedError(t *testing.T, err error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatal("response validation accepted an invalid attention item")
	}
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("response validation error = %T %v, want WorkflowRequestValidationError", err, err)
	}
	if validationErr.Field != wantField {
		t.Fatalf("response validation field = %q, want %q", validationErr.Field, wantField)
	}
}
