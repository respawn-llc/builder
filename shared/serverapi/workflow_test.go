package serverapi

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/textutil"
)

type workflowRequestValidator interface {
	Validate() error
}

type workflowValidRequestCase struct {
	name    string
	request workflowRequestValidator
}

type workflowFieldErrorCase struct {
	name    string
	request workflowRequestValidator
	field   string
	code    string
}

func testValidWorkflowRequests(t *testing.T, cases []workflowValidRequestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.request.Validate(); err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
		})
	}
}

func testWorkflowFieldErrors(t *testing.T, cases []workflowFieldErrorCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.request.Validate(); !isWorkflowFieldError(err, tc.field, tc.code) {
				t.Fatalf("validation error = %#v, want %s on %s", err, tc.code, tc.field)
			}
		})
	}
}

func marshalWorkflowJSON[T any](t *testing.T, value any) ([]byte, T) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	var decoded T
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	return data, decoded
}

func TestWorkflowCreateUpdateRequestValidation(t *testing.T) {
	testValidWorkflowRequests(t, []workflowValidRequestCase{{name: "create", request: WorkflowCreateRequest{Name: "Pipeline"}}})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "create requires name", request: WorkflowCreateRequest{Name: " "}, field: "name", code: WorkflowRequestErrorRequired},
		{name: "update caps name", request: WorkflowUpdateRequest{WorkflowID: "workflow-1", Name: strings.Repeat("x", 121)}, field: "name", code: WorkflowRequestErrorTooLong},
	})
}

func TestWorkflowNodeAndEdgeRequestValidation(t *testing.T) {
	testValidWorkflowRequests(t, []workflowValidRequestCase{
		{name: "node", request: WorkflowNodeAddRequest{WorkflowID: "workflow-1", Key: "implement", Kind: "agent", DisplayName: "Implement", InputFields: []WorkflowInputField{{Name: "summary", Description: "Summary"}}}},
		{name: "edge", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "new_session", PromptTemplate: "Do the next step.", Parameters: []WorkflowParameter{{Key: "summary", Description: "Summary"}}}},
		{name: "selected context source", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "continue_session", ContextSource: WorkflowContextSource{Kind: "selected_node", NodeKey: "implement"}, PromptTemplate: "Do the next step.", Parameters: []WorkflowParameter{{Key: "summary", Description: "Summary"}}}},
		{name: "previous target context source", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "continue_session", ContextSource: WorkflowContextSource{Kind: "previous_target"}, PromptTemplate: "Do the next step.", Parameters: []WorkflowParameter{{Key: "summary", Description: "Summary"}}}},
		{name: "previous target or new context source", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "continue_session", ContextSource: WorkflowContextSource{Kind: "previous_target_or_new"}, PromptTemplate: "Do the next step.", Parameters: []WorkflowParameter{{Key: "summary", Description: "Summary"}}}},
	})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "node rejects invalid key", request: WorkflowNodeAddRequest{WorkflowID: "workflow-1", Key: "Bad-Key", Kind: "agent", DisplayName: "Implement"}, field: "key", code: WorkflowRequestErrorInvalidKey},
		{name: "node requires display name", request: WorkflowNodeAddRequest{WorkflowID: "workflow-1", Key: "implement", Kind: "agent"}, field: "display_name", code: WorkflowRequestErrorRequired},
		{name: "terminal rejects completion mode", request: WorkflowNodeAddRequest{WorkflowID: "workflow-1", Key: "done", Kind: "terminal", DisplayName: "Done", CompletionMode: "tool"}, field: "completion_mode", code: WorkflowRequestErrorInvalidValue},
		{name: "node rejects invalid completion mode", request: WorkflowNodeAddRequest{WorkflowID: "workflow-1", Key: "implement", Kind: "agent", DisplayName: "Implement", CompletionMode: "invalid"}, field: "completion_mode", code: WorkflowRequestErrorInvalidValue},
		{name: "edge caps parameters", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "new_session", PromptTemplate: "Do the next step.", Parameters: make([]WorkflowParameter, WorkflowGraphDraftMaxFieldsPerEntity+1)}, field: "parameters", code: WorkflowRequestErrorTooLong},
		{name: "previous target rejects node key", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "continue_session", ContextSource: WorkflowContextSource{Kind: "previous_target", NodeKey: "implement"}, PromptTemplate: "Do the next step."}, field: "context_source.node_key", code: WorkflowRequestErrorInvalidValue},
		{name: "previous target or new rejects node key", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "continue_session", ContextSource: WorkflowContextSource{Kind: "previous_target_or_new", NodeKey: "implement"}, PromptTemplate: "Do the next step."}, field: "context_source.node_key", code: WorkflowRequestErrorInvalidValue},
		{name: "selected source rejects invalid node key", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "continue_session", ContextSource: WorkflowContextSource{Kind: "selected_node", NodeKey: "Bad-Key"}, PromptTemplate: "Do the next step."}, field: "context_source.node_key", code: WorkflowRequestErrorInvalidKey},
		{name: "edge rejects context source kind", request: WorkflowEdgeAddRequest{WorkflowID: "workflow-1", TransitionGroupID: "group-1", Key: "done", TargetNodeID: "node-2", ContextMode: "continue_session", ContextSource: WorkflowContextSource{Kind: "other", NodeKey: "implement"}, PromptTemplate: "Do the next step."}, field: "context_source.kind", code: WorkflowRequestErrorInvalidValue},
	})
}

func TestWorkflowTransitionGroupDescriptionRequestValidation(t *testing.T) {
	validAdd := WorkflowTransitionGroupAddRequest{
		WorkflowID:   "workflow-1",
		SourceNodeID: "node-1",
		TransitionID: "review",
		DisplayName:  "Review",
		Description:  "Use this when implementation needs review.",
	}
	emptyDescriptionAdd := validAdd
	emptyDescriptionAdd.Description = ""
	oversizedAdd := validAdd
	oversizedAdd.Description = strings.Repeat("x", 1001)

	validUpdate := WorkflowTransitionGroupUpdateRequest{
		WorkflowID:   "workflow-1",
		GroupID:      "group-1",
		SourceNodeID: "node-1",
		TransitionID: "review",
		DisplayName:  "Review",
		Description:  "Use this when implementation needs review.",
	}
	emptyDescriptionUpdate := validUpdate
	emptyDescriptionUpdate.Description = ""
	oversizedUpdate := validUpdate
	oversizedUpdate.Description = strings.Repeat("x", 1001)
	testValidWorkflowRequests(t, []workflowValidRequestCase{
		{name: "add", request: validAdd},
		{name: "add empty description", request: emptyDescriptionAdd},
		{name: "update", request: validUpdate},
		{name: "update empty description", request: emptyDescriptionUpdate},
	})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "add caps description", request: oversizedAdd, field: "description", code: WorkflowRequestErrorTooLong},
		{name: "update caps description", request: oversizedUpdate, field: "description", code: WorkflowRequestErrorTooLong},
	})
}

