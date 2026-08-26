package workflow_test

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/workflowcontract"
)

func TestDefinitionRejectsInvalidExecutionTargetPolicies(t *testing.T) {
	customRef := "refs/tags/v1"
	for _, policy := range []workflow.ExecutionTargetPolicy{
		{Mode: workflowcontract.ExecutionTargetModeHead, CustomRef: &customRef},
		{Mode: workflowcontract.ExecutionTargetMode("future")},
	} {
		def := parameterWorkflow(t)
		def.ExecutionTargetPolicy = policy
		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
			Context:      workflow.ValidationContextDraft,
			RoleResolver: testsetup.QuestionsEnabled("coder"),
		})
		if !hasValidationCode(result, workflow.CodeInvalidExecutionTargetPolicy) {
			t.Fatalf("policy %+v validation codes = %v, want invalid policy", policy, result.Codes())
		}
	}
}

func TestDefinitionReportsCustomRefDraftIssueWithoutBlockingDraft(t *testing.T) {
	def := parameterWorkflow(t)
	def.ExecutionTargetPolicy = workflow.ExecutionTargetPolicy{Mode: workflowcontract.ExecutionTargetModeCustomRef}

	draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: testsetup.QuestionsEnabled("coder")})
	if !hasValidationCode(draft, workflow.CodeExecutionTargetCustomRefRequired) {
		t.Fatalf("draft validation codes = %v, want custom-ref requirement", draft.Codes())
	}
	if draft.HasBlockingErrors() {
		t.Fatalf("draft validation unexpectedly blocks: %+v", draft.BlockingErrors())
	}

	execution := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextExecution, RoleResolver: testsetup.QuestionsEnabled("coder")})
	if !hasValidationCode(execution, workflow.CodeExecutionTargetCustomRefRequired) || !execution.HasBlockingErrors() {
		t.Fatalf("execution validation = %+v, want blocking custom-ref requirement", execution)
	}
}

func hasValidationCode(result workflow.ValidationResult, want workflow.ValidationErrorCode) bool {
	for _, code := range result.Codes() {
		if code == want {
			return true
		}
	}
	return false
}
