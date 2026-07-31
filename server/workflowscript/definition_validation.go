package workflowscript

import "core/server/workflow"

func EvaluateDefinition(def workflow.Definition, contexts []workflow.ValidationContext, resolver workflow.RoleResolver, scriptRoot *string) map[workflow.ValidationContext]workflow.ValidationResult {
	results := make(map[workflow.ValidationContext]workflow.ValidationResult, len(contexts))
	for _, context := range contexts {
		if _, exists := results[context]; exists {
			continue
		}
		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: context, RoleResolver: resolver})
		if context == workflow.ValidationContextExecution {
			result.Errors = append(result.Errors, definitionScriptPathErrors(def, scriptRoot)...)
		}
		results[context] = result
	}
	return results
}

func definitionScriptPathErrors(def workflow.Definition, rootPath *string) []workflow.ValidationError {
	var errors []workflow.ValidationError
	for _, node := range def.Nodes {
		if node.Kind() != workflow.NodeKindScript {
			continue
		}
		for _, diagnostic := range Validate(ValidationRequest{
			RawPath:  workflow.NodeScriptPath(node).String(),
			RootPath: rootPath,
		}) {
			errors = append(errors, workflow.ValidationError{
				Code:          workflow.ValidationErrorCode(diagnostic.Code),
				Message:       diagnostic.Message,
				WorkflowID:    workflow.OptionalWorkflowID(def.ID),
				NodeID:        workflow.NodeIDOf(node),
				BlocksContext: diagnostic.Blocking,
			})
		}
	}
	return errors
}