func TestWorkflowTaskAndCommentRequestValidation(t *testing.T) {
	setupOperationID := NewWorktreeSetupOperationID()
	if err := (WorkflowTaskCreateRequest{ProjectID: "project-1", Title: "Task"}).Validate(); err != nil {
		t.Fatalf("valid task create rejected: %v", err)
	}
	blankWorkflowID := " "
	if err := (WorkflowTaskCreateRequest{ProjectID: "project-1", WorkflowID: &blankWorkflowID, Title: "Task"}).Validate(); !isWorkflowFieldError(err, "workflow_id", WorkflowRequestErrorRequired) {
		t.Fatalf("blank present workflow id error = %#v, want required on workflow_id", err)
	}
	if err := (WorkflowTaskCreateRequest{ProjectID: "project-1", Title: "", Body: "Body"}).Validate(); !isWorkflowFieldError(err, "title", WorkflowRequestErrorRequired) {
		t.Fatalf("empty title error = %#v, want required on title", err)
	}
	updateTitle := "Task"
	blankTitle := " "
	selectedOption := 1
	zeroOption := 0
	negativeOption := -1
	testValidWorkflowRequests(t, []workflowValidRequestCase{
		{name: "task create", request: WorkflowTaskCreateRequest{ProjectID: "project-1", Title: "Task"}},
		{name: "task update", request: WorkflowTaskUpdateRequest{TaskID: "task-1", Title: &updateTitle}},
		{name: "task update omits title", request: WorkflowTaskUpdateRequest{TaskID: "task-1"}},
		{name: "task start", request: WorkflowTaskStartRequest{TaskID: "task-1", SetupOperationID: setupOperationID}},
		{name: "task get by project short id", request: WorkflowTaskGetRequest{ProjectID: "project-1", ShortID: "BLD-1"}},
		{name: "task get by global short id", request: WorkflowTaskGetRequest{ShortID: "BLD-1"}},
		{name: "task resume", request: WorkflowTaskResumeRequest{TaskID: "task-1"}},
		{name: "task interrupt", request: WorkflowTaskInterruptRequest{TaskID: "task-1"}},
		{name: "task approve", request: WorkflowTaskApproveRequest{TaskTransitionID: "transition-1", SetupOperationID: setupOperationID}},
		{name: "agent completion by session", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorAgent, AgentSessionID: "session-1"}},
		{name: "agent completion by run", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorAgent, AgentSessionID: "session-1", RunID: "run-1"}},
		{name: "user completion with run and project", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true, RunID: "run-1", ProjectID: "project-1"}},
		{name: "question freeform", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", FreeformAnswer: "answer"}},
		{name: "question option and freeform", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", SelectedOptionNumber: &selectedOption, FreeformAnswer: "because"}},
		{name: "question approval", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce, Commentary: "trusted"}}},
		{name: "user comment", request: WorkflowTaskCommentAddRequest{TaskID: "task-1", Body: "comment", Author: "user"}},
		{name: "agent comment", request: WorkflowTaskCommentAddRequest{TaskID: "task-1", Body: "comment", Author: "agent"}},
		{name: "activity page", request: WorkflowTaskActivityListRequest{TaskID: "task-1", PageSize: 10}},
		{name: "max comment page", request: WorkflowTaskCommentListRequest{TaskID: "task-1", PageSize: WorkflowTaskCommentListMaxPageSize}},
	})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "task create requires title", request: WorkflowTaskCreateRequest{ProjectID: "project-1", Body: "Body"}, field: "title", code: WorkflowRequestErrorRequired},
		{name: "task update requires nonblank title", request: WorkflowTaskUpdateRequest{TaskID: "task-1", Title: &blankTitle}, field: "title", code: WorkflowRequestErrorRequired},
		{name: "task get rejects blank short id", request: WorkflowTaskGetRequest{ProjectID: "project-1", ShortID: " "}, field: "short_id", code: WorkflowRequestErrorInvalidMode},
		{name: "task get rejects blank task id selector", request: WorkflowTaskGetRequest{TaskID: " ", ShortID: "BLD-1"}, field: "task_id", code: WorkflowRequestErrorInvalidMode},
		{name: "task get rejects blank project id selector", request: WorkflowTaskGetRequest{ProjectID: " ", ShortID: "BLD-1"}, field: "project_id", code: WorkflowRequestErrorInvalidMode},
		{name: "task approve requires transition", request: WorkflowTaskApproveRequest{}, field: "transition_id", code: WorkflowRequestErrorRequired},
		{name: "agent completion rejects force", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorAgent, AgentSessionID: "session-1", Force: true}, field: "force", code: WorkflowRequestErrorInvalidMode},
		{name: "user completion requires force", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, RunID: "run-1"}, field: "force", code: WorkflowRequestErrorInvalidMode},
		{name: "user completion requires selector", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true}, field: "selector", code: WorkflowRequestErrorRequired},
		{name: "user completion rejects multiple selectors", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true, RunID: "run-1", SessionID: "session-1"}, field: "selector", code: WorkflowRequestErrorInvalidMode},
		{name: "user completion rejects project-only selector", request: WorkflowTaskCompleteRequest{ActorKind: WorkflowTaskCompleteActorUser, Force: true, ProjectID: "project-1"}, field: "selector", code: WorkflowRequestErrorRequired},
		{name: "question rejects zero option", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", SelectedOptionNumber: &zeroOption}, field: "selected_option_number", code: WorkflowRequestErrorInvalidMode},
		{name: "question rejects negative option", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", SelectedOptionNumber: &negativeOption}, field: "selected_option_number", code: WorkflowRequestErrorInvalidMode},
		{name: "question rejects error plus answer", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", ErrorMessage: "err", FreeformAnswer: "answer"}, field: "error_message", code: WorkflowRequestErrorInvalidMode},
		{name: "question rejects legacy plus freeform", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Answer: "one", FreeformAnswer: "two"}, field: "answer", code: WorkflowRequestErrorInvalidMode},
		{name: "question rejects approval plus option", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", SelectedOptionNumber: &selectedOption, Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce}}, field: "approval", code: WorkflowRequestErrorInvalidMode},
		{name: "question rejects approval plus answer", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce}, FreeformAnswer: "also"}, field: "approval", code: WorkflowRequestErrorInvalidMode},
		{name: "question rejects approval plus error", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce}, ErrorMessage: "err"}, field: "error_message", code: WorkflowRequestErrorInvalidMode},
		{name: "question rejects invalid approval decision", request: WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-1", TaskID: "task-1", AskID: "ask-1", Approval: &WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecision("future")}}, field: "approval.decision", code: WorkflowRequestErrorInvalidValue},
		{name: "comment rejects system author", request: WorkflowTaskCommentAddRequest{TaskID: "task-1", Body: "comment", Author: "system"}, field: "author", code: WorkflowRequestErrorInvalidValue},
		{name: "comment requires body", request: WorkflowTaskCommentAddRequest{TaskID: "task-1", Author: "user"}, field: "body", code: WorkflowRequestErrorRequired},
		{name: "activity page rejects negative size", request: WorkflowTaskActivityListRequest{TaskID: "task-1", PageSize: -1}, field: "page_size", code: WorkflowRequestErrorInvalidMode},
		{name: "comment page rejects negative size", request: WorkflowTaskCommentListRequest{TaskID: "task-1", PageSize: -1}, field: "page_size", code: WorkflowRequestErrorInvalidMode},
		{name: "comment page rejects oversized size", request: WorkflowTaskCommentListRequest{TaskID: "task-1", PageSize: WorkflowTaskCommentListMaxPageSize + 1}, field: "page_size", code: WorkflowRequestErrorInvalidMode},
	})
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

	data, raw := marshalWorkflowJSON[map[string]any](t, req)
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

