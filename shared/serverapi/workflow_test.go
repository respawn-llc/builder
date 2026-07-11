package serverapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/shared/clientui"
)

func TestWorkflowCreateUpdateRequestValidation(t *testing.T) {
	if err := (WorkflowCreateRequest{Name: "Pipeline"}).Validate(); err != nil {
		t.Fatalf("valid create request rejected: %v", err)
	}
	if err := (WorkflowCreateRequest{Name: " "}).Validate(); !isWorkflowFieldError(err, "name", WorkflowRequestErrorRequired) {
		t.Fatalf("empty name error = %#v, want required on name", err)
	}
	if err := (WorkflowUpdateRequest{WorkflowID: "workflow-1", Name: strings.Repeat("x", 121)}).Validate(); !isWorkflowFieldError(err, "name", WorkflowRequestErrorTooLong) {
		t.Fatalf("long name error = %#v, want too_long on name", err)
	}
}

func TestWorkflowNodeAndEdgeRequestValidation(t *testing.T) {
	validNode := WorkflowNodeAddRequest{WorkflowID: "workflow-1", Key: "implement", Kind: "agent", DisplayName: "Implement", InputFields: []WorkflowInputField{{Name: "summary", Description: "Summary"}}}
	if err := validNode.Validate(); err != nil {
		t.Fatalf("valid node request rejected: %v", err)
	}
	invalidNode := validNode
	invalidNode.Key = "Bad-Key"
	if err := invalidNode.Validate(); !isWorkflowFieldError(err, "key", WorkflowRequestErrorInvalidKey) {
		t.Fatalf("invalid node key error = %#v, want invalid_key on key", err)
	}
	invalidNode = validNode
	invalidNode.DisplayName = ""
	if err := invalidNode.Validate(); !isWorkflowFieldError(err, "display_name", WorkflowRequestErrorRequired) {
		t.Fatalf("invalid display name error = %#v, want required on display_name", err)
	}
	invalidNode = validNode
	invalidNode.CompletionMode = "tool"
	invalidNode.Kind = "terminal"
	if err := invalidNode.Validate(); !isWorkflowFieldError(err, "completion_mode", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("terminal completion mode error = %#v, want invalid_value on completion_mode", err)
	}
	invalidNode = validNode
	invalidNode.CompletionMode = "invalid"
	if err := invalidNode.Validate(); !isWorkflowFieldError(err, "completion_mode", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid completion mode error = %#v, want invalid_value on completion_mode", err)
	}

	validEdge := WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "new_session", PromptTemplate: "Do the next step.", Parameters: []WorkflowParameter{{Key: "summary", Description: "Summary"}}}
	if err := validEdge.Validate(); err != nil {
		t.Fatalf("valid edge request rejected: %v", err)
	}
	oversizedEdge := validEdge
	oversizedEdge.Parameters = make([]WorkflowParameter, WorkflowGraphDraftMaxFieldsPerEntity+1)
	if err := oversizedEdge.Validate(); !isWorkflowFieldError(err, "parameters", WorkflowRequestErrorTooLong) {
		t.Fatalf("oversized edge parameters error = %#v, want too_long on parameters", err)
	}
	selectedSourceEdge := validEdge
	selectedSourceEdge.ContextMode = "continue_session"
	selectedSourceEdge.ContextSource = WorkflowContextSource{Kind: "selected_node", NodeKey: "implement"}
	if err := selectedSourceEdge.Validate(); err != nil {
		t.Fatalf("valid selected context source rejected: %v", err)
	}
	previousTargetEdge := validEdge
	previousTargetEdge.ContextMode = "continue_session"
	previousTargetEdge.ContextSource = WorkflowContextSource{Kind: "previous_target"}
	if err := previousTargetEdge.Validate(); err != nil {
		t.Fatalf("valid previous-target context source rejected: %v", err)
	}
	previousTargetOrNewEdge := validEdge
	previousTargetOrNewEdge.ContextMode = "continue_session"
	previousTargetOrNewEdge.ContextSource = WorkflowContextSource{Kind: "previous_target_or_new"}
	if err := previousTargetOrNewEdge.Validate(); err != nil {
		t.Fatalf("valid previous-target-or-new context source rejected: %v", err)
	}
	invalidPreviousTargetEdge := previousTargetEdge
	invalidPreviousTargetEdge.ContextSource = WorkflowContextSource{Kind: "previous_target", NodeKey: "implement"}
	if err := invalidPreviousTargetEdge.Validate(); !isWorkflowFieldError(err, "context_source.node_key", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid previous-target context source error = %#v, want invalid_value on context_source.node_key", err)
	}
	invalidPreviousTargetOrNewEdge := previousTargetOrNewEdge
	invalidPreviousTargetOrNewEdge.ContextSource = WorkflowContextSource{Kind: "previous_target_or_new", NodeKey: "implement"}
	if err := invalidPreviousTargetOrNewEdge.Validate(); !isWorkflowFieldError(err, "context_source.node_key", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid previous-target-or-new context source error = %#v, want invalid_value on context_source.node_key", err)
	}
	invalidSourceEdge := selectedSourceEdge
	invalidSourceEdge.ContextSource = WorkflowContextSource{Kind: "selected_node", NodeKey: "Bad-Key"}
	if err := invalidSourceEdge.Validate(); !isWorkflowFieldError(err, "context_source.node_key", WorkflowRequestErrorInvalidKey) {
		t.Fatalf("invalid selected context source error = %#v, want invalid_key on context_source.node_key", err)
	}
	invalidSourceEdge = selectedSourceEdge
	invalidSourceEdge.ContextSource = WorkflowContextSource{Kind: "other", NodeKey: "implement"}
	if err := invalidSourceEdge.Validate(); !isWorkflowFieldError(err, "context_source.kind", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid context source kind error = %#v, want invalid_value on context_source.kind", err)
	}
}

