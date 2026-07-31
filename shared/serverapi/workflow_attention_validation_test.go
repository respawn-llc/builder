package serverapi

import (
	"errors"
	"testing"

	"core/shared/textutil"
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
	interrupted := func(mutate func(*WorkflowAttentionItem)) WorkflowAttentionItem {
		item := validWorkflowAttentionInterrupted()
		mutate(&item)
		return item
	}
	tests := []struct {
		name string
		item WorkflowAttentionItem
		want bool
	}{
		{name: "question", item: validWorkflowAttentionQuestion(), want: true},
		{name: "approval", item: validWorkflowAttentionApproval(), want: true},
		{name: "interrupted current node", item: validWorkflowAttentionInterrupted(), want: true},
		{name: "unknown kind", item: question(func(item *WorkflowAttentionItem) { item.Kind = "unknown" }), want: false},
		{name: "blank project identity", item: question(func(item *WorkflowAttentionItem) { item.ProjectID = "" }), want: false},
		{name: "blank task identity", item: question(func(item *WorkflowAttentionItem) { item.TaskID = "" }), want: false},
		{name: "blank task short identity", item: question(func(item *WorkflowAttentionItem) { item.TaskShortID = "" }), want: false},
		{name: "blank task title", item: question(func(item *WorkflowAttentionItem) { item.TaskTitle = "" }), want: false},
		{name: "blank workflow identity", item: question(func(item *WorkflowAttentionItem) { item.WorkflowID = "" }), want: false},
		{name: "question without message", item: question(func(item *WorkflowAttentionItem) { item.Message = nil }), want: false},
		{name: "question with blank message", item: question(func(item *WorkflowAttentionItem) { item.Message = textutil.Value("") }), want: false},
		{name: "question without current node", item: question(func(item *WorkflowAttentionItem) { item.CurrentNode = nil }), want: false},
		{name: "question without question", item: question(func(item *WorkflowAttentionItem) { item.QuestionID = nil }), want: false},
		{name: "question with approval snapshot", item: question(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = workflowAttentionApprovalSnapshot() }), want: false},
		{name: "question with approval identity", item: question(func(item *WorkflowAttentionItem) { item.ApprovalID = textutil.Value("approval-1") }), want: false},
		{name: "question with detail", item: question(func(item *WorkflowAttentionItem) { item.DetailJSON = textutil.Value("{}") }), want: false},
		{name: "approval without identity", item: approval(func(item *WorkflowAttentionItem) { item.ApprovalID = nil }), want: false},
		{name: "approval without message", item: approval(func(item *WorkflowAttentionItem) { item.Message = nil }), want: true},
		{name: "approval with blank message", item: approval(func(item *WorkflowAttentionItem) { item.Message = textutil.Value("") }), want: false},
		{name: "approval without snapshot", item: approval(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = nil }), want: false},
		{name: "approval with malformed snapshot", item: approval(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = &WorkflowAttentionApprovalSnapshot{} }), want: false},
		{name: "approval with question", item: approval(func(item *WorkflowAttentionItem) { item.QuestionID = textutil.Value("question-1") }), want: false},
		{name: "approval with session", item: approval(func(item *WorkflowAttentionItem) { item.SessionID = textutil.Value("session-1") }), want: false},
		{name: "approval with suggestions", item: approval(func(item *WorkflowAttentionItem) { item.Suggestions = []string{} }), want: false},
		{name: "approval with recommendation", item: approval(func(item *WorkflowAttentionItem) { item.RecommendedOptionIndex = textutil.Value(1) }), want: false},
		{name: "approval with question metadata", item: approval(func(item *WorkflowAttentionItem) { item.Question = &WorkflowAttentionQuestionPrompt{} }), want: false},
		{name: "approval with detail", item: approval(func(item *WorkflowAttentionItem) { item.DetailJSON = textutil.Value("{}") }), want: false},
		{name: "interrupted without current node", item: interrupted(func(item *WorkflowAttentionItem) { item.CurrentNode = nil }), want: false},
		{name: "interrupted without message", item: interrupted(func(item *WorkflowAttentionItem) { item.Message = nil }), want: true},
		{name: "interrupted with blank message", item: interrupted(func(item *WorkflowAttentionItem) { item.Message = textutil.Value("") }), want: false},
		{name: "interrupted without detail", item: interrupted(func(item *WorkflowAttentionItem) { item.DetailJSON = nil }), want: true},
		{name: "interrupted with session", item: interrupted(func(item *WorkflowAttentionItem) {
			sessionID := "session-1"
			item.CurrentNode.SessionID = &sessionID
			item.SessionID = &sessionID
		}), want: true},
		{name: "interrupted with blank session", item: interrupted(func(item *WorkflowAttentionItem) { item.SessionID = textutil.Value("") }), want: false},
		{name: "interrupted with blank detail", item: interrupted(func(item *WorkflowAttentionItem) { item.DetailJSON = textutil.Value("") }), want: false},
		{name: "interrupted with question", item: interrupted(func(item *WorkflowAttentionItem) { item.QuestionID = textutil.Value("question-1") }), want: false},
		{name: "interrupted with approval snapshot", item: interrupted(func(item *WorkflowAttentionItem) { item.ApprovalSnapshot = workflowAttentionApprovalSnapshot() }), want: false},
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