func TestWorkflowTaskQuestionAnswerSelectedOptionJSONUsesNullableValue(t *testing.T) {
	const omittedSelection = `{
		"client_request_id":"req-1",
		"task_id":"task-1",
		"ask_id":"ask-1",
		"freeform_answer":"typed"
	}`
	var omitted WorkflowTaskQuestionAnswerRequest
	if err := json.Unmarshal([]byte(omittedSelection), &omitted); err != nil {
		t.Fatalf("unmarshal omitted selection: %v", err)
	}
	if omitted.SelectedOptionNumber != nil {
		t.Fatalf("omitted selected option = %v, want nil", *omitted.SelectedOptionNumber)
	}
	if err := omitted.Validate(); err != nil {
		t.Fatalf("omitted selection with freeform answer rejected: %v", err)
	}

	const malformedZeroSelection = `{
		"client_request_id":"req-1",
		"task_id":"task-1",
		"ask_id":"ask-1",
		"selected_option_number":0,
		"freeform_answer":"typed"
	}`
	var malformed WorkflowTaskQuestionAnswerRequest
	if err := json.Unmarshal([]byte(malformedZeroSelection), &malformed); err != nil {
		t.Fatalf("unmarshal zero selection: %v", err)
	}
	if malformed.SelectedOptionNumber == nil || *malformed.SelectedOptionNumber != 0 {
		t.Fatalf("decoded zero selection = %v, want present zero", malformed.SelectedOptionNumber)
	}
	if err := malformed.Validate(); !isWorkflowFieldError(err, "selected_option_number", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("present zero selection error = %#v, want invalid_mode on selected_option_number", err)
	}
}

func TestWorkflowBoardJSONContainsMetadataOnly(t *testing.T) {
	board := WorkflowBoard{
		ProjectID: "project-1",
		Project: ProjectBoardProject{
			ProjectKey:             "KNT",
			DisplayName:            "Kent",
			DefaultWorkspaceID:     "workspace-default",
			AttachedWorkspaceCount: 2,
		},
	}

	raw, shape := marshalWorkflowJSON[map[string]any](t, board)
	project, ok := shape["project"].(map[string]any)
	if !ok || project["default_workspace_id"] != "workspace-default" || project["attached_workspace_count"] != float64(2) {
		t.Fatalf("project JSON = %#v, want workspace facts", shape["project"])
	}
	if _, exists := project["project_id"]; exists {
		t.Fatalf("board project JSON duplicates outer project_id: %#v", project)
	}
	for _, forbidden := range []string{"cards", "done_preview", "has_hidden_done_cards", "next_page_token"} {
		if _, ok := shape[forbidden]; ok {
			t.Fatalf("board metadata JSON contains card-owned key %q: %#v", forbidden, shape)
		}
	}

	var decoded WorkflowBoard
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal board: %v", err)
	}
	if decoded.Project.DefaultWorkspaceID != "workspace-default" || decoded.Project.AttachedWorkspaceCount != 2 {
		t.Fatalf("decoded board = %+v, want project facts", decoded)
	}
}

func TestWorkflowBoardCardJSONContainsNestedMarkdownPreviewAndNullableCursors(t *testing.T) {
	_, cardShape := marshalWorkflowJSON[map[string]any](t, WorkflowBoardTaskCard{
		TaskID:  "task-1",
		ShortID: "KNT-1",
		Title:   "Task",
		Preview: MarkdownPreview{
			Markdown: "Complete body must not cross the board-card boundary",
		},
		WorkflowID: "workflow-1",
	})
	if _, ok := cardShape["body"]; ok {
		t.Fatalf("board card JSON contains full body: %#v", cardShape)
	}
	preview, ok := cardShape["preview"].(map[string]any)
	if !ok {
		t.Fatalf("board card preview JSON = %#v, want nested object", cardShape["preview"])
	}
	if preview["markdown"] != "Complete body must not cross the board-card boundary" || preview["truncated"] != false {
		t.Fatalf("board card preview JSON = %#v, want markdown and truncation fact", preview)
	}
	for _, obsolete := range []string{"body_preview", "preview_markdown", "preview_truncated"} {
		if _, ok := cardShape[obsolete]; ok {
			t.Fatalf("board card JSON contains obsolete flat preview key %q: %#v", obsolete, cardShape)
		}
	}

	_, pageShape := marshalWorkflowJSON[map[string]any](t, WorkflowBoardNodeCardsListResponse{
		ProjectID:  "project-1",
		WorkflowID: "workflow-1",
		NodeID:     "node-1",
	})
	for _, cursor := range []string{"previous_page_token", "next_page_token"} {
		value, ok := pageShape[cursor]
		if !ok || value != nil {
			t.Fatalf("%s JSON = %#v, want explicit null", cursor, value)
		}
	}

	_, requestShape := marshalWorkflowJSON[map[string]any](t, WorkflowBoardNodeCardsListRequest{
		ProjectID:  "project-1",
		WorkflowID: "workflow-1",
		NodeID:     "node-1",
	})
	if value, ok := requestShape["page_token"]; !ok || value != nil {
		t.Fatalf("request page_token JSON = %#v, want explicit null", value)
	}
}

func TestWorkflowBoardNodeCardsRequestCapsPageSizeAt25(t *testing.T) {
	testValidWorkflowRequests(t, []workflowValidRequestCase{{name: "25 cards", request: WorkflowBoardNodeCardsListRequest{ProjectID: "project-1", WorkflowID: "workflow-1", NodeID: "node-1", PageSize: 25}}})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{{name: "26 cards", request: WorkflowBoardNodeCardsListRequest{ProjectID: "project-1", WorkflowID: "workflow-1", NodeID: "node-1", PageSize: 26}, field: "page_size", code: WorkflowRequestErrorInvalidMode}})
}

func TestWorkflowBoardRequestOptionalWorkflowSelectionJSONAndValidation(t *testing.T) {
	omitted := WorkflowBoardRequest{ProjectID: "project-1"}
	raw, omittedShape := marshalWorkflowJSON[map[string]any](t, omitted)
	if _, ok := omittedShape["workflow_id"]; ok {
		t.Fatalf("omitted workflow_id present in request JSON: %s", raw)
	}
	if err := omitted.Validate(); err != nil {
		t.Fatalf("omitted workflow selection rejected: %v", err)
	}

	for _, tt := range []struct {
		name  string
		value string
		code  string
	}{
		{name: "empty", value: "", code: WorkflowRequestErrorRequired},
		{name: "whitespace only", value: " \t", code: WorkflowRequestErrorRequired},
		{name: "leading whitespace", value: " workflow-1", code: WorkflowRequestErrorInvalidValue},
		{name: "trailing whitespace", value: "workflow-1 ", code: WorkflowRequestErrorInvalidValue},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := WorkflowBoardRequest{ProjectID: "project-1", WorkflowID: &tt.value}
			if err := request.Validate(); !isWorkflowFieldError(err, "workflow_id", tt.code) {
				t.Fatalf("validation error = %#v, want %s on workflow_id", err, tt.code)
			}
		})
	}

	workflowID := "workflow-1"
	selected := WorkflowBoardRequest{ProjectID: "project-1", WorkflowID: &workflowID}
	raw, selectedShape := marshalWorkflowJSON[map[string]any](t, selected)
	if selectedShape["workflow_id"] != workflowID {
		t.Fatalf("workflow_id = %#v, want %q", selectedShape["workflow_id"], workflowID)
	}
	if err := selected.Validate(); err != nil {
		t.Fatalf("selected workflow rejected: %v", err)
	}
}