func TestWorkflowTransitionGroupDescriptionRequestValidation(t *testing.T) {
	validAdd := WorkflowTransitionGroupAddRequest{
		WorkflowID:   "workflow-1",
		SourceNodeID: "node-1",
		TransitionID: "review",
		DisplayName:  "Review",
		Description:  "Use this when implementation needs review.",
	}
	if err := validAdd.Validate(); err != nil {
		t.Fatalf("valid transition group add rejected: %v", err)
	}
	emptyDescriptionAdd := validAdd
	emptyDescriptionAdd.Description = ""
	if err := emptyDescriptionAdd.Validate(); err != nil {
		t.Fatalf("empty transition group add description rejected: %v", err)
	}
	oversizedAdd := validAdd
	oversizedAdd.Description = strings.Repeat("x", 1001)
	if err := oversizedAdd.Validate(); !isWorkflowFieldError(err, "description", WorkflowRequestErrorTooLong) {
		t.Fatalf("oversized transition group add description error = %#v, want too_long on description", err)
	}

	validUpdate := WorkflowTransitionGroupUpdateRequest{
		WorkflowID:   "workflow-1",
		GroupID:      "group-1",
		SourceNodeID: "node-1",
		TransitionID: "review",
		DisplayName:  "Review",
		Description:  "Use this when implementation needs review.",
	}
	if err := validUpdate.Validate(); err != nil {
		t.Fatalf("valid transition group update rejected: %v", err)
	}
	emptyDescriptionUpdate := validUpdate
	emptyDescriptionUpdate.Description = ""
	if err := emptyDescriptionUpdate.Validate(); err != nil {
		t.Fatalf("empty transition group update description rejected: %v", err)
	}
	oversizedUpdate := validUpdate
	oversizedUpdate.Description = strings.Repeat("x", 1001)
	if err := oversizedUpdate.Validate(); !isWorkflowFieldError(err, "description", WorkflowRequestErrorTooLong) {
		t.Fatalf("oversized transition group update description error = %#v, want too_long on description", err)
	}
}