func TestWorkflowAttentionItemValidateRequiresStrictInterruptedDetailSchema(t *testing.T) {
	for name, detail := range map[string]string{
		"malformed":        "{",
		"array":            "[]",
		"null":             "null",
		"missing code":     `{"fields":{}}`,
		"blank code":       `{"code":"","fields":{}}`,
		"unknown field":    `{"code":"restart","fields":{},"message":"restarted"}`,
		"non-string field": `{"code":"restart","fields":{"attempt":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			item := validWorkflowAttentionInterrupted()
			item.DetailJSON = textutil.Value(detail)
			requireWorkflowAttentionValidationError(t, item.Validate(), "detail_json")
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
			item.ApprovalID = nil
			return item
		}(),
	}}
	requireWorkflowAttentionIndexedError(t, global.Validate(), "items[1].approval_id")

	taskResponse := WorkflowTaskAttentionListResponse{Items: []WorkflowAttentionItem{validWorkflowAttentionQuestion()}}
	if err := taskResponse.Validate(); err != nil {
		t.Fatalf("task attention response rejected a valid item: %v", err)
	}
	if err := taskResponse.ValidateForTask("task-2"); err == nil {
		t.Fatal("task attention response accepted an item for another task")
	}
}

func TestWorkflowTaskActivityResponseValidationOnlyAcceptsDurableActivity(t *testing.T) {
	valid := WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{
		Type:    "comment",
		TaskID:  "task-1",
		Comment: &WorkflowTaskComment{ID: "comment-1"},
	}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("comment activity response rejected: %v", err)
	}
	if err := valid.ValidateForTask("task-1"); err != nil {
		t.Fatalf("comment activity task binding rejected: %v", err)
	}

	tests := []struct {
		name     string
		response WorkflowTaskActivityListResponse
		taskID   string
	}{
		{
			name:     "outer task mismatch",
			taskID:   "task-1",
			response: WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{Type: "comment", TaskID: "task-2", Comment: &WorkflowTaskComment{ID: "comment-1"}}}},
		},
		{
			name:   "comment with session payload",
			taskID: "task-1",
			response: WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{
				Type:           "comment",
				TaskID:         "task-1",
				Comment:        &WorkflowTaskComment{ID: "comment-1"},
				SessionStarted: &WorkflowTaskSessionStarted{SessionID: "session-1", Name: "Session"},
			}}},
		},
		{
			name:   "session without payload",
			taskID: "task-1",
			response: WorkflowTaskActivityListResponse{Items: []WorkflowTaskActivityItem{{
				Type:   "session_started",
				TaskID: "task-1",
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
	sessionID := "session-1"
	questionID := "question-1"
	recommended := 1
	return WorkflowAttentionItem{
		ID:                     "question:task-1:node-1:session-1:question-1",
		ProjectID:              "project-1",
		TaskID:                 "task-1",
		TaskShortID:            "KENT-1",
		TaskTitle:              "Task",
		WorkflowID:             "workflow-1",
		Kind:                   "question",
		Message:                textutil.Value("Continue?"),
		CurrentNode:            &WorkflowTaskCurrentNode{NodeID: "node-1", SessionID: &sessionID},
		SessionID:              &sessionID,
		QuestionID:             &questionID,
		Suggestions:            []string{"Continue"},
		RecommendedOptionIndex: &recommended,
		Question: &WorkflowAttentionQuestionPrompt{
			Kind:                   WorkflowAttentionQuestionKindOrdinary,
			Suggestions:            []string{"Continue"},
			RecommendedOptionIndex: &recommended,
		},
	}
}

func validWorkflowAttentionApproval() WorkflowAttentionItem {
	return WorkflowAttentionItem{
		ID:               "approval:approval-1",
		ProjectID:        "project-1",
		Kind:             "approval",
		TaskID:           "task-1",
		TaskShortID:      "KENT-1",
		TaskTitle:        "Task",
		WorkflowID:       "workflow-1",
		ApprovalID:       textutil.Value("approval-1"),
		ApprovalSnapshot: workflowAttentionApprovalSnapshot(),
	}
}

func validWorkflowAttentionInterrupted() WorkflowAttentionItem {
	return WorkflowAttentionItem{
		ID:          "interrupted:task-1:node-1",
		ProjectID:   "project-1",
		Kind:        "interrupted_current_node",
		TaskID:      "task-1",
		TaskShortID: "KENT-1",
		TaskTitle:   "Task",
		WorkflowID:  "workflow-1",
		DetailJSON:  textutil.Value(`{"code":"restart","fields":{}}`),
		CurrentNode: &WorkflowTaskCurrentNode{NodeID: "node-1"},
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