func TestWorkflowBoardSelectedWorkflowJSONRepresentsAbsence(t *testing.T) {
	selection := &WorkflowPickerItem{
		WorkflowID:  "workflow-1",
		DisplayName: "Main",
		Version:     3,
	}
	selected := WorkflowBoard{ProjectID: "project-1", SelectedWorkflow: selection}
	raw, selectedShape := marshalWorkflowJSON[map[string]any](t, selected)
	selectedWorkflow, ok := selectedShape["selected_workflow"].(map[string]any)
	if !ok {
		t.Fatalf("selected_workflow = %#v, want object", selectedShape["selected_workflow"])
	}
	if selectedWorkflow["workflow_id"] != "workflow-1" {
		t.Fatalf("selected workflow_id = %#v, want workflow-1", selectedWorkflow["workflow_id"])
	}

	absent := WorkflowBoard{ProjectID: "project-1"}
	raw, absentShape := marshalWorkflowJSON[map[string]any](t, absent)
	if _, ok := absentShape["selected_workflow"]; ok {
		t.Fatalf("selected_workflow present in board without selection: %s", raw)
	}
}

func TestAskAnswerRequestValidatesNullableSelectedOption(t *testing.T) {
	base := AskAnswerRequest{ClientRequestID: "req-1", SessionID: "session-1", AskID: "ask-1"}
	freeform := base
	freeform.FreeformAnswer = "typed"
	if err := freeform.Validate(); err != nil {
		t.Fatalf("nil selected option plus freeform answer rejected: %v", err)
	}
	selected := 1
	withSelected := base
	withSelected.SelectedOptionNumber = &selected
	if err := withSelected.Validate(); err != nil {
		t.Fatalf("positive selected option rejected: %v", err)
	}
	zero := 0
	withZero := base
	withZero.SelectedOptionNumber = &zero
	if err := withZero.Validate(); err == nil {
		t.Fatal("present zero selected option accepted")
	}
	negative := -1
	withNegative := base
	withNegative.SelectedOptionNumber = &negative
	if err := withNegative.Validate(); err == nil {
		t.Fatal("present negative selected option accepted")
	}
	if err := base.Validate(); err == nil {
		t.Fatal("nil selected option without another answer accepted")
	}

	var legacy AskAnswerRequest
	if err := json.Unmarshal([]byte(`{"client_request_id":"req-1","session_id":"session-1","ask_id":"ask-1","freeform_answer":"typed"}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy omitted selection: %v", err)
	}
	if legacy.SelectedOptionNumber != nil {
		t.Fatalf("legacy omitted selected option = %v, want nil", *legacy.SelectedOptionNumber)
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
	_, ordinaryRaw := marshalWorkflowJSON[map[string]any](t, item)
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
	_, approvalRaw := marshalWorkflowJSON[map[string]any](t, approval)
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
	if !ok || !slices.Equal(decisions, wantDecisions) {
		t.Fatalf("approval decisions = %#v", approvalQuestion["approval_decisions"])
	}
}

func TestWorkflowTaskListRequestValidation(t *testing.T) {
	projectID := "project-1"
	workflowID := "workflow-1"
	valid := WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		PageSize:    WorkflowTaskListMaxPageSize,
		PageToken:   "token",
		ColumnKeys:  []string{"backlog", "plan"},
		StatusKinds: []WorkflowTaskStatusKind{WorkflowTaskStatusKindBacklog, WorkflowTaskStatusKindRunning, WorkflowTaskStatusKindQueued, WorkflowTaskStatusKindDone, WorkflowTaskStatusKindCanceled},
		AttentionKinds: []WorkflowTaskAttentionKind{
			WorkflowTaskAttentionKindQuestion,
			WorkflowTaskAttentionKindApproval,
			WorkflowTaskAttentionKindInterrupted,
		},
		Sort: []WorkflowTaskListSort{
			{Field: WorkflowTaskListSortFieldStatus, Direction: WorkflowTaskListSortDirectionAsc},
			{Field: WorkflowTaskListSortFieldColumn, Direction: WorkflowTaskListSortDirectionAsc},
			{Field: WorkflowTaskListSortFieldUpdated, Direction: WorkflowTaskListSortDirectionDesc},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid task list request rejected: %v", err)
	}
	validJSON, validShape := marshalWorkflowJSON[map[string]any](t, valid)
	for _, key := range []string{"project_id", "workflow_id", "column_keys", "status_kinds", "attention_kinds"} {
		if _, ok := validShape[key]; !ok {
			t.Fatalf("task list request JSON missing %s: %s", key, validJSON)
		}
	}
	for _, key := range []string{"status_keys", "run_statuses"} {
		if _, ok := validShape[key]; ok {
			t.Fatalf("task list request JSON unexpectedly contains %s: %s", key, validJSON)
		}
	}
	if err := (WorkflowTaskListRequest{ProjectID: &projectID}).Validate(); err != nil {
		t.Fatalf("request with default sort rejected: %v", err)
	}

	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "project scope required without continuation token", request: WorkflowTaskListRequest{}, field: "project_id", code: WorkflowRequestErrorRequired},
		{name: "workflow requires project on first page", request: WorkflowTaskListRequest{WorkflowID: &workflowID}, field: "project_id", code: WorkflowRequestErrorRequired},
		{name: "negative page size", request: WorkflowTaskListRequest{ProjectID: &projectID, PageSize: -1}, field: "page_size", code: WorkflowRequestErrorInvalidMode},
		{name: "oversized page size", request: WorkflowTaskListRequest{ProjectID: &projectID, PageSize: WorkflowTaskListMaxPageSize + 1}, field: "page_size", code: WorkflowRequestErrorInvalidMode},
		{name: "page token whitespace", request: WorkflowTaskListRequest{ProjectID: &projectID, PageToken: " token"}, field: "page_token", code: WorkflowRequestErrorInvalidMode},
		{name: "invalid sort field", request: WorkflowTaskListRequest{ProjectID: &projectID, Sort: []WorkflowTaskListSort{{Field: "priority", Direction: WorkflowTaskListSortDirectionAsc}}}, field: "sort[0].field", code: WorkflowRequestErrorInvalidValue},
		{name: "invalid sort direction", request: WorkflowTaskListRequest{ProjectID: &projectID, Sort: []WorkflowTaskListSort{{Field: WorkflowTaskListSortFieldCreated, Direction: "up"}}}, field: "sort[0].direction", code: WorkflowRequestErrorInvalidValue},
		{name: "duplicate sort field", request: WorkflowTaskListRequest{ProjectID: &projectID, Sort: []WorkflowTaskListSort{{Field: WorkflowTaskListSortFieldTitle, Direction: WorkflowTaskListSortDirectionAsc}, {Field: WorkflowTaskListSortFieldTitle, Direction: WorkflowTaskListSortDirectionDesc}}}, field: "sort[1].field", code: WorkflowRequestErrorInvalidValue},
		{name: "too many sort fields", request: WorkflowTaskListRequest{ProjectID: &projectID, Sort: []WorkflowTaskListSort{{Field: WorkflowTaskListSortFieldCreated, Direction: WorkflowTaskListSortDirectionAsc}, {Field: WorkflowTaskListSortFieldUpdated, Direction: WorkflowTaskListSortDirectionAsc}, {Field: WorkflowTaskListSortFieldStatus, Direction: WorkflowTaskListSortDirectionAsc}, {Field: WorkflowTaskListSortFieldColumn, Direction: WorkflowTaskListSortDirectionAsc}, {Field: WorkflowTaskListSortFieldRunCount, Direction: WorkflowTaskListSortDirectionAsc}, {Field: WorkflowTaskListSortFieldTitle, Direction: WorkflowTaskListSortDirectionAsc}}}, field: "sort", code: WorkflowRequestErrorInvalidValue},
		{name: "invalid task status", request: WorkflowTaskListRequest{ProjectID: &projectID, StatusKinds: []WorkflowTaskStatusKind{"waiting"}}, field: "status_kinds[0]", code: WorkflowRequestErrorInvalidValue},
		{name: "invalid attention kind", request: WorkflowTaskListRequest{ProjectID: &projectID, AttentionKinds: []WorkflowTaskAttentionKind{"waiting"}}, field: "attention_kinds[0]", code: WorkflowRequestErrorInvalidValue},
		{name: "blank column key", request: WorkflowTaskListRequest{ProjectID: &projectID, ColumnKeys: []string{" "}}, field: "column_keys[0]", code: WorkflowRequestErrorInvalidKey},
		{name: "invalid column key syntax", request: WorkflowTaskListRequest{ProjectID: &projectID, ColumnKeys: []string{"Plan"}}, field: "column_keys[0]", code: WorkflowRequestErrorInvalidKey},
	})
}

func TestWorkflowTaskListResponseJSONShape(t *testing.T) {
	selectedColumnKeys := []string{"plan", "qa"}
	selected := WorkflowTaskListResponse{
		Scope: WorkflowTaskListScope{
			ProjectID:  "project-1",
			WorkflowID: stringPointerForTest("workflow-1"),
		},
		MatchingWorkflowCardinality: WorkflowTaskListMatchingWorkflowCardinalityOne,
		NextPageToken:               "next",
		GeneratedAtUnixMs:           10,
		Tasks: []WorkflowTaskListItem{{
			TaskID:          "task-1",
			ShortID:         "BLD-1",
			WorkflowID:      "workflow-1",
			Title:           "Task",
			CreatedAtUnixMs: 11,
			UpdatedAtUnixMs: 12,
			ColumnKeys:      &selectedColumnKeys,
			Status:          WorkflowTaskStatus{Kind: WorkflowTaskStatusKindQueued, NativeState: "active", NodeIDs: []string{"node-1"}, RunIDs: []string{"run-1"}, AttentionTypes: []WorkflowTaskAttentionKind{WorkflowTaskAttentionKindApproval}},
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
	scope, ok := selectedShape["scope"].(map[string]any)
	if !ok || scope["project_id"] != "project-1" || scope["workflow_id"] != "workflow-1" {
		t.Fatalf("scope = %#v, want project and selected workflow", selectedShape["scope"])
	}
	if selectedShape["matching_workflow_cardinality"] != string(WorkflowTaskListMatchingWorkflowCardinalityOne) {
		t.Fatalf("matching workflow cardinality = %#v", selectedShape["matching_workflow_cardinality"])
	}
	tasks, ok := selectedShape["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks shape = %#v, want one task", selectedShape["tasks"])
	}
	task, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("task shape = %#v, want object", tasks[0])
	}
	for _, key := range []string{"column_keys", "status", "created_at_unix_ms", "updated_at_unix_ms", "run_count"} {
		if _, ok := task[key]; !ok {
			t.Fatalf("task JSON missing %s: %s", key, raw)
		}
	}
	for _, key := range []string{"status_keys", "run_status", "run_statuses"} {
		if _, ok := task[key]; ok {
			t.Fatalf("task JSON unexpectedly contains %s: %s", key, raw)
		}
	}
	status, ok := task["status"].(map[string]any)
	if !ok {
		t.Fatalf("task status shape = %#v, want object", task["status"])
	}
	if _, ok := status["label"]; ok {
		t.Fatalf("task status JSON unexpectedly contains label: %s", raw)
	}

	empty := WorkflowTaskListResponse{
		Scope:                       WorkflowTaskListScope{ProjectID: "project-1"},
		MatchingWorkflowCardinality: WorkflowTaskListMatchingWorkflowCardinalityNone,
		Tasks:                       []WorkflowTaskListItem{},
	}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty response: %v", err)
	}
	var emptyShape map[string]any
	if err := json.Unmarshal(raw, &emptyShape); err != nil {
		t.Fatalf("unmarshal empty response: %v", err)
	}
	emptyScope, ok := emptyShape["scope"].(map[string]any)
	if !ok || emptyScope["project_id"] != "project-1" {
		t.Fatalf("empty scope = %#v, want project-only scope", emptyShape["scope"])
	}
	if _, ok := emptyScope["workflow_id"]; ok {
		t.Fatalf("workflow_id present in project-only scope JSON: %s", raw)
	}
	projectWide := WorkflowTaskListResponse{
		Scope:                       WorkflowTaskListScope{ProjectID: "project-1"},
		MatchingWorkflowCardinality: WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		Tasks:                       []WorkflowTaskListItem{{TaskID: "task-1", WorkflowID: "workflow-1"}},
	}
	raw, err = json.Marshal(projectWide)
	if err != nil {
		t.Fatalf("marshal project-wide response: %v", err)
	}
	var projectWideShape map[string]any
	if err := json.Unmarshal(raw, &projectWideShape); err != nil {
		t.Fatalf("unmarshal project-wide response: %v", err)
	}
	tasks, ok = projectWideShape["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("project-wide tasks shape = %#v", projectWideShape["tasks"])
	}
	projectWideTask, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("project-wide task shape = %#v", tasks[0])
	}
	if _, ok := projectWideTask["column_keys"]; ok {
		t.Fatalf("project-wide task unexpectedly contains column_keys: %s", raw)
	}
	emptyColumns := []string{}
	narrowed := WorkflowTaskListResponse{
		Scope:                       WorkflowTaskListScope{ProjectID: "project-1", WorkflowID: stringPointerForTest("workflow-1")},
		MatchingWorkflowCardinality: WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks:                       []WorkflowTaskListItem{{TaskID: "task-1", WorkflowID: "workflow-1", ColumnKeys: &emptyColumns}},
	}
	raw, err = json.Marshal(narrowed)
	if err != nil {
		t.Fatalf("marshal narrowed response: %v", err)
	}
	var narrowedShape map[string]any
	if err := json.Unmarshal(raw, &narrowedShape); err != nil {
		t.Fatalf("unmarshal narrowed response: %v", err)
	}
	narrowedTasks, ok := narrowedShape["tasks"].([]any)
	if !ok || len(narrowedTasks) != 1 {
		t.Fatalf("narrowed tasks shape = %#v", narrowedShape["tasks"])
	}
	narrowedTask, ok := narrowedTasks[0].(map[string]any)
	if !ok {
		t.Fatalf("narrowed task shape = %#v", narrowedTasks[0])
	}
	if columnKeys, present := narrowedTask["column_keys"]; !present || !equalJSONArrays(columnKeys.([]any), []any{}) {
		t.Fatalf("narrowed empty column_keys = %#v, want present empty array", narrowedTask["column_keys"])
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

func TestWorkflowLifecycleJSONOmitsAbsentFacts(t *testing.T) {
	payload := struct {
		Summary    WorkflowTaskSummary    `json:"summary"`
		Run        WorkflowRun            `json:"run"`
		Transition WorkflowTaskTransition `json:"transition"`
	}{
		Summary: WorkflowTaskSummary{
			ID:         "task-1",
			ProjectID:  "project-1",
			WorkflowID: "workflow-1",
			ShortID:    "KT-1",
			Title:      "Task",
		},
		Run: WorkflowRun{
			ID:          "run-1",
			TaskID:      "task-1",
			PlacementID: "placement-1",
			NodeID:      "node-1",
			Status:      "queued",
		},
		Transition: WorkflowTaskTransition{
			ID:        "transition-1",
			TaskID:    "task-1",
			CreatedAt: 1,
		},
	}

	data, raw := marshalWorkflowJSON[map[string]map[string]any](t, payload)
	if _, ok := raw["summary"]["cancel_reason"]; ok {
		t.Fatalf("absent task cancellation reason serialized: %s", data)
	}
	for _, key := range []string{
		"started_at_unix_ms",
		"completed_at_unix_ms",
		"interrupted_at_unix_ms",
		"interruption_reason",
	} {
		if value, ok := raw["run"][key]; !ok || value != nil {
			t.Fatalf("absent run lifecycle fact %q serialized as %v in %s, want null", key, value, data)
		}
	}
	for _, key := range []string{
		"waiting_ask_id",
	} {
		if _, ok := raw["run"][key]; ok {
			t.Fatalf("absent run lifecycle fact %q serialized: %s", key, data)
		}
	}
	if _, ok := raw["transition"]["applied_at_unix_ms"]; ok {
		t.Fatalf("absent transition applied fact serialized: %s", data)
	}
}

func TestWorkflowTaskListScopeErrorRoundTrip(t *testing.T) {
	canonicalWorkflowID := "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	for _, original := range []*WorkflowTaskListScopeError{
		{
			Reason:    WorkflowTaskListScopeReasonNoLinkedWorkflows,
			ProjectID: stringPointerForTest("project-1"),
		},
		{
			Reason:     WorkflowTaskListScopeReasonWorkflowNotLinked,
			ProjectID:  stringPointerForTest("project-1"),
			WorkflowID: &canonicalWorkflowID,
		},
		{
			Reason:    WorkflowTaskListScopeReasonWorkflowRequiredColumns,
			ProjectID: stringPointerForTest("project-1"),
		},
	} {
		decoded := DecodeWorkflowTaskListScopeError(original.RPCErrorData(), original.Error())
		var scopeErr *WorkflowTaskListScopeError
		if !errors.As(decoded, &scopeErr) {
			t.Fatalf("decoded scope error = %T %v, want WorkflowTaskListScopeError", decoded, decoded)
		}
		if scopeErr.Reason != original.Reason || !textutil.EqualOptional(scopeErr.ProjectID, original.ProjectID) || !textutil.EqualOptional(scopeErr.WorkflowID, original.WorkflowID) {
			t.Fatalf("decoded scope error = %+v, want %+v", scopeErr, original)
		}
		if scopeErr.RPCErrorCode() != protocol.ErrCodeWorkflowTaskListScope {
			t.Fatalf("scope error code = %d, want %d", scopeErr.RPCErrorCode(), protocol.ErrCodeWorkflowTaskListScope)
		}
	}
	if decoded := DecodeWorkflowTaskListScopeError(
		json.RawMessage(`{"type":"workflow_task_list_scope_error","kind":"ambiguous","workflow_ids":["workflow-1","workflow-2"]}`),
		"legacy scope error",
	); decoded == nil || decoded.Error() != "legacy scope error" {
		t.Fatalf("legacy ambiguity payload decoded as %#v, want fallback error", decoded)
	}
	for name, payload := range map[string]string{
		"no links missing project":        `{"type":"workflow_task_list_scope_error","reason":"no_linked_workflows"}`,
		"no links with workflow":          `{"type":"workflow_task_list_scope_error","reason":"no_linked_workflows","project_id":"project-1","workflow_id":"` + canonicalWorkflowID + `"}`,
		"not linked missing workflow":     `{"type":"workflow_task_list_scope_error","reason":"workflow_not_linked","project_id":"project-1"}`,
		"columns with workflow":           `{"type":"workflow_task_list_scope_error","reason":"workflow_required_for_columns","project_id":"project-1","workflow_id":"` + canonicalWorkflowID + `"}`,
		"padded project":                  `{"type":"workflow_task_list_scope_error","reason":"no_linked_workflows","project_id":" project-1"}`,
		"malformed persisted workflow id": `{"type":"workflow_task_list_scope_error","reason":"workflow_not_linked","project_id":"project-1","workflow_id":"workflow-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			decoded := DecodeWorkflowTaskListScopeError(json.RawMessage(payload), "fallback")
			var scopeErr *WorkflowTaskListScopeError
			if errors.As(decoded, &scopeErr) {
				t.Fatalf("invalid payload decoded as typed scope error: %+v", scopeErr)
			}
		})
	}
}

