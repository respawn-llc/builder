package workflow

import "core/server/workflowscript"

func EvaluateDefinition(def Definition, contexts []ValidationContext, resolver RoleResolver, scriptRoot *string) map[ValidationContext]ValidationResult {
	results := make(map[ValidationContext]ValidationResult, len(contexts))
	for _, context := range contexts {
		if _, exists := results[context]; exists {
			continue
		}
		result := ValidateDefinition(def, ValidationOptions{Context: context, RoleResolver: resolver})
		if context == ValidationContextExecution {
			result.Errors = append(result.Errors, definitionScriptPathErrors(def, scriptRoot)...)
		}
		results[context] = result
	}
	return results
}

func definitionScriptPathErrors(def Definition, rootPath *string) []ValidationError {
	var errors []ValidationError
	for _, node := range def.Nodes {
		if node.Kind() != NodeKindScript {
			continue
		}
		for _, diagnostic := range workflowscript.Validate(workflowscript.ValidationRequest{
			RawPath:  NodeScriptPath(node).String(),
			RootPath: rootPath,
		}) {
			errors = append(errors, ValidationError{
				Code:          ValidationErrorCode(diagnostic.Code),
				Message:       diagnostic.Message,
				WorkflowID:    def.ID,
				NodeID:        NodeIDOf(node),
				BlocksContext: diagnostic.Blocking,
			})
		}
	}
	return errors
}