func TestWorkflowTaskAndCommentRequestValidation(t *testing.T) {
	setupOperationID := NewWorktreeSetupOperationID()
	if err := (WorkflowTaskCreateRequest{ProjectID: "project-1", Title: "Task"}).Validate(); err != nil {
		t.Fatalf("valid task create rejected: %v", err)
	}
	if err := (WorkflowTaskCreateRequest{ProjectID: "project-1", Title: "", Body: "Body"}).Validate(); !isWorkflowFieldError(err, "title", WorkflowRequestErrorRequired) {
		t.Fatalf("empty title error = %#v, want required on title", err)
	}
	updateTitle := "Task"
	if err := (WorkflowTaskUpdateRequest{TaskID: "task-1", Title: &updateTitle}).Validate(); err != nil {
		t.Fatalf("valid task update rejected: %v", err)
	}
	if err := (WorkflowTaskUpdateRequest{TaskID: "task-1"}).Validate(); err != nil {
		t.Fatalf("title-omitted task update rejected: %v", err)
	}
	blankTitle := " "
	if err := (WorkflowTaskUpdateRequest{TaskID: "task-1", Title: &blankTitle}).Validate(); !isWorkflowFieldError(err, "title", WorkflowRequestErrorRequired) {
		t.Fatalf("empty update title error = %#v, want required on title", err)
	}
	if err := (WorkflowTaskStartRequest{TaskID: "task-1", SetupOperationID: setupOperationID}).Validate(); err != nil {
		t.Fatalf("valid task start rejected: %v", err)
	}
	if err := (WorkflowTaskGetRequest{ProjectID: "project-1", ShortID: "BLD-1"}).Validate(); err != nil {
		t.Fatalf("valid task get by short id rejected: %v", err)
	}
	if err := (WorkflowTaskGetRequest{ShortID: "BLD-1"}).Validate(); err != nil {
		t.Fatalf("valid task get by globally unique short id rejected: %v", err)
	}
	if err := (WorkflowTaskGetRequest{ProjectID: "project-1", ShortID: " "}).Validate(); !isWorkflowFieldError(err, "short_id", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("empty get short id error = %#v, want invalid_mode on short_id", err)
	}
	if err := (WorkflowTaskGetRequest{TaskID: " ", ShortID: "BLD-1"}).Validate(); !isWorkflowFieldError(err, "task_id", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("whitespace task id error = %#v, want invalid_mode on task_id", err)
	}
	if err := (WorkflowTaskGetRequest{ProjectID: " ", ShortID: "BLD-1"}).Validate(); !isWorkflowFieldError(err, "project_id", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("whitespace project id error = %#v, want invalid_mode on project_id", err)
	}
	if err := (WorkflowTaskResumeRequest{TaskID: "task-1"}).Validate(); err != nil {
		t.Fatalf("valid task resume rejected: %v", err)
	}
	if err := (WorkflowTaskInterruptRequest{TaskID: "task-1"}).Validate(); err != nil {
		t.Fatalf("valid task interrupt rejected: %v", err)
	}
	if err := (WorkflowTaskApproveRequest{TaskTransitionID: "transition-1", SetupOperationID: setupOperationID}).Validate(); err != nil {
		t.Fatalf("valid task approval rejected: %v", err)
	}
	if err := (WorkflowTaskApproveRequest{}).Validate(); !isWorkflowFieldError(err, "transition_id", WorkflowRequestErrorRequired) {
		t.Fatalf("empty legacy task approval error = %#v, want required on transition_id", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorAgent, AgentSessionID: "session-1"}).Validate(); err != nil {
		t.Fatalf("valid agent task complete rejected: %v", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorAgent, AgentSessionID: "session-1", RunID: "run-1"}).Validate(); err != nil {
		t.Fatalf("valid agent task complete by run rejected: %v", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorAgent, AgentSessionID: "session-1", Force: true}).Validate(); !isWorkflowFieldError(err, "force", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("agent force task complete error = %#v, want invalid_mode on force", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, RunID: "run-1"}).Validate(); !isWorkflowFieldError(err, "force", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("user task complete without force error = %#v, want invalid_mode on force", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true}).Validate(); !isWorkflowFieldError(err, "selector", WorkflowRequestErrorRequired) {
		t.Fatalf("user task complete without selector error = %#v, want required on selector", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true, RunID: "run-1", SessionID: "session-1"}).Validate(); !isWorkflowFieldError(err, "selector", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("multi-selector task complete error = %#v, want invalid_mode on selector", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true, ProjectID: "project-1"}).Validate(); !isWorkflowFieldError(err, "selector", WorkflowRequestErrorRequired) {
		t.Fatalf("project-only task complete error = %#v, want required on selector", err)
	}
	if err := (WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true, RunID: "run-1", ProjectID: "project-1"}).Validate(); err != nil {
		t.Fatalf("task complete with run selector and extra project id rejected: %v", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", FreeformAnswer: "answer"}).Validate(); err != nil {
		t.Fatalf("valid task question answer rejected: %v", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", SelectedOptionNumber: 1, FreeformAnswer: "because"}).Validate(); err != nil {
		t.Fatalf("valid selected option plus freeform rejected: %v", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", SelectedOptionNumber: -1}).Validate(); !isWorkflowFieldError(err, "selected_option_number", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("negative selected option error = %#v, want invalid_mode on selected_option_number", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", ErrorMessage: "err", FreeformAnswer: "answer"}).Validate(); !isWorkflowFieldError(err, "error_message", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("conflicting task question answer error = %#v, want invalid_mode on error_message", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Answer: "one", FreeformAnswer: "two"}).Validate(); !isWorkflowFieldError(err, "answer", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("multi-mode task question answer error = %#v, want invalid_mode on answer", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce, Commentary: "trusted"}}).Validate(); err != nil {
		t.Fatalf("valid task approval question answer rejected: %v", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce}, FreeformAnswer: "also"}).Validate(); !isWorkflowFieldError(err, "approval", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("approval plus ordinary answer error = %#v, want invalid_mode on approval", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce}, ErrorMessage: "err"}).Validate(); !isWorkflowFieldError(err, "error_message", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("approval plus error error = %#v, want invalid_mode on error_message", err)
	}
	if err := (WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecision("future")}}).Validate(); !isWorkflowFieldError(err, "approval.decision", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid approval decision error = %#v, want invalid_value on approval.decision", err)
	}
	if err := (WorkflowTaskCommentAddRequest{TaskID: "task-1", Body: "comment", Author: "user"}).Validate(); err != nil {
		t.Fatalf("valid comment add rejected: %v", err)
	}
	if err := (WorkflowTaskCommentAddRequest{TaskID: "task-1", Body: "comment", Author: "agent"}).Validate(); err != nil {
		t.Fatalf("valid agent comment add rejected: %v", err)
	}
	if err := (WorkflowTaskCommentAddRequest{TaskID: "task-1", Body: "comment", Author: "system"}).Validate(); !isWorkflowFieldError(err, "author", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("system comment author error = %#v, want invalid_value on author", err)
	}
	if err := (WorkflowTaskCommentAddRequest{TaskID: "task-1", Body: "", Author: "user"}).Validate(); !isWorkflowFieldError(err, "body", WorkflowRequestErrorRequired) {
		t.Fatalf("empty comment body error = %#v, want required on body", err)
	}
	if err := (WorkflowTaskActivityListRequest{TaskID: "task-1", PageSize: 10}).Validate(); err != nil {
		t.Fatalf("valid activity list rejected: %v", err)
	}
	if err := (WorkflowTaskActivityListRequest{TaskID: "task-1", PageSize: -1}).Validate(); !isWorkflowFieldError(err, "page_size", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("invalid activity page size error = %#v, want invalid_mode on page_size", err)
	}
	if err := (WorkflowTaskCommentListRequest{TaskID: "task-1", PageSize: WorkflowTaskCommentListMaxPageSize}).Validate(); err != nil {
		t.Fatalf("max comment page size rejected: %v", err)
	}
	if err := (WorkflowTaskCommentListRequest{TaskID: "task-1", PageSize: -1}).Validate(); !isWorkflowFieldError(err, "page_size", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("negative comment page size error = %#v, want invalid_mode on page_size", err)
	}
	if err := (WorkflowTaskCommentListRequest{TaskID: "task-1", PageSize: WorkflowTaskCommentListMaxPageSize + 1}).Validate(); !isWorkflowFieldError(err, "page_size", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("oversized comment page size error = %#v, want invalid_mode on page_size", err)
	}
}

