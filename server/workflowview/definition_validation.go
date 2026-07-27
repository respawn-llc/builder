package workflowview

import (
	"core/server/workflow"
	"core/server/workflowvalidation"
)

func definitionExecutionValidation(def workflow.Definition, roleResolver workflow.RoleResolver) *workflow.ValidationResult {
	result := workflowvalidation.EvaluateDefinition(
		def,
		[]workflow.ValidationContext{workflow.ValidationContextExecution},
		roleResolver,
		nil,
	)[workflow.ValidationContextExecution]
	return &result
}
