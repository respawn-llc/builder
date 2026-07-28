package workflowview

import (
	"core/server/workflow"
)

func definitionExecutionValidation(def workflow.Definition, roleResolver workflow.RoleResolver) *workflow.ValidationResult {
	result := workflow.EvaluateDefinition(
		def,
		[]workflow.ValidationContext{workflow.ValidationContextExecution},
		roleResolver,
		nil,
	)[workflow.ValidationContextExecution]
	return &result
}
