package workflowview

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestValidationErrorsIncludesStructuredDetails(t *testing.T) {
	role := "reviewer"
	requiredTool := toolspec.ToolAskQuestion
	errors := ValidationErrors("workflow-1", []workflow.ValidationError{{
		Code:           workflow.CodeInvalidTemplatePlaceholder,
		Message:        "prompt template references an unknown node input",
		NodeID:         "node-1",
		InputName:      "summary",
		Placeholder:    ".Inputs.summary",
		ProviderEdgeID: "edge-provider",
		FieldName:      "summary",
		AgentRole:      &role,
		RequiredTool:   &requiredTool,
	}})

	if len(errors) != 1 || errors[0].Details == nil {
		t.Fatalf("errors = %+v, want structured details", errors)
	}
	details := errors[0].Details
	if details.FieldName != "summary" || details.InputName != "summary" || details.Placeholder != ".Inputs.summary" || details.ProviderEdgeID != "edge-provider" ||
		details.Role == nil || *details.Role != role || details.RequiredTool == nil || *details.RequiredTool != string(requiredTool) {
		t.Fatalf("details = %+v", details)
	}
}

func TestValidationErrorsPreservesAbsentWorkflowIdentity(t *testing.T) {
	errors := ValidationErrors("", []workflow.ValidationError{{
		Code:    workflow.CodeInvalidTemplatePlaceholder,
		Message: "invalid",
	}})

	if len(errors) != 1 || errors[0].WorkflowID != nil {
		t.Fatalf("errors = %+v, want absent workflow identity", errors)
	}
}

func TestFocusedReadModelsRejectInvalidRequests(t *testing.T) {
	store, workflowStore, _ := newWorkflowViewTestStore(t)
	fixture, err := newWorkflowViewTestFixture(store, workflowStore, nil, nil)
	if err != nil {
		t.Fatalf("newWorkflowViewTestFixture: %v", err)
	}
	if _, err := fixture.board(t).Get(context.Background(), serverapi.WorkflowBoardRequest{ProjectID: " "}); !isWorkflowRequestValidationField(err, "project_id") {
		t.Fatalf("Get board missing id error = %v", err)
	}
	if _, err := fixture.board(t).ListNodeCards(context.Background(), serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: "project-1", WorkflowID: "workflow-1", NodeID: "node-1", PageSize: -1}); !isWorkflowRequestValidationField(err, "page_size") {
		t.Fatalf("List board node cards negative page size error = %v", err)
	}
	if _, err := fixture.detail(t).GetTask(context.Background(), " "); !errors.Is(err, ErrTaskIDRequired) {
		t.Fatalf("Get task missing id error = %v", err)
	}
}

func isWorkflowRequestValidationField(err error, field string) bool {
	var validationErr serverapi.WorkflowRequestValidationError
	return errors.As(err, &validationErr) && validationErr.Field == field
}