func TestWorkflowTaskCreateSelectionErrorRoundTrip(t *testing.T) {
	original := &WorkflowTaskCreateSelectionError{
		Reason:    WorkflowTaskCreateSelectionReasonNoLinkedWorkflows,
		ProjectID: "project-1",
	}
	decoded := DecodeWorkflowTaskCreateSelectionError(original.RPCErrorData(), original.Error())
	var selectionErr *WorkflowTaskCreateSelectionError
	if !errors.As(decoded, &selectionErr) {
		t.Fatalf("decoded selection error = %T %v, want WorkflowTaskCreateSelectionError", decoded, decoded)
	}
	if selectionErr.Reason != original.Reason ||
		selectionErr.ProjectID != original.ProjectID ||
		selectionErr.WorkflowID != nil {
		t.Fatalf("decoded selection error = %+v, want %+v", selectionErr, original)
	}
	if selectionErr.RPCErrorCode() != protocol.ErrCodeWorkflowTaskCreateSelection {
		t.Fatalf("selection error code = %d, want %d", selectionErr.RPCErrorCode(), protocol.ErrCodeWorkflowTaskCreateSelection)
	}
}

func TestWorkflowTaskCreateConflictErrorRoundTrip(t *testing.T) {
	original := &WorkflowTaskCreateConflictError{
		Reason: WorkflowTaskCreateConflictReasonSerialization,
	}
	decoded := DecodeWorkflowTaskCreateConflictError(original.RPCErrorData(), original.Error())
	var conflictErr *WorkflowTaskCreateConflictError
	if !errors.As(decoded, &conflictErr) {
		t.Fatalf("decoded conflict error = %T %v, want WorkflowTaskCreateConflictError", decoded, decoded)
	}
	if conflictErr.Reason != original.Reason || conflictErr.RPCErrorCode() != protocol.ErrCodeWorkflowTaskCreateConflict {
		t.Fatalf("decoded conflict error = %+v, want %+v", conflictErr, original)
	}
}