func TestWorkflowExecutionPolicyAndInitiatingActionContractValidation(t *testing.T) {
	customRef := "refs/heads/release"
	if err := (WorkflowExecutionPolicy{Mode: WorkflowExecutionPolicyCustomRef, CustomRef: &customRef}).Validate(); err != nil {
		t.Fatalf("valid custom execution policy rejected: %v", err)
	}
	if err := (WorkflowExecutionPolicy{Mode: WorkflowExecutionPolicyHead, CustomRef: &customRef}).Validate(); !isWorkflowFieldError(err, "execution_policy.custom_ref", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("head policy with custom ref error = %#v, want invalid custom ref", err)
	}
	if err := (WorkflowExecutionPolicy{Mode: WorkflowExecutionPolicyCustomRef}).Validate(); !isWorkflowFieldError(err, "execution_policy.custom_ref", WorkflowRequestErrorRequired) {
		t.Fatalf("custom policy without ref error = %#v, want required custom ref", err)
	}

	selection := WorkflowTaskExecutionTargetSelection{Mode: WorkflowTaskExecutionTargetSelectionCustomRef, CustomRef: &customRef}
	if err := (WorkflowTaskStartRequest{TaskID: "task-1", SetupOperationID: NewWorktreeSetupOperationID(), Selection: &selection}).Validate(); err != nil {
		t.Fatalf("start override selection rejected: %v", err)
	}
	if err := (WorkflowTaskApproveRequest{TaskTransitionID: "transition-1", SetupOperationID: NewWorktreeSetupOperationID(), Selection: &selection}).Validate(); !isWorkflowFieldError(err, "selection_generation", WorkflowRequestErrorRequired) {
		t.Fatalf("approval selection without generation error = %#v, want required generation", err)
	}
	generation := "generation-1"
	if err := (WorkflowTaskMoveRequest{TaskID: "task-1", TargetNodeID: "node-1", SetupOperationID: NewWorktreeSetupOperationID(), SelectionGeneration: &generation, Selection: &selection}).Validate(); err != nil {
		t.Fatalf("move selection with generation rejected: %v", err)
	}

	selectionRequired := WorkflowTaskInitiatingActionResult{
		Outcome: WorkflowTaskInitiatingActionOutcomeSelectionRequired,
		SelectionRequired: &WorkflowTaskExecutionTargetSelectionRequired{
			TaskID:              "task-1",
			Generation:          "generation-1",
			SourceWorkspaceID:   "workspace-1",
			Source:              WorkflowTaskExecutionTargetSource{Kind: WorkflowTaskExecutionTargetSourceNamedRef, NamedRef: workflowTestStringPointer("refs/heads/main"), Commit: workflowTestStringPointer("abc123")},
			SupportedSelections: []WorkflowTaskExecutionTargetSelectionMode{WorkflowTaskExecutionTargetSelectionNone, WorkflowTaskExecutionTargetSelectionHead, WorkflowTaskExecutionTargetSelectionDefaultBranch, WorkflowTaskExecutionTargetSelectionCustomRef},
			ConfiguredPolicy:    WorkflowExecutionPolicy{Mode: WorkflowExecutionPolicyAsk},
		},
	}
	if err := selectionRequired.Validate(); err != nil {
		t.Fatalf("valid selection-required outcome rejected: %v", err)
	}

	conflict := WorkflowTaskInitiatingActionResult{
		Outcome: WorkflowTaskInitiatingActionOutcomeConflict,
		Conflict: &WorkflowTaskExecutionTargetNegotiationConflict{
			TaskID: "task-1",
		},
	}
	if err := conflict.Validate(); err != nil {
		t.Fatalf("valid conflict outcome rejected: %v", err)
	}
	invalidConflict := conflict
	invalidConflict.Conflict = &WorkflowTaskExecutionTargetNegotiationConflict{}
	if err := invalidConflict.Validate(); !isWorkflowFieldError(err, "conflict.task_id", WorkflowRequestErrorRequired) {
		t.Fatalf("conflict without task id error = %#v, want required task_id", err)
	}

	mixed := selectionRequired
	mixed.InProgress = &WorkflowTaskExecutionTargetMaterializationProgress{TaskID: "task-1", Phase: WorkflowTaskExecutionTargetMaterializationPhaseMaterializing}
	if err := mixed.Validate(); err == nil {
		t.Fatal("mixed outcome payloads accepted")
	}
}

func TestWorkflowTaskInitiatingActionResultJSONIsClosedUnion(t *testing.T) {
	result := WorkflowTaskInitiatingActionResult{
		Outcome: WorkflowTaskInitiatingActionOutcomeStarted,
		Started: &WorkflowTaskStartResponse{
			TransitionID: "transition-1",
			PlacementID:  "placement-1",
			RunID:        "run-1",
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if raw["outcome"] != string(WorkflowTaskInitiatingActionOutcomeStarted) {
		t.Fatalf("outcome = %#v, want started", raw["outcome"])
	}
	if _, ok := raw["started"]; !ok {
		t.Fatalf("started payload missing: %#v", raw)
	}
	if _, ok := raw["selection_required"]; ok {
		t.Fatalf("unexpected selection payload: %#v", raw)
	}

	var decoded WorkflowTaskInitiatingActionResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal typed result: %v", err)
	}
	if decoded.Started == nil || decoded.Started.RunID != "run-1" || decoded.SelectionRequired != nil || decoded.InProgress != nil {
		t.Fatalf("decoded result = %#v", decoded)
	}

	conflict := WorkflowTaskInitiatingActionResult{
		Outcome: WorkflowTaskInitiatingActionOutcomeConflict,
		Conflict: &WorkflowTaskExecutionTargetNegotiationConflict{
			TaskID: "task-1",
		},
	}
	conflictData, err := json.Marshal(conflict)
	if err != nil {
		t.Fatalf("marshal conflict result: %v", err)
	}
	if err := json.Unmarshal(conflictData, &decoded); err != nil {
		t.Fatalf("unmarshal conflict result: %v", err)
	}
	if decoded.Conflict == nil || decoded.Conflict.TaskID != "task-1" || decoded.Started != nil || decoded.SelectionRequired != nil || decoded.InProgress != nil {
		t.Fatalf("decoded conflict result = %#v", decoded)
	}

	if err := json.Unmarshal([]byte(`{"outcome":"started","started":{"transition_id":"transition-1","placement_id":"placement-1","run_id":"run-1"},"in_progress":{"task_id":"task-1","phase":"materializing"}}`), &decoded); err == nil {
		t.Fatal("mixed JSON result accepted")
	}
	if err := json.Unmarshal([]byte(`{"outcome":"conflict","conflict":{"task_id":"task-1"},"in_progress":{"task_id":"task-1","phase":"materializing"}}`), &decoded); err == nil {
		t.Fatal("mixed conflict JSON result accepted")
	}
}

func workflowTestStringPointer(value string) *string {
	return &value
}

func isWorkflowFieldError(err error, field string, code string) bool {
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		return false
	}
	return validationErr.Field == field && validationErr.Code == code
}

