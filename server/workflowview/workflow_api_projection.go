package workflowview

import (
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func DerivedWiring(def workflow.Definition) serverapi.WorkflowDerivedWiring {
	derived := workflow.DeriveWiring(def)
	resp := serverapi.WorkflowDerivedWiring{
		Diagnostics: ValidationErrors(workflow.OptionalWorkflowID(def.ID), derived.Diagnostics),
	}
	for _, node := range def.Nodes {
		nodeID := workflow.NodeIDOf(node)
		resp.Nodes = append(resp.Nodes, serverapi.WorkflowDerivedNodeWiring{
			NodeID:                  string(nodeID),
			PossibleProvisionFields: OutputFields(derived.PossibleProvisionFieldsForNode(nodeID)),
			JoinOutputFields:        OutputFields(derived.JoinOutputFieldsForNode(nodeID)),
		})
	}
	for _, group := range def.TransitionGroups {
		resp.TransitionGroups = append(resp.TransitionGroups, serverapi.WorkflowDerivedTransitionGroupWiring{
			TransitionGroupID:       string(group.ID),
			RequiredProvisionFields: OutputFields(derived.RequiredProvisionFieldsForTransitionGroup(group.ID)),
		})
	}
	for _, edge := range def.Edges {
		resp.Edges = append(resp.Edges, serverapi.WorkflowDerivedEdgeWiring{
			EdgeID:                  string(edge.ID),
			InputBindings:           InputBindings(derived.InputBindingsForEdge(edge.ID)),
			RequiredProvisionFields: OutputFields(derived.RequiredProvisionFieldsForEdge(edge.ID)),
			RequiredProviderFields:  OutputFields(derived.RequiredProviderFieldsForJoinEdge(edge.ID)),
		})
	}
	return resp
}

func ValidationErrors(inheritedWorkflowID *runtimeids.WorkflowID, errs []workflow.ValidationError) []serverapi.WorkflowValidationError {
	out := make([]serverapi.WorkflowValidationError, 0, len(errs))
	for _, err := range errs {
		projected := serverapi.WorkflowValidationError{
			Code:              string(err.Code),
			Message:           err.Message,
			WorkflowID:        err.WorkflowID,
			NodeID:            string(err.NodeID),
			TransitionGroupID: string(err.TransitionGroupID),
			EdgeID:            string(err.EdgeID),
			Details:           validationErrorDetails(err),
			RelatedIDs:        err.RelatedIDs,
			BlocksContext:     err.BlocksContext,
		}
		if projected.WorkflowID == nil {
			projected.WorkflowID = inheritedWorkflowID
		}
		out = append(out, projected)
	}
	return out
}

func validationErrorDetails(err workflow.ValidationError) *serverapi.WorkflowValidationErrorDetails {
	var requiredTool *string
	if err.RequiredTool != nil {
		value := string(*err.RequiredTool)
		requiredTool = &value
	}
	details := serverapi.WorkflowValidationErrorDetails{
		FieldName:      err.FieldName,
		InputName:      err.InputName,
		Placeholder:    err.Placeholder,
		ProviderEdgeID: string(err.ProviderEdgeID),
		Role:           err.AgentRole,
		RequiredTool:   requiredTool,
	}
	if details.FieldName == "" && details.InputName == "" && details.Placeholder == "" && details.ProviderEdgeID == "" && details.Role == nil && details.RequiredTool == nil {
		return nil
	}
	return &details
}

func OutputFields(in []workflow.OutputField) []serverapi.WorkflowOutputField {
	out := make([]serverapi.WorkflowOutputField, 0, len(in))
	for _, field := range in {
		out = append(out, serverapi.WorkflowOutputField{Name: field.Name, Description: field.Description})
	}
	return out
}

func InputBindings(in []workflow.InputBinding) []serverapi.WorkflowInputBinding {
	out := make([]serverapi.WorkflowInputBinding, 0, len(in))
	for _, binding := range in {
		out = append(out, serverapi.WorkflowInputBinding{Name: binding.Name, Source: string(binding.Source), Field: binding.Field})
	}
	return out
}