func stringPointerForTest(value string) *string {
	return &value
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
	testValidWorkflowRequests(t, []workflowValidRequestCase{{name: "empty diagnostic path", request: WorkflowScriptPathValidateRequest{WorkflowID: "workflow-1", NodeID: "node-script"}}})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "requires workflow", request: WorkflowScriptPathValidateRequest{NodeID: "node-script"}, field: "workflow_id", code: WorkflowRequestErrorRequired},
		{name: "requires node", request: WorkflowScriptPathValidateRequest{WorkflowID: "workflow-1"}, field: "node_id", code: WorkflowRequestErrorRequired},
	})
}

func TestWorkflowGraphDraftRequestValidation(t *testing.T) {
	scriptPath := "scripts/run"
	testValidWorkflowRequests(t, []workflowValidRequestCase{
		{name: "semantic graph shape passes request validation", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Graph: WorkflowGraphDraft{Nodes: []WorkflowGraphDraftNode{{ID: "node-1", Key: "Bad-Key", Kind: "unknown"}}}, Modes: []WorkflowValidationMode{WorkflowValidationModeDraft, WorkflowValidationModeExecution}}},
		{name: "empty graph", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{WorkflowValidationModeDraft, WorkflowValidationModeExecution}}},
		{name: "draft metadata", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Metadata: &WorkflowGraphMetadata{Name: "Draft Name", Description: "Draft description"}, Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}}},
		{name: "previous target or new context source", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}, Graph: WorkflowGraphDraft{Edges: []WorkflowGraphDraftEdge{{ID: "edge-1", ContextSource: WorkflowContextSource{Kind: "previous_target_or_new"}}}}}},
		{name: "metadata preview", request: WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Metadata: &WorkflowGraphMetadata{Name: "Draft Name"}}},
	})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "requires workflow", request: WorkflowGraphValidateDraftRequest{Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}}, field: "workflow_id", code: WorkflowRequestErrorRequired},
		{name: "requires modes", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1"}, field: "modes", code: WorkflowRequestErrorRequired},
		{name: "rejects mode", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{"other"}}, field: "modes", code: WorkflowRequestErrorInvalidMode},
		{name: "caps nodes", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}, Graph: WorkflowGraphDraft{Nodes: make([]WorkflowGraphDraftNode, WorkflowGraphDraftMaxNodes+1)}}, field: "graph.nodes", code: WorkflowRequestErrorTooLong},
		{name: "rejects completion mode", request: WorkflowGraphValidateDraftRequest{WorkflowID: "workflow-1", Modes: []WorkflowValidationMode{WorkflowValidationModeDraft}, Graph: WorkflowGraphDraft{Nodes: []WorkflowGraphDraftNode{{ID: "node-1", Kind: "agent", CompletionMode: "invalid"}}}}, field: "graph.nodes.completion_mode", code: WorkflowRequestErrorInvalidValue},
		{name: "rejects script path on agent", request: WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Graph: WorkflowGraphDraft{Nodes: []WorkflowGraphDraftNode{{ID: "node-1", Kind: "agent", ScriptPath: &scriptPath}}}}, field: "graph.nodes.script_path", code: WorkflowRequestErrorInvalidValue},
		{name: "rejects context source kind", request: WorkflowGraphSaveRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Graph: WorkflowGraphDraft{Edges: []WorkflowGraphDraftEdge{{ID: "edge-1", ContextSource: WorkflowContextSource{Kind: "other"}}}}}, field: "context_source.kind", code: WorkflowRequestErrorInvalidValue},
		{name: "rejects previous target node key", request: WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Graph: WorkflowGraphDraft{Edges: []WorkflowGraphDraftEdge{{ID: "edge-1", ContextSource: WorkflowContextSource{Kind: "previous_target_or_new", NodeKey: "implement"}}}}}, field: "context_source.node_key", code: WorkflowRequestErrorInvalidValue},
		{name: "rejects negative version", request: WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: -1}, field: "expected_version", code: WorkflowRequestErrorInvalidValue},
		{name: "rejects untrimmed metadata name", request: WorkflowGraphSavePreviewRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Metadata: &WorkflowGraphMetadata{Name: " Draft Name "}}, field: "metadata.name", code: WorkflowRequestErrorInvalidValue},
		{name: "rejects negative confirmation", request: WorkflowGraphSaveRequest{WorkflowID: "workflow-1", ExpectedVersion: 1, Confirmation: &WorkflowGraphSaveConfirmation{ExpectedRemovedNodeCount: -1}}, field: "expected_removed_node_count", code: WorkflowRequestErrorInvalidValue},
	})
}

