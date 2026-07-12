package workflow_test

import (
	"testing"

	"core/server/workflow"
)

func TestDefinitionRejectsInvalidExecutionTargetPolicies(t *testing.T) {
	customRef := "refs/tags/v1"
	for _, policy := range []workflow.ExecutionTargetPolicy{
		{Mode: workflow.ExecutionTargetModeHead, CustomRef: &customRef},
		{Mode: workflow.ExecutionTargetMode("future")},
	} {
		def := parameterWorkflow()
		def.ExecutionTargetPolicy = policy
		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
			Context:      workflow.ValidationContextDraft,
			RoleResolver: workflow.StaticRoleResolver{"coder": true},
		})
		if !hasValidationCode(result, workflow.CodeInvalidExecutionTargetPolicy) {
			t.Fatalf("policy %+v validation codes = %v, want invalid policy", policy, result.Codes())
		}
	}
}

func TestExecutionTargetSelectionValidation(t *testing.T) {
	customRef := "release/v1"
	blankRef := " "

	tests := []struct {
		name      string
		selection workflow.ExecutionTargetSelection
		wantErr   bool
	}{
		{name: "none", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeNone}},
		{name: "head", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeHead}},
		{name: "default branch", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeDefaultBranch}},
		{name: "custom ref", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeCustomRef, CustomRef: &customRef}},
		{name: "ask on first execution cannot be selected", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeAskOnFirstExecution}, wantErr: true},
		{name: "custom selection requires ref", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeCustomRef}, wantErr: true},
		{name: "custom selection rejects blank ref", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeCustomRef, CustomRef: &blankRef}, wantErr: true},
		{name: "concrete non custom selection rejects ref", selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeHead, CustomRef: &customRef}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.selection.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestDefinitionReportsCustomRefDraftIssueWithoutBlockingDraft(t *testing.T) {
	def := parameterWorkflow()
	def.ExecutionTargetPolicy = workflow.ExecutionTargetPolicy{Mode: workflow.ExecutionTargetModeCustomRef}

	draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})
	if !hasValidationCode(draft, workflow.CodeExecutionTargetCustomRefRequired) {
		t.Fatalf("draft validation codes = %v, want custom-ref requirement", draft.Codes())
	}
	if draft.HasBlockingErrors() {
		t.Fatalf("draft validation unexpectedly blocks: %+v", draft.BlockingErrors())
	}

	execution := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextExecution, RoleResolver: workflow.StaticRoleResolver{"coder": true}})
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