func TestWorkflowTaskQuestionAnswerApprovalJSON(t *testing.T) {
	req := WorkflowTaskQuestionAnswerRequest{
		ClientRequestID: "req-1",
		TaskID:          "task-1",
		AskID:           "ask-1",
		Approval: &WorkflowTaskQuestionApprovalAnswer{
			Decision:   clientui.ApprovalDecisionAllowOnce,
			Commentary: "trusted path",
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw request JSON: %v", err)
	}
	if _, ok := raw["Decision"]; ok {
		t.Fatalf("marshaled JSON contains Go decision field: %#v", raw)
	}
	if _, ok := raw["Commentary"]; ok {
		t.Fatalf("marshaled JSON contains Go commentary field: %#v", raw)
	}
	approval, ok := raw["approval"].(map[string]any)
	if !ok {
		t.Fatalf("marshaled JSON missing approval object: %#v", raw)
	}
	if approval["decision"] != string(clientui.ApprovalDecisionAllowOnce) || approval["commentary"] != "trusted path" {
		t.Fatalf("approval JSON = %#v", approval)
	}
	if _, ok := approval["Decision"]; ok {
		t.Fatalf("approval JSON contains Go decision field: %#v", approval)
	}
	if _, ok := approval["Commentary"]; ok {
		t.Fatalf("approval JSON contains Go commentary field: %#v", approval)
	}

	var decoded WorkflowTaskQuestionAnswerRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Approval == nil || decoded.Approval.Decision != clientui.ApprovalDecisionAllowOnce || decoded.Approval.Commentary != "trusted path" {
		t.Fatalf("decoded approval = %+v", decoded.Approval)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded request rejected: %v", err)
	}
}

func TestWorkflowAttentionQuestionPromptJSON(t *testing.T) {
	item := WorkflowAttentionItem{
		ID:                     "attention-1",
		Kind:                   "question",
		Message:                "Choose",
		Suggestions:            []string{"A", "B"},
		RecommendedOptionIndex: 1,
		Question: &WorkflowAttentionQuestionPrompt{
			Kind:                   WorkflowAttentionQuestionKindOrdinary,
			Suggestions:            []string{"A", "B"},
			RecommendedOptionIndex: 1,
		},
		OccurredAtUnixMs: 1,
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal ordinary question: %v", err)
	}
	var ordinaryRaw map[string]any
	if err := json.Unmarshal(data, &ordinaryRaw); err != nil {
		t.Fatalf("unmarshal ordinary question JSON: %v", err)
	}
	ordinaryQuestion, ok := ordinaryRaw["question"].(map[string]any)
	if !ok {
		t.Fatalf("ordinary question JSON missing question object: %#v", ordinaryRaw)
	}
	if ordinaryQuestion["kind"] != string(WorkflowAttentionQuestionKindOrdinary) {
		t.Fatalf("ordinary question kind = %#v", ordinaryQuestion["kind"])
	}
	ordinarySuggestions, ok := ordinaryQuestion["suggestions"].([]any)
	if !ok || len(ordinarySuggestions) != 2 || ordinarySuggestions[0] != "A" || ordinarySuggestions[1] != "B" {
		t.Fatalf("ordinary question suggestions = %#v", ordinaryQuestion["suggestions"])
	}
	if ordinaryQuestion["recommended_option_index"] != float64(1) {
		t.Fatalf("ordinary question recommended option = %#v", ordinaryQuestion["recommended_option_index"])
	}

	approval := WorkflowAttentionItem{
		ID:      "attention-2",
		Kind:    "question",
		Message: "Approve?",
		Question: &WorkflowAttentionQuestionPrompt{
			Kind: WorkflowAttentionQuestionKindApproval,
			ApprovalDecisions: []clientui.ApprovalDecision{
				clientui.ApprovalDecisionAllowOnce,
				clientui.ApprovalDecisionAllowSession,
				clientui.ApprovalDecisionDeny,
			},
		},
		OccurredAtUnixMs: 1,
	}
	data, err = json.Marshal(approval)
	if err != nil {
		t.Fatalf("marshal approval question: %v", err)
	}
	var approvalRaw map[string]any
	if err := json.Unmarshal(data, &approvalRaw); err != nil {
		t.Fatalf("unmarshal approval question JSON: %v", err)
	}
	approvalQuestion, ok := approvalRaw["question"].(map[string]any)
	if !ok {
		t.Fatalf("approval question JSON missing question object: %#v", approvalRaw)
	}
	if _, ok := approvalQuestion["label"]; ok {
		t.Fatalf("approval prompt JSON must not carry label: %#v", approvalQuestion)
	}
	if _, ok := approvalQuestion["Label"]; ok {
		t.Fatalf("approval prompt JSON must not carry Go label field: %#v", approvalQuestion)
	}
	if approvalQuestion["kind"] != string(WorkflowAttentionQuestionKindApproval) {
		t.Fatalf("approval question kind = %#v", approvalQuestion["kind"])
	}
	decisions, ok := approvalQuestion["approval_decisions"].([]any)
	wantDecisions := []any{
		string(clientui.ApprovalDecisionAllowOnce),
		string(clientui.ApprovalDecisionAllowSession),
		string(clientui.ApprovalDecisionDeny),
	}
	if !ok || !equalJSONArrays(decisions, wantDecisions) {
		t.Fatalf("approval decisions = %#v", approvalQuestion["approval_decisions"])
	}
}

func equalJSONArrays(got []any, want []any) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestWorkflowTaskListRequestValidation(t *testing.T) {
	valid := WorkflowTaskListRequest{
		ProjectID:   "project-1",
		PageSize:    WorkflowTaskListMaxPageSize,
		PageToken:   "token",
		StatusKeys:  []string{"backlog", "plan"},
		RunStatuses: []WorkflowTaskRunStatus{WorkflowTaskRunStatusOpen, WorkflowTaskRunStatusRunning, WorkflowTaskRunStatusDone, WorkflowTaskRunStatusCanceled},
		Sort: []WorkflowTaskListSort{
			{Field: WorkflowTaskListSortFieldStatus, Direction: WorkflowTaskListSortDirectionAsc},
			{Field: WorkflowTaskListSortFieldUpdated, Direction: WorkflowTaskListSortDirectionDesc},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid task list request rejected: %v", err)
	}
	if err := (WorkflowTaskListRequest{ProjectID: "project-1"}).Validate(); err != nil {
		t.Fatalf("request with default sort rejected: %v", err)
	}

	tests := []struct {
		name  string
		req   WorkflowTaskListRequest
		field string
		code  string
	}{
		{
			name:  "required project",
			req:   WorkflowTaskListRequest{ProjectID: " "},
			field: "project_id",
			code:  WorkflowRequestErrorRequired,
		},
		{
			name:  "negative page size",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", PageSize: -1},
			field: "page_size",
			code:  WorkflowRequestErrorInvalidMode,
		},
		{
			name:  "oversized page size",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", PageSize: WorkflowTaskListMaxPageSize + 1},
			field: "page_size",
			code:  WorkflowRequestErrorInvalidMode,
		},
		{
			name:  "page token whitespace",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", PageToken: " token"},
			field: "page_token",
			code:  WorkflowRequestErrorInvalidMode,
		},
		{
			name:  "invalid sort field",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", Sort: []WorkflowTaskListSort{{Field: "priority", Direction: WorkflowTaskListSortDirectionAsc}}},
			field: "sort[0].field",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name:  "invalid sort direction",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", Sort: []WorkflowTaskListSort{{Field: WorkflowTaskListSortFieldCreated, Direction: "up"}}},
			field: "sort[0].direction",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "duplicate sort field",
			req: WorkflowTaskListRequest{ProjectID: "project-1", Sort: []WorkflowTaskListSort{
				{Field: WorkflowTaskListSortFieldTitle, Direction: WorkflowTaskListSortDirectionAsc},
				{Field: WorkflowTaskListSortFieldTitle, Direction: WorkflowTaskListSortDirectionDesc},
			}},
			field: "sort[1].field",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "too many sort fields",
			req: WorkflowTaskListRequest{ProjectID: "project-1", Sort: []WorkflowTaskListSort{
				{Field: WorkflowTaskListSortFieldCreated, Direction: WorkflowTaskListSortDirectionAsc},
				{Field: WorkflowTaskListSortFieldUpdated, Direction: WorkflowTaskListSortDirectionAsc},
				{Field: WorkflowTaskListSortFieldStatus, Direction: WorkflowTaskListSortDirectionAsc},
				{Field: WorkflowTaskListSortFieldRunCount, Direction: WorkflowTaskListSortDirectionAsc},
				{Field: WorkflowTaskListSortFieldTitle, Direction: WorkflowTaskListSortDirectionAsc},
				{Field: WorkflowTaskListSortField("future"), Direction: WorkflowTaskListSortDirectionAsc},
			}},
			field: "sort",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name:  "invalid run status",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", RunStatuses: []WorkflowTaskRunStatus{"waiting"}},
			field: "run_statuses[0]",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name:  "blank status key",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", StatusKeys: []string{" "}},
			field: "status_keys[0]",
			code:  WorkflowRequestErrorInvalidKey,
		},
		{
			name:  "invalid status key syntax",
			req:   WorkflowTaskListRequest{ProjectID: "project-1", StatusKeys: []string{"Plan"}},
			field: "status_keys[0]",
			code:  WorkflowRequestErrorInvalidKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); !isWorkflowFieldError(err, tt.field, tt.code) {
				t.Fatalf("validation error = %#v, want %s on %s", err, tt.code, tt.field)
			}
		})
	}
}

