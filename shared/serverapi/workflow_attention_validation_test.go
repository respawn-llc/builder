package serverapi

import (
	"errors"
	"testing"
)

func TestWorkflowAttentionItemValidateEnforcesDiscriminatedVariants(t *testing.T) {
	question := func(mutate func(*WorkflowAttentionItem)) WorkflowAttentionItem {
		item := validWorkflowAttentionQuestion()
		mutate(&item)
		return item
	}
	approval := func(mutate func(*WorkflowAttentionItem)) WorkflowAttentionItem {
		item := validWorkflowAttentionApproval()
		mutate(&item)
		return item
	}
	interruptedRun := func(mutate func(*WorkflowAttentionItem)) WorkflowAttentionItem {
		item := validWorkflowAttentionInterruptedRun()
		mutate(&item)
		return item
	}
	tests := []struct {
		name string
		item WorkflowAttentionItem
		want bool
	}{
		{name: "question without recovered prompt metadata", item: validWorkflowAttentionQuestion(), want: true},
		{name: "approval", item: validWorkflowAttentionApproval(), want: true},
		{name: "interrupted run", item: validWorkflowAttentionInterruptedRun(), want: true},
		{name: "removed validation blocker", item: question(func(item *WorkflowAttentionItem) { item.Kind = "validation_blocker" }), want: false},
		{name: "unknown kind", item: question(func(item *WorkflowAttentionItem) { item.Kind = "unknown" }), want: false},
		{name: "blank project identity", item: question(func(item *WorkflowAttentionItem) { item.ProjectID = "" }), want: false},
		{name: "blank task identity", item: question(func(item *WorkflowAttentionItem) { item.TaskID = "" }), want: false},
		{name: "blank task short identity", item: question(func(item *WorkflowAttentionItem) { item.TaskShortID = "" }), want: false},
		{name: "blank task title", item: question(func(item *WorkflowAttentionItem) { item.TaskTitle = "" }), want: false},
		{name: "blank workflow identity", item: question(func(item *WorkflowAttentionItem) { item.WorkflowID = nil }), want: false},
		{name: "question without run", item: question(func(item *WorkflowAttentionItem) { item.RunID = nil }), want: false},
		{name: "question without ask", item: question(func(item *WorkflowAttentionItem) { item.AskID = nil }), want: false},
		{name: "question with transition", item: question(func(item *WorkflowAttentionItem) { item.TaskTransitionID = workflowAttentionString("transition-1") }), want: false},
		{name: "question with approval snapshot", item: question(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = workflowAttentionApprovalSnapshot() }), want: false},
		{name: "question with interruption payload", item: question(func(item *WorkflowAttentionItem) { item.DetailJSON = workflowAttentionString("{}") }), want: false},
		{name: "approval without transition", item: approval(func(item *WorkflowAttentionItem) { item.TaskTransitionID = nil }), want: false},
		{name: "approval without snapshot", item: approval(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = nil }), want: false},
		{name: "approval with malformed snapshot", item: approval(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = &WorkflowAttentionApprovalSnapshot{} }), want: false},
		{name: "approval with ask", item: approval(func(item *WorkflowAttentionItem) { item.AskID = workflowAttentionString("ask-1") }), want: false},
		{name: "approval with run", item: approval(func(item *WorkflowAttentionItem) { item.RunID = workflowAttentionString("run-1") }), want: false},
		{name: "approval with session", item: approval(func(item *WorkflowAttentionItem) { item.SessionID = workflowAttentionString("session-1") }), want: false},
		{name: "approval with suggestions", item: approval(func(item *WorkflowAttentionItem) { item.Suggestions = []string{} }), want: false},
		{name: "approval with recommendation", item: approval(func(item *WorkflowAttentionItem) { item.RecommendedOptionIndex = workflowAttentionInt(1) }), want: false},
		{name: "approval with question metadata", item: approval(func(item *WorkflowAttentionItem) { item.Question = &WorkflowAttentionQuestionPrompt{} }), want: false},
		{name: "approval with interruption payload", item: approval(func(item *WorkflowAttentionItem) { item.DetailJSON = workflowAttentionString("{}") }), want: false},
		{name: "interrupted run without run", item: interruptedRun(func(item *WorkflowAttentionItem) { item.RunID = nil }), want: false},
		{name: "interrupted run with ask", item: interruptedRun(func(item *WorkflowAttentionItem) { item.AskID = workflowAttentionString("ask-1") }), want: false},
		{name: "interrupted run with suggestions", item: interruptedRun(func(item *WorkflowAttentionItem) { item.Suggestions = []string{} }), want: false},
		{name: "interrupted run with recommendation", item: interruptedRun(func(item *WorkflowAttentionItem) { item.RecommendedOptionIndex = workflowAttentionInt(1) }), want: false},
		{name: "interrupted run with question metadata", item: interruptedRun(func(item *WorkflowAttentionItem) { item.Question = &WorkflowAttentionQuestionPrompt{} }), want: false},
		{name: "interrupted run with transition", item: interruptedRun(func(item *WorkflowAttentionItem) { item.TaskTransitionID = workflowAttentionString("transition-1") }), want: false},
		{name: "interrupted run with approval snapshot", item: interruptedRun(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = workflowAttentionApprovalSnapshot() }), want: false},
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