func TestWorkflowProjectLinkRequestValidation(t *testing.T) {
	testValidWorkflowRequests(t, []workflowValidRequestCase{
		{name: "link", request: WorkflowLinkProjectRequest{ProjectID: "project-1", WorkflowID: "workflow-1"}},
		{name: "link default if absent", request: WorkflowLinkProjectRequest{ProjectID: "project-1", WorkflowID: "workflow-1", DefaultPolicy: WorkflowProjectLinkDefaultIfProjectHasNone}},
		{name: "create and link", request: WorkflowCreateAndLinkProjectRequest{Name: "Workflow", ProjectID: "project-1", DefaultPolicy: WorkflowProjectLinkDefaultIfProjectHasNone}},
		{name: "list links", request: WorkflowListProjectLinksRequest{ProjectID: "project-1"}},
		{name: "list workflows", request: WorkflowListRequest{PageSize: 20, PageToken: "10", Query: "agent"}},
		{name: "set default", request: WorkflowSetDefaultProjectLinkRequest{ProjectID: "project-1", WorkflowID: "workflow-1"}},
	})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "link rejects default policy", request: WorkflowLinkProjectRequest{ProjectID: "project-1", WorkflowID: "workflow-1", DefaultPolicy: "sometimes"}, field: "default_policy", code: WorkflowRequestErrorInvalidMode},
		{name: "list rejects negative page size", request: WorkflowListRequest{PageSize: -1}, field: "page_size", code: WorkflowRequestErrorInvalidMode},
		{name: "list rejects oversized page size", request: WorkflowListRequest{PageSize: WorkflowListMaxPageSize + 1}, field: "page_size", code: WorkflowRequestErrorInvalidMode},
		{name: "list rejects malformed token", request: WorkflowListRequest{PageToken: " 10"}, field: "page_token", code: WorkflowRequestErrorInvalidMode},
		{name: "set default requires project", request: WorkflowSetDefaultProjectLinkRequest{WorkflowID: "workflow-1"}, field: "project_id", code: WorkflowRequestErrorRequired},
	})
	projectID := "project-1"
	workflowID := "workflow-1"
	if err := (WorkflowListRequest{ProjectID: &projectID, WorkflowID: &workflowID}).Validate(); err != nil {
		t.Fatalf("valid project workflow filters rejected: %v", err)
	}
	blank := " "
	if err := (WorkflowListRequest{ProjectID: &blank}).Validate(); !isWorkflowFieldError(err, "project_id", WorkflowRequestErrorRequired) {
		t.Fatalf("blank project filter error = %#v, want required on project_id", err)
	}
	if err := (WorkflowListRequest{WorkflowID: &blank}).Validate(); !isWorkflowFieldError(err, "workflow_id", WorkflowRequestErrorRequired) {
		t.Fatalf("blank workflow filter error = %#v, want required on workflow_id", err)
	}
	if err := (WorkflowListRequest{WorkflowID: &workflowID}).Validate(); err != nil {
		t.Fatalf("global exact workflow filter rejected: %v", err)
	}
	if err := (WorkflowSetDefaultProjectLinkRequest{ProjectID: "project-1", WorkflowID: "workflow-1"}).Validate(); err != nil {
		t.Fatalf("valid set default request rejected: %v", err)
	}
	if err := (WorkflowSetDefaultProjectLinkRequest{ProjectID: "", WorkflowID: "workflow-1"}).Validate(); !isWorkflowFieldError(err, "project_id", WorkflowRequestErrorRequired) {
		t.Fatalf("empty project id error = %#v, want required on project_id", err)
	}
}

