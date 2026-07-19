package workflowview

import (
	"core/server/workflow"
	"core/server/workflowscript"
)

func definitionExecutionValidation(def workflow.Definition, roleResolver workflow.RoleResolver) *workflow.ValidationResult {
	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextExecution,
		RoleResolver: roleResolver,
	})
	result.Errors = append(result.Errors, scriptPathDefinitionValidationErrors(def, nil)...)
	return &result
}

func scriptPathDefinitionValidationErrors(def workflow.Definition, rootPath *string) []workflow.ValidationError {
	out := []workflow.ValidationError{}
	for _, node := range def.Nodes {
		if node.Kind() != workflow.NodeKindScript {
			continue
		}
		diagnostics := workflowscript.Validate(workflowscript.ValidationRequest{
			RawPath:  workflow.NodeScriptPath(node).String(),
			RootPath: rootPath,
		})
		for _, diagnostic := range diagnostics {
			out = append(out, workflow.ValidationError{
				Code:          workflow.ValidationErrorCode(diagnostic.Code),
				Message:       diagnostic.Message,
				WorkflowID:    def.ID,
				NodeID:        workflow.NodeIDOf(node),
				BlocksContext: diagnostic.Blocking,
			})
		}
	}
	return out
}