func TestWorkflowAttentionApprovalSnapshotValidateRejectsMalformedContents(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*WorkflowAttentionApprovalSnapshot)
		wantField string
	}{
		{name: "blank source node", mutate: func(snapshot *WorkflowAttentionApprovalSnapshot) { snapshot.SourceNodeDisplayName = "" }, wantField: "approval_snapshot.source_node_display_name"},
		{name: "nil targets", mutate: func(snapshot *WorkflowAttentionApprovalSnapshot) { snapshot.Targets = nil }, wantField: "approval_snapshot.targets"},
		{name: "blank target name", mutate: func(snapshot *WorkflowAttentionApprovalSnapshot) { snapshot.Targets[0].DisplayName = "" }, wantField: "approval_snapshot.targets[0].display_name"},
		{name: "nil output values", mutate: func(snapshot *WorkflowAttentionApprovalSnapshot) { snapshot.OutputValues = nil }, wantField: "approval_snapshot.output_values"},
		{name: "negative workflow revision", mutate: func(snapshot *WorkflowAttentionApprovalSnapshot) { snapshot.WorkflowRevisionSeen = -1 }, wantField: "approval_snapshot.workflow_revision_seen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := *workflowAttentionApprovalSnapshot()
			snapshot.Targets = append([]WorkflowAttentionApprovalTarget(nil), snapshot.Targets...)
			tt.mutate(&snapshot)
			requireWorkflowAttentionValidationError(t, snapshot.Validate(), tt.wantField)
		})
	}
}

func TestWorkflowAttentionResponseValidationPrefixesItemErrorsAndBindsTaskResponses(t *testing.T) {
	global := WorkflowAttentionListResponse{Items: []WorkflowAttentionItem{
		validWorkflowAttentionQuestion(),
		func() WorkflowAttentionItem {
			item := validWorkflowAttentionApproval()
			item.TaskID = "task-2"
			item.TaskTransitionID = nil
			return item
		}(),
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
				Type:      "run_interrupted",
				TaskID:    "task-1",
				Attention: workflowAttentionQuestionPointer(),
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
		ProjectID:   "project-1",
		TaskID:      "task-1",
		TaskShortID: "KENT-1",
		TaskTitle:   "Task",
		WorkflowID:  workflowAttentionWorkflowID(),
		Kind:        "question",
		RunID:       workflowAttentionString("run-1"),
		AskID:       workflowAttentionString("ask-1"),
	}
}

func validWorkflowAttentionApproval() WorkflowAttentionItem {
	return WorkflowAttentionItem{
		ProjectID:        "project-1",
		Kind:             "approval",
		TaskID:           "task-1",
		TaskShortID:      "KENT-1",
		TaskTitle:        "Task",
		WorkflowID:       workflowAttentionWorkflowID(),
		TaskTransitionID: workflowAttentionString("transition-1"),
		ApprovalSnapshot: workflowAttentionApprovalSnapshot(),
	}
}

func validWorkflowAttentionInterruptedRun() WorkflowAttentionItem {
	return *workflowAttentionInterruptedRunForTask("task-1")
}

func workflowAttentionQuestionPointer() *WorkflowAttentionItem {
	item := validWorkflowAttentionQuestion()
	return &item
}

func workflowAttentionInterruptedRunForTask(taskID string) *WorkflowAttentionItem {
	return &WorkflowAttentionItem{
		ProjectID:   "project-1",
		Kind:        "interrupted_run",
		TaskID:      taskID,
		TaskShortID: "KENT-1",
		TaskTitle:   "Task",
		WorkflowID:  workflowAttentionWorkflowID(),
		RunID:       workflowAttentionString("run-1"),
	}
}

func workflowAttentionApprovalSnapshot() *WorkflowAttentionApprovalSnapshot {
	return &WorkflowAttentionApprovalSnapshot{
		SourceNodeDisplayName: "Review",
		Targets:               []WorkflowAttentionApprovalTarget{{DisplayName: "Done"}},
		OutputValues:          map[string]string{},
		WorkflowRevisionSeen:  0,
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

func requireWorkflowAttentionValidationError(t *testing.T, err error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatal("validation accepted malformed attention payload")
	}
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("validation error = %T %v, want WorkflowRequestValidationError", err, err)
	}
	if validationErr.Field != wantField {
		t.Fatalf("validation field = %q, want %q", validationErr.Field, wantField)
	}
}