func TestWorkflowTaskListResponseJSONShape(t *testing.T) {
	selected := WorkflowTaskListResponse{
		ProjectID:         "project-1",
		WorkflowID:        "workflow-1",
		NextPageToken:     "next",
		SelectedWorkflow:  &WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Main", Version: 3},
		GeneratedAtUnixMs: 10,
		Tasks: []WorkflowTaskListItem{{
			TaskID:          "task-1",
			ShortID:         "BLD-1",
			WorkflowID:      "workflow-1",
			Title:           "Task",
			CreatedAtUnixMs: 11,
			UpdatedAtUnixMs: 12,
			StatusKeys:      []string{"plan", "qa"},
			RunStatus:       WorkflowTaskRunStatusRunning,
			RunCount:        2,
		}},
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		t.Fatalf("marshal selected response: %v", err)
	}
	var selectedShape map[string]any
	if err := json.Unmarshal(raw, &selectedShape); err != nil {
		t.Fatalf("unmarshal selected response: %v", err)
	}
	if got := selectedShape["workflow_id"]; got != "workflow-1" {
		t.Fatalf("workflow_id = %#v, want workflow-1", got)
	}
	if _, ok := selectedShape["selected_workflow"]; !ok {
		t.Fatalf("selected_workflow missing from selected response JSON: %s", raw)
	}
	tasks, ok := selectedShape["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks shape = %#v, want one task", selectedShape["tasks"])
	}
	task, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("task shape = %#v, want object", tasks[0])
	}
	for _, key := range []string{"status_keys", "run_status", "created_at_unix_ms", "updated_at_unix_ms", "run_count"} {
		if _, ok := task[key]; !ok {
			t.Fatalf("task JSON missing %s: %s", key, raw)
		}
	}

	empty := WorkflowTaskListResponse{ProjectID: "project-1", WorkflowID: "", Tasks: []WorkflowTaskListItem{}}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty response: %v", err)
	}
	var emptyShape map[string]any
	if err := json.Unmarshal(raw, &emptyShape); err != nil {
		t.Fatalf("unmarshal empty response: %v", err)
	}
	if got, ok := emptyShape["workflow_id"]; !ok || got != "" {
		t.Fatalf("empty workflow_id = %#v present=%v, want present empty string", got, ok)
	}
	if _, ok := emptyShape["selected_workflow"]; ok {
		t.Fatalf("selected_workflow present in no-selected-workflow response JSON: %s", raw)
	}
}

