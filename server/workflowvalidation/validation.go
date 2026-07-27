// Package workflowvalidation evaluates workflow definitions consistently at
// every server boundary that needs validation.
package workflowvalidation

import (
	"core/server/workflow"
	"core/server/workflowscript"
)

// EvaluateDefinition evaluates def for each requested context. Execution
// validation includes script-path diagnostics because script paths are part of
// the executable workflow contract.
func EvaluateDefinition(def workflow.Definition, contexts []workflow.ValidationContext, resolver workflow.RoleResolver, scriptRoot *string) map[workflow.ValidationContext]workflow.ValidationResult {
	results := make(map[workflow.ValidationContext]workflow.ValidationResult, len(contexts))
	for _, context := range contexts {
		if _, exists := results[context]; exists {
			continue
		}
		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
			Context:      context,
			RoleResolver: resolver,
		})
		if context == workflow.ValidationContextExecution {
			result.Errors = append(result.Errors, scriptPathErrors(def, scriptRoot)...)
		}
		results[context] = result
	}
	return results
}

func scriptPathErrors(def workflow.Definition, rootPath *string) []workflow.ValidationError {
	var errors []workflow.ValidationError
	for _, node := range def.Nodes {
		if node.Kind() != workflow.NodeKindScript {
			continue
		}
		for _, diagnostic := range workflowscript.Validate(workflowscript.ValidationRequest{
			RawPath:  workflow.NodeScriptPath(node).String(),
			RootPath: rootPath,
		}) {
			errors = append(errors, workflow.ValidationError{
				Code:          workflow.ValidationErrorCode(diagnostic.Code),
				Message:       diagnostic.Message,
				WorkflowID:    def.ID,
				NodeID:        workflow.NodeIDOf(node),
				BlocksContext: diagnostic.Blocking,
			})
		}
	}
	return errors
}
