package workflowscript

import (
	"strings"

	"core/server/workflow"
)

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
			nodeID := workflow.NodeIDOf(node)
			validationError := workflow.ValidationError{
				Code:          workflow.ValidationErrorCode(diagnostic.Code),
				Message:       diagnostic.Message,
				WorkflowID:    workflow.WorkflowIDPointer(def.ID),
				BlocksContext: diagnostic.Blocking,
			}
			if strings.TrimSpace(string(nodeID)) != "" {
				validationError.NodeID = &nodeID
			}
			errors = append(errors, validationError)
		}
	}
	return errors
}