func TestWorkflowValidateRequestValidation(t *testing.T) {
	for _, mode := range []WorkflowValidationMode{"", WorkflowValidationModeDraft, WorkflowValidationModeTaskCreation, WorkflowValidationModeExecution} {
		if err := (WorkflowValidateRequest{WorkflowID: "workflow-1", Mode: mode}).Validate(); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	if err := (WorkflowValidateRequest{WorkflowID: "workflow-1", Mode: "other"}).Validate(); !isWorkflowFieldError(err, "mode", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("invalid mode error = %#v, want invalid_mode on mode", err)
	}
}

func TestWorkflowScriptPathValidateRequestValidation(t *testing.T) {
	valid := WorkflowScriptPathValidateRequest{WorkflowID: "workflow-1", NodeID: "node-script", ScriptPath: ""}
	if err := valid.Validate(); err != nil {
		t.Fatalf("empty script path should be accepted for diagnostic validation: %v", err)
	}
	if err := (WorkflowScriptPathValidateRequest{NodeID: "node-script"}).Validate(); !isWorkflowFieldError(err, "workflow_id", WorkflowRequestErrorRequired) {
		t.Fatalf("missing workflow_id error = %#v, want required workflow_id", err)
	}
	if err := (WorkflowScriptPathValidateRequest{WorkflowID: "workflow-1"}).Validate(); !isWorkflowFieldError(err, "node_id", WorkflowRequestErrorRequired) {
		t.Fatalf("missing node_id error = %#v, want required node_id", err)
	}
}

func TestWorkflowGraphDraftRequestValidation(t *testing.T) {
	graphWithInvalidShape := WorkflowGraphDraft{
		Nodes: []WorkflowGraphDraftNode{{ID: "node-1", Key: "Bad-Key", Kind: "unknown"}},
	}
	if err := (WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Graph: graphWithInvalidShape, Modes: []WorkflowValidationMode{WorkflowValidationModeDraft, WorkflowValidationModeExecution}}).Validate(); err != nil {
		t.Fatalf("graph shape should be validated by workflow validation, not request validation: %v", err)
	}
	if err := (WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{WorkflowValidationModeDraft, WorkflowValidationModeExecution}}).Validate(); err != nil {
		t.Fatalf("empty graph draft should be accepted for structured validation: %v", err)
	}
	if err := (WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Metadata: &WorkflowGraphMetadata{Name: "Draft Name", Description: "Draft description"}, Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}}).Validate(); err != nil {
		t.Fatalf("draft metadata should be accepted for validation: %v", err)
	}
	if err := (WorkflowGraphValidateDraftRequest{WorkflowID: "", Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}}).Validate(); !isWorkflowFieldError(err, "workflow_id", WorkflowRequestErrorRequired) {
		t.Fatalf("missing workflow id error = %#v, want required on workflow_id", err)
	}
	if err := (WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1"}).Validate(); !isWorkflowFieldError(err, "modes", WorkflowRequestErrorRequired) {
		t.Fatalf("missing modes error = %#v, want required on modes", err)
	}
	if err := (WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{"other"}}).Validate(); !isWorkflowFieldError(err, "modes", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("invalid modes error = %#v, want invalid_mode on modes", err)
	}
	oversized := WorkflowGraphValidateDraftRequest{
		WorkflowID: "workflow-1",
		Modes:      []WorkflowValidationMode{WorkflowValidationModeDraft},
		Graph:      WorkflowGraphDraft{Nodes: make([]WorkflowGraphDraftNode, WorkflowGraphDraftMaxNodes+1)},
	}
	if err := oversized.Validate(); !isWorkflowFieldError(err, "graph.nodes", WorkflowRequestErrorTooLong) {
		t.Fatalf("oversized graph draft error = %#v, want too_long on graph.nodes", err)
	}
	invalidMode := WorkflowGraphValidateDraftRequest{
		WorkflowID: "workflow-1",
		Modes:      []WorkflowValidationMode{WorkflowValidationModeDraft},
		Graph:      WorkflowGraphDraft{Nodes: []WorkflowGraphDraftNode{{ID: "node-1", Kind: "agent", CompletionMode: "invalid"}}},
	}
	if err := invalidMode.Validate(); !isWorkflowFieldError(err, "graph.nodes.completion_mode", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid graph node completion mode error = %#v, want invalid_value on graph.nodes.completion_mode", err)
	}
	scriptPath := "scripts/run"
	nonScriptPathGraph := WorkflowGraphSavePreviewRequest{
		WorkflowID:      "workflow-1",
		ExpectedVersion: 1,
		Graph:           WorkflowGraphDraft{Nodes: []WorkflowGraphDraftNode{{ID: "node-1", Kind: "agent", ScriptPath: &scriptPath}}},
	}
	if err := nonScriptPathGraph.Validate(); !isWorkflowFieldError(err, "graph.nodes.script_path", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("non-script script_path error = %#v, want invalid_value on graph.nodes.script_path", err)
	}
	validPreviousTargetOrNewGraph := WorkflowGraphDraft{
		Edges: []WorkflowGraphDraftEdge{{ID: "edge-1", ContextSource: WorkflowContextSource{Kind: "previous_target_or_new"}}},
	}
	if err := (WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}, Graph: validPreviousTargetOrNewGraph}).Validate(); err != nil {
		t.Fatalf("valid graph previous_target_or_new context source rejected: %v", err)
	}
	invalidGraphSourceKind := validPreviousTargetOrNewGraph
	invalidGraphSourceKind.Edges = []WorkflowGraphDraftEdge{{ID: "edge-1", ContextSource: WorkflowContextSource{Kind: "other"}}}
	if err := (WorkflowGraphSaveRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Graph: invalidGraphSourceKind}).Validate(); !isWorkflowFieldError(err, "context_source.kind", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid graph context source kind error = %#v, want invalid_value on context_source.kind", err)
	}
	invalidGraphSourceNodeKey := validPreviousTargetOrNewGraph
	invalidGraphSourceNodeKey.Edges = []WorkflowGraphDraftEdge{{ID: "edge-1", ContextSource: WorkflowContextSource{Kind: "previous_target_or_new", NodeKey: "implement"}}}
	if err := (WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Graph: invalidGraphSourceNodeKey}).Validate(); !isWorkflowFieldError(err, "context_source.node_key", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid graph context source node key error = %#v, want invalid_value on context_source.node_key", err)
	}
	if err := (WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: -1}).Validate(); !isWorkflowFieldError(err, "expected_version", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("negative preview revision error = %#v, want invalid_value on expected_version", err)
	}
	if err := (WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Metadata: &WorkflowGraphMetadata{Name: "Draft Name"}}).Validate(); err != nil {
		t.Fatalf("metadata preview with expected version rejected: %v", err)
	}
	if err := (WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Metadata: &WorkflowGraphMetadata{Name: " Draft Name "}}).Validate(); !isWorkflowFieldError(err, "metadata.name", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("invalid metadata name error = %#v, want invalid_value on metadata.name", err)
	}
	if err := (WorkflowGraphSaveRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Confirmation: &WorkflowGraphSaveConfirmation{ExpectedRemovedNodeCount: -1}}).Validate(); !isWorkflowFieldError(err, "expected_removed_node_count", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("negative graph save confirmation error = %#v, want invalid_value on expected_removed_node_count", err)
	}
}