func TestWorkflowListProjectContextJSONIsOptionalAndTyped(t *testing.T) {
	projectID := "project-1"
	workflowID := "workflow-1"
	request := WorkflowListRequest{ProjectID: &projectID, WorkflowID: &workflowID}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal workflow list request: %v", err)
	}
	var requestShape map[string]any
	if err := json.Unmarshal(data, &requestShape); err != nil {
		t.Fatalf("decode workflow list request: %v", err)
	}
	if requestShape["project_id"] != projectID || requestShape["workflow_id"] != workflowID {
		t.Fatalf("request JSON = %#v, want typed project/workflow filters", requestShape)
	}

	response := WorkflowListResponse{
		ProjectID: &projectID,
		Workflows: []WorkflowRecord{{
			ID:          workflowID,
			Name:        "Workflow",
			ProjectLink: &WorkflowListProjectLink{Default: true},
		}},
	}
	data, err = json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal workflow list response: %v", err)
	}
	var responseShape map[string]any
	if err := json.Unmarshal(data, &responseShape); err != nil {
		t.Fatalf("decode workflow list response: %v", err)
	}
	if responseShape["project_id"] != projectID {
		t.Fatalf("response JSON = %#v, want project context", responseShape)
	}
	workflows := responseShape["workflows"].([]any)
	record := workflows[0].(map[string]any)
	link := record["project_link"].(map[string]any)
	if link["default"] != true {
		t.Fatalf("project link JSON = %#v, want default=true", link)
	}

	globalData, err := json.Marshal(WorkflowListResponse{Workflows: []WorkflowRecord{{ID: workflowID, Name: "Workflow"}}})
	if err != nil {
		t.Fatalf("marshal global response: %v", err)
	}
	var globalShape map[string]any
	if err := json.Unmarshal(globalData, &globalShape); err != nil {
		t.Fatalf("decode global response: %v", err)
	}
	if _, exists := globalShape["project_id"]; exists {
		t.Fatalf("global response JSON = %#v, must omit project_id", globalShape)
	}
	globalRecords := globalShape["workflows"].([]any)
	if _, exists := globalRecords[0].(map[string]any)["project_link"]; exists {
		t.Fatalf("global response JSON = %#v, must omit project_link", globalShape)
	}
}

func TestWorkflowTaskStatusKindNativeState(t *testing.T) {
	cases := []struct {
		kind WorkflowTaskStatusKind
		want WorkflowTaskNativeState
	}{
		{WorkflowTaskStatusKindCanceled, WorkflowTaskNativeStateCanceled},
		{WorkflowTaskStatusKindDone, WorkflowTaskNativeStateTerminal},
		{WorkflowTaskStatusKindWaitingQuestion, WorkflowTaskNativeStateWaitingAsk},
		{WorkflowTaskStatusKindWaitingApproval, WorkflowTaskNativeStateWaitingApproval},
		{WorkflowTaskStatusKindInterrupted, WorkflowTaskNativeStateInterrupted},
		{WorkflowTaskStatusKindRunning, WorkflowTaskNativeStateRunning},
		{WorkflowTaskStatusKindQueued, WorkflowTaskNativeStateQueued},
		{WorkflowTaskStatusKindBacklog, WorkflowTaskNativeStateActive},
		{WorkflowTaskStatusKindActive, WorkflowTaskNativeStateActive},
	}
	for _, tt := range cases {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got, valid := tt.kind.NativeState(); !valid || got != tt.want {
				t.Fatalf("%q NativeState() = (%q, %t), want (%q, true)", tt.kind, got, valid, tt.want)
			}
		})
	}
	if nativeState, valid := WorkflowTaskStatusKind("invalid").NativeState(); valid || nativeState != "" {
		t.Fatalf("invalid NativeState() = (%q, %t), want (empty, false)", nativeState, valid)
	}
}

func TestWorkflowDeleteRequestValidation(t *testing.T) {
	testValidWorkflowRequests(t, []workflowValidRequestCase{
		{name: "preview", request: WorkflowDeletePreviewRequest{WorkflowID: "workflow-1"}},
		{name: "delete", request: WorkflowDeleteRequest{WorkflowID: "workflow-1", Confirmed: true, ExpectedVersion: 1, ExpectedProjectCount: 1, ExpectedLinkCount: 1, ExpectedTaskCount: 1}},
	})
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "preview requires workflow", request: WorkflowDeletePreviewRequest{}, field: "workflow_id", code: WorkflowRequestErrorRequired},
		{name: "delete requires workflow", request: WorkflowDeleteRequest{}, field: "workflow_id", code: WorkflowRequestErrorRequired},
		{name: "delete rejects negative version", request: WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedVersion: -1}, field: "expected_version", code: WorkflowRequestErrorInvalidMode},
		{name: "delete rejects negative project count", request: WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedProjectCount: -1}, field: "expected_project_count", code: WorkflowRequestErrorInvalidMode},
		{name: "delete rejects negative link count", request: WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedLinkCount: -1}, field: "expected_link_count", code: WorkflowRequestErrorInvalidMode},
		{name: "delete rejects negative task count", request: WorkflowDeleteRequest{WorkflowID: "workflow-1", ExpectedTaskCount: -1}, field: "expected_task_count", code: WorkflowRequestErrorInvalidMode},
	})
}