func TestWorkflowProjectLinkRequestValidation(t *testing.T) {
	if err := (WorkflowLinkProjectRequest{ProjectID: "project-1", WorkflowID: "workflow-1"}).Validate(); err != nil {
		t.Fatalf("valid link request rejected: %v", err)
	}
	if err := (WorkflowLinkProjectRequest{
		ProjectID:     "project-1",
		WorkflowID:    "workflow-1",
		DefaultPolicy: WorkflowProjectLinkDefaultIfProjectHasNone,
	}).Validate(); err != nil {
		t.Fatalf("valid link default policy rejected: %v", err)
	}
	if err := (WorkflowLinkProjectRequest{
		ProjectID:     "project-1",
		WorkflowID:    "workflow-1",
		DefaultPolicy: "sometimes",
	}).Validate(); !isWorkflowFieldError(err, "default_policy", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("invalid link default policy error = %#v, want invalid_mode on default_policy", err)
	}
	if err := (WorkflowCreateAndLinkProjectRequest{Name: "Workflow", ProjectID: "project-1", DefaultPolicy: WorkflowProjectLinkDefaultIfProjectHasNone}).Validate(); err != nil {
		t.Fatalf("valid create and link request rejected: %v", err)
	}
	if err := (WorkflowListProjectLinksRequest{ProjectID: "project-1"}).Validate(); err != nil {
		t.Fatalf("valid list links request rejected: %v", err)
	}
	if err := (WorkflowListRequest{PageSize: 20, PageToken: "10", Query: "agent"}).Validate(); err != nil {
		t.Fatalf("valid workflow list request rejected: %v", err)
	}
	if err := (WorkflowListRequest{PageSize: -1}).Validate(); !isWorkflowFieldError(err, "page_size", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("invalid page size error = %#v, want invalid_mode on page_size", err)
	}
	if err := (WorkflowListRequest{PageSize: WorkflowListMaxPageSize + 1}).Validate(); !isWorkflowFieldError(err, "page_size", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("oversized page size error = %#v, want invalid_mode on page_size", err)
	}
	if err := (WorkflowListRequest{PageToken: " 10"}).Validate(); !isWorkflowFieldError(err, "page_token", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("invalid page token error = %#v, want invalid_mode on page_token", err)
	}
	if err := (WorkflowSetDefaultProjectLinkRequest{ProjectID: "project-1", WorkflowID: "workflow-1"}).Validate(); err != nil {
		t.Fatalf("valid set default request rejected: %v", err)
	}
	if err := (WorkflowSetDefaultProjectLinkRequest{ProjectID: "", WorkflowID: "workflow-1"}).Validate(); !isWorkflowFieldError(err, "project_id", WorkflowRequestErrorRequired) {
		t.Fatalf("empty project id error = %#v, want required on project_id", err)
	}
}

func TestWorkflowDeleteRequestValidation(t *testing.T) {
	if err := (WorkflowDeletePreviewRequest{WorkflowID: "workflow-1"}).Validate(); err != nil {
		t.Fatalf("valid delete preview rejected: %v", err)
	}
	if err := (WorkflowDeletePreviewRequest{}).Validate(); !isWorkflowFieldError(err, "workflow_id", WorkflowRequestErrorRequired) {
		t.Fatalf("empty delete preview workflow id error = %#v, want required on workflow_id", err)
	}
	if err := (WorkflowDeleteRequest{
		WorkflowID:           "workflow-1",
		Confirmed:            true,
		ExpectedVersion:      1,
		ExpectedProjectCount: 1,
		ExpectedLinkCount:    1,
		ExpectedTaskCount:    1,
	}).Validate(); err != nil {
		t.Fatalf("valid delete request rejected: %v", err)
	}
	if err := (WorkflowDeleteRequest{}).Validate(); !isWorkflowFieldError(err, "workflow_id", WorkflowRequestErrorRequired) {
		t.Fatalf("empty delete workflow id error = %#v, want required on workflow_id", err)
	}
	if err := (WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedVersion: -1}).Validate(); !isWorkflowFieldError(err, "expected_version", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("negative graph revision error = %#v, want invalid_mode on expected_version", err)
	}
	if err := (WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedProjectCount: -1}).Validate(); !isWorkflowFieldError(err, "expected_project_count", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("negative project count error = %#v, want invalid_mode on expected_project_count", err)
	}
	if err := (WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedLinkCount: -1}).Validate(); !isWorkflowFieldError(err, "expected_link_count", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("negative link count error = %#v, want invalid_mode on expected_link_count", err)
	}
	if err := (WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedTaskCount: -1}).Validate(); !isWorkflowFieldError(err, "expected_task_count", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("negative task count error = %#v, want invalid_mode on expected_task_count", err)
	}
}
